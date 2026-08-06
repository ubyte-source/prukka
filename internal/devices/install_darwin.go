//go:build darwin

package devices

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// halDir is where coreaudiod discovers HAL plug-in bundles.
const halDir = "/Library/Audio/Plug-Ins/HAL"

// appsDir hosts the camera-extension app.
const appsDir = "/Applications"

// webcamApp is the camera app bundle name inside appsDir.
const webcamApp = "Prukka Camera.app"

// webcamNextStep names the macOS gap: activating a camera system extension
// requires a Developer-ID-signed, notarized build.
const webcamNextStep = "the camera extension needs a Developer-ID-signed release " +
	"(in progress) before macOS will activate it; the microphone and speaker " +
	"are fully functional today"

// audioBundles maps the audio kinds to their HAL bundle names.
var audioBundles = map[Kind]string{
	Microphone: "PrukkaMic.driver",
	Speaker:    "PrukkaSpeaker.driver",
}

// install places the HAL bundles and the camera app, restarting
// coreaudiod when the audio drivers changed.
func install(ctx context.Context) ([]Result, error) {
	pay, err := payloads()
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, 3)

	audioChanged := false

	for _, kind := range []Kind{Microphone, Speaker} {
		state, installErr := installPayload(kind, pay[string(kind)], halDir, audioBundles[kind])
		if installErr != nil {
			return nil, installErr
		}

		audioChanged = audioChanged || state == StateInstalled
		results = append(results, Result{Kind: kind, State: state})
	}

	if audioChanged {
		if kickErr := restartCoreaudio(ctx); kickErr != nil {
			return nil, kickErr
		}
	}

	webcam, webcamErr := installWebcam(ctx, pay[string(Webcam)])
	if webcamErr != nil {
		return nil, webcamErr
	}

	return append(results, webcam), nil
}

// installWebcam places the camera app; the user opens it to trigger the
// extension-approval flow.
func installWebcam(_ context.Context, data []byte) (Result, error) {
	state, err := installPayload(Webcam, data, appsDir, webcamApp)
	if err != nil {
		return Result{}, err
	}

	return webcamResult(state), nil
}

// webcamResult overlays the signing gap on a marker-derived webcam state: the
// app bundle being in place proves nothing about activation, so installed
// reads manual.
func webcamResult(state State) Result {
	switch state {
	case StateInstalled:
		return Result{Kind: Webcam, State: StateManual, NextStep: webcamNextStep}
	case StateSkipped:
		return Result{Kind: Webcam, State: StateSkipped, NextStep: webcamNextStep}
	default:
		return Result{Kind: Webcam, State: state}
	}
}

// restartCoreaudio makes coreaudiod rescan its HAL plug-ins. SIP denies
// kickstart on the system domain even to root, so a plain kill is the
// fallback.
func restartCoreaudio(ctx context.Context) error {
	kickErr := runTool(ctx, "launchctl", "kickstart", "-kp", "system/com.apple.audio.coreaudiod")
	if kickErr == nil {
		return nil
	}

	if killErr := runTool(ctx, "killall", "coreaudiod"); killErr != nil {
		return errors.Join(kickErr, killErr)
	}

	return nil
}

// remove deletes the HAL bundles and the camera app; deleting the app is
// how macOS retires its extension.
func remove(ctx context.Context) ([]Result, error) {
	results := make([]Result, 0, 3)

	audioRemoved := false

	for _, kind := range []Kind{Microphone, Speaker} {
		result, err := removeAt(kind, filepath.Join(halDir, audioBundles[kind]))
		if err != nil {
			return nil, err
		}

		audioRemoved = audioRemoved || result.State == StateRemoved
		results = append(results, result)
	}

	if audioRemoved {
		if kickErr := restartCoreaudio(ctx); kickErr != nil {
			return nil, kickErr
		}
	}

	webcam, webcamErr := removeAt(Webcam, filepath.Join(appsDir, webcamApp))
	if webcamErr != nil {
		return nil, webcamErr
	}
	if clearErr := clearInstallRecords(); clearErr != nil {
		return nil, clearErr
	}

	return append(results, webcam), nil
}

// status reports each device against the bundled payloads.
func status(context.Context) ([]Result, error) {
	return statusIn(halDir, appsDir)
}

// statusIn reports status against explicit install roots, which is what tests
// can write to.
func statusIn(hal, apps string) ([]Result, error) {
	pay, err := payloads()
	if err != nil && !errors.Is(err, ErrNotBundled) {
		return nil, err
	}

	results := make([]Result, 0, 3)
	for _, kind := range kinds() {
		results = append(results,
			deviceStatus(kind, expectedMarker(pay[string(kind)]), payloadDest(hal, apps, kind)))
	}

	return results, nil
}

// deviceStatus classifies one device from its marker and its destination. The
// presence check runs before the webcam overlay, so a deleted app bundle reads
// missing rather than carrying the signing note.
func deviceStatus(kind Kind, want, dest string) Result {
	state := markerState(kind, want)
	if state == StateInstalled {
		if info, err := os.Lstat(dest); err != nil || !info.IsDir() {
			return Result{Kind: kind, State: StateMissing, NextStep: missingFilesNextStep}
		}
	}

	if kind == Webcam {
		return webcamResult(state)
	}

	return Result{Kind: kind, State: state}
}

// payloadDest is where one kind's payload lives once installed.
func payloadDest(hal, apps string, kind Kind) string {
	if kind == Webcam {
		return filepath.Join(apps, webcamApp)
	}

	return filepath.Join(hal, audioBundles[kind])
}
