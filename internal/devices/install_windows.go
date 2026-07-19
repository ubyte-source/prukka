//go:build windows

package devices

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// InstallHint is the privileged command that (re)installs the drivers on
// this OS.
func InstallHint() string {
	return executable() + " devices install (as administrator)"
}

// RequirePrivilege fails before touching system registration when the
// current UAC token is not elevated.
func RequirePrivilege(verb string) error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}

	return fmt.Errorf("managing drivers needs administrator rights — run `prukka devices %s` in an elevated shell", verb)
}

// privilegeHint completes permission errors with the missing privilege.
const privilegeHint = "administrator required — run from an elevated shell"

// audioNextStep explains why the audio drivers stay manual: PortCls
// kernel drivers need signing Windows does not grant unsigned builds.
const audioNextStep = "Windows requires signed kernel audio drivers — see docs/DEVICES.md for test-signing"

// webcamCtl is the controller executable inside the staged webcam dir.
const webcamCtl = "PrukkaWebcamCtl.exe"

const webcamClassKey = `Software\Classes\CLSID\{81530786-7639-4DEF-BB04-85C9482CD274}`

// webcamDir is where the user-mode camera driver is staged.
func webcamDir() string {
	return filepath.Join(devicesDir(), "webcam")
}

// webcamNextStep words the one action Windows needs: the controller is a
// resident process — the camera lives for as long as it runs.
func webcamNextStep() string {
	return "run as Administrator and keep open: " + filepath.Join(webcamDir(), webcamCtl) + " install"
}

// install stages the user-mode camera driver; the audio devices stay
// manual until the kernel drivers are signed.
func install(context.Context) ([]Result, error) {
	pay, err := payloads()
	if err != nil {
		return nil, err
	}

	results := []Result{
		{Kind: Microphone, State: StateManual, NextStep: audioNextStep},
		{Kind: Speaker, State: StateManual, NextStep: audioNextStep},
	}

	state, stageErr := installPayload(Webcam, pay[string(Webcam)], devicesDir(), "webcam")
	if stageErr != nil {
		return nil, stageErr
	}

	if state == StateSkipped {
		return append(results, Result{Kind: Webcam, State: StateSkipped}), nil
	}

	return append(results, Result{Kind: Webcam, State: StateManual, NextStep: webcamNextStep()}), nil
}

// remove unregisters every live device and deletes its driver packages,
// deployed files and staging records.
func remove(ctx context.Context) ([]Result, error) {
	audio, err := removeWindowsAudio(setupAPIWindowsAudio{})
	if err != nil {
		return nil, err
	}

	if webcamErr := uninstallWebcam(ctx); webcamErr != nil {
		return nil, webcamErr
	}

	webcam, err := removeAt(Webcam, webcamDir())
	if err != nil {
		return nil, err
	}
	if clearErr := clearInstallRecords(); clearErr != nil {
		return nil, clearErr
	}

	return append(audio, webcam), nil
}

// uninstallWebcam lets the controller remove the live Media Foundation
// camera and its COM registration before staged files disappear. The
// registry fallback repairs incomplete installs whose controller vanished.
func uninstallWebcam(ctx context.Context) error {
	controller := filepath.Join(webcamDir(), webcamCtl)
	if info, err := os.Stat(controller); err == nil && !info.IsDir() {
		uninstallErr := runTool(ctx, controller, "uninstall")
		// taskkill reaps a lingering resident controller; it exits nonzero
		// when none runs, so its error only matters if the retry fails too.
		killErr := runTool(ctx, "taskkill", "/F", "/T", "/IM", webcamCtl)
		if uninstallErr != nil {
			if retryErr := runTool(ctx, controller, "uninstall"); retryErr != nil {
				return errors.Join(uninstallErr, killErr, retryErr)
			}
		}

		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect webcam controller: %w", err)
	}

	if err := deleteWebcamClass(); err != nil {
		return err
	}

	dll := installedWebcamDLL()
	if err := os.Remove(dll); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove installed webcam DLL %s: %w", dll, err)
	}

	return nil
}

func deleteWebcamClass() error {
	for _, key := range []string{webcamClassKey + `\InprocServer32`, webcamClassKey} {
		if err := registry.DeleteKey(registry.LOCAL_MACHINE, key); err != nil &&
			!errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("remove webcam registry key %s: %w", key, err)
		}
	}

	return nil
}

func installedWebcamDLL() string {
	return filepath.Join(programDataRoot(), "PrukkaWebcam.dll")
}

// status reports the staged camera against the bundled payload; the
// audio devices always read manual.
func status(context.Context) ([]Result, error) {
	pay, err := payloads()
	if err != nil && !errors.Is(err, ErrNotBundled) {
		return nil, err
	}

	webcam := Result{Kind: Webcam, State: markerState(Webcam, expectedMarker(pay[string(Webcam)]))}
	if webcam.State == StateInstalled {
		if _, statErr := os.Stat(filepath.Join(webcamDir(), webcamCtl)); statErr != nil {
			webcam = Result{Kind: Webcam, State: StateMissing, NextStep: "staged files are gone — run: prukka devices install"}
		}
	}

	return []Result{
		{Kind: Microphone, State: StateManual, NextStep: audioNextStep},
		{Kind: Speaker, State: StateManual, NextStep: audioNextStep},
		webcam,
	}, nil
}
