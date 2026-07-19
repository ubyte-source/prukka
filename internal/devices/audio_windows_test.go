//go:build windows

package devices

import (
	"reflect"
	"testing"
)

type fakeWindowsAudioManager struct {
	inventory       []windowsAudioPackage
	devicesRemoved  map[Kind]bool
	restartRequired map[Kind]bool
	packagesRemoved []string
}

func (f *fakeWindowsAudioManager) packages() ([]windowsAudioPackage, error) {
	return f.inventory, nil
}

func (f *fakeWindowsAudioManager) removeDevices(kind Kind) (removed, restart bool, err error) {
	return f.devicesRemoved[kind], f.restartRequired[kind], nil
}

func (f *fakeWindowsAudioManager) removePackage(inf string) error {
	f.packagesRemoved = append(f.packagesRemoved, inf)

	return nil
}

func TestRemoveWindowsAudioRemovesDevicesAndPackages(t *testing.T) {
	manager := &fakeWindowsAudioManager{
		inventory: []windowsAudioPackage{
			{kind: Microphone, inf: "oem3.inf"},
			{kind: Speaker, inf: "oem7.inf"},
			{kind: Speaker, inf: "OEM7.INF"},
		},
		devicesRemoved:  map[Kind]bool{Microphone: true},
		restartRequired: map[Kind]bool{Microphone: true},
	}

	results, err := removeWindowsAudio(manager)
	if err != nil {
		t.Fatalf("removeWindowsAudio: %v", err)
	}
	if len(results) != 2 || results[0].State != StateRemoved || results[1].State != StateRemoved {
		t.Fatalf("results = %+v", results)
	}
	if results[0].NextStep == "" || results[1].NextStep != "" {
		t.Fatalf("restart results = %+v", results)
	}
	if want := []string{"oem3.inf", "oem7.inf"}; !reflect.DeepEqual(manager.packagesRemoved, want) {
		t.Fatalf("removed packages = %v, want %v", manager.packagesRemoved, want)
	}
}

func TestRemoveWindowsAudioIsIdempotent(t *testing.T) {
	results, err := removeWindowsAudio(&fakeWindowsAudioManager{})
	if err != nil {
		t.Fatalf("removeWindowsAudio: %v", err)
	}
	for _, result := range results {
		if result.State != StateMissing {
			t.Fatalf("%s = %+v, want missing", result.Kind, result)
		}
	}
}

func TestWindowsAudioIdentity(t *testing.T) {
	for _, test := range []struct {
		path  string
		valid bool
	}{
		{path: `C:\Windows\INF\oem42.inf`, valid: true},
		{path: "OEM0.INF", valid: true},
		{path: "prukka_mic.inf", valid: false},
		{path: "oem1.inf.bak", valid: false},
		{path: "oemx.inf", valid: false},
	} {
		if _, valid := publishedINF(test.path); valid != test.valid {
			t.Errorf("publishedINF(%q) valid = %v, want %v", test.path, valid, test.valid)
		}
	}

	if kind, ok := audioKind("", []string{`ROOT\PRUKKASPEAKER`}); !ok || kind != Speaker {
		t.Fatalf("speaker identity = %q, %v", kind, ok)
	}
	if kind, ok := audioKind(`Root\PrukkaMic`, nil); !ok || kind != Microphone {
		t.Fatalf("microphone identity = %q, %v", kind, ok)
	}
}
