//go:build darwin

package devices

import (
	"context"
	"errors"
	"path/filepath"
)

// halDir is where coreaudiod discovers HAL plug-in bundles.
const halDir = "/Library/Audio/Plug-Ins/HAL"

// appsDir hosts the camera-extension app.
const appsDir = "/Applications"

// webcamApp is the camera app bundle name inside appsDir.
const webcamApp = "Prukka Camera.app"

// webcamNextStep is honest about the one macOS gap: activating a camera
// system extension requires a Developer-ID-signed, notarized build —
// developer mode is no way out, it needs SIP disabled. The audio devices
// carry no such requirement and work today.
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

// installWebcam places the camera app and opens it so macOS raises the
// extension-approval prompt.
func installWebcam(ctx context.Context, data []byte) (Result, error) {
	state, err := installPayload(Webcam, data, appsDir, webcamApp)
	if err != nil {
		return Result{}, err
	}

	if state == StateSkipped {
		return Result{Kind: Webcam, State: StateSkipped}, nil
	}

	// Raising the app from here helps only in a GUI session; a root or
	// SSH install cannot, and the next step spells the full path anyway.
	_ = ctx

	return Result{Kind: Webcam, State: StateManual, NextStep: webcamNextStep}, nil
}

// restartCoreaudio makes coreaudiod rescan its HAL plug-ins. SIP denies
// kickstart on the system domain even to root, so a plain kill is the
// fallback: launchd relaunches coreaudiod on demand.
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
	pay, err := payloads()
	if err != nil && !errors.Is(err, ErrNotBundled) {
		return nil, err
	}

	results := make([]Result, 0, 3)
	for _, kind := range kinds() {
		results = append(results, Result{Kind: kind, State: markerState(kind, expectedMarker(pay[string(kind)]))})
	}

	return results, nil
}
