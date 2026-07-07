//go:build windows

package devices

import (
	"strings"
	"testing"
)

// TestStatusReportsManualAudio: the unsigned kernel drivers stay
// manual, the webcam reports its record.
func TestStatusReportsManualAudio(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	results, err := status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("status returned %d results, want 3", len(results))
	}

	for _, result := range results[:2] {
		if result.State != StateManual || result.NextStep != audioNextStep {
			t.Errorf("%s = %+v, want manual with the signing note", result.Kind, result)
		}
	}

	if results[2].Kind != Webcam || results[2].State != StateMissing {
		t.Fatalf("webcam = %+v, want missing", results[2])
	}
}

// TestWebcamNextStepNamesTheController: the one action points at the
// staged resident controller.
func TestWebcamNextStepNamesTheController(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if got := webcamNextStep(); !strings.Contains(got, webcamCtl) || !strings.Contains(got, "install") {
		t.Fatalf("webcamNextStep = %q", got)
	}
}

func TestInstalledWebcamDLLUsesProgramData(t *testing.T) {
	t.Setenv("ProgramData", `D:\SharedData`)

	want := `D:\SharedData\Prukka\PrukkaWebcam.dll`
	if got := installedWebcamDLL(); got != want {
		t.Fatalf("installedWebcamDLL = %q, want %q", got, want)
	}
}
