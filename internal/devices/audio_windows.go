//go:build windows

package devices

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	microphoneHardwareID = `Root\PrukkaMic`
	speakerHardwareID    = `Root\PrukkaSpeaker`
)

type windowsAudioPackage struct {
	kind Kind
	inf  string
}

type windowsAudioManager interface {
	packages() ([]windowsAudioPackage, error)
	removeDevices(kind Kind) (removed, restart bool, err error)
	removePackage(inf string) error
}

type setupAPIWindowsAudio struct{}

func removeWindowsAudio(manager windowsAudioManager) ([]Result, error) {
	packages, err := manager.packages()
	if err != nil {
		return nil, err
	}

	removed := map[Kind]bool{}
	restart := map[Kind]bool{}
	for _, kind := range []Kind{Microphone, Speaker} {
		deviceRemoved, needsRestart, removeErr := manager.removeDevices(kind)
		if removeErr != nil {
			return nil, removeErr
		}
		removed[kind] = deviceRemoved
		restart[kind] = needsRestart
	}

	seen := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		key := strings.ToLower(pkg.inf)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		if err := manager.removePackage(pkg.inf); err != nil {
			return nil, fmt.Errorf("remove %s driver package %s: %w", pkg.kind, pkg.inf, err)
		}
		removed[pkg.kind] = true
	}

	results := make([]Result, 0, 2)
	for _, kind := range []Kind{Microphone, Speaker} {
		state := StateMissing
		if removed[kind] {
			state = StateRemoved
		}
		result := Result{Kind: kind, State: state}
		if restart[kind] {
			result.NextStep = "restart Windows to finish driver removal"
		}
		results = append(results, result)
	}

	return results, nil
}

func (setupAPIWindowsAudio) packages() (packages []windowsAudioPackage, err error) {
	class := mediaClassGUID()
	set, err := windows.SetupDiCreateDeviceInfoListEx(&class, 0, "")
	if err != nil {
		return nil, fmt.Errorf("create Windows audio driver inventory: %w", err)
	}
	defer func() { err = errors.Join(err, set.Close()) }()

	params, err := set.DeviceInstallParams(nil)
	if err != nil {
		return nil, fmt.Errorf("read Windows audio inventory parameters: %w", err)
	}
	params.FlagsEx |= windows.DI_FLAGSEX_ALLOWEXCLUDEDDRVS
	if setErr := set.SetDeviceInstallParams(nil, params); setErr != nil {
		return nil, fmt.Errorf("configure Windows audio driver inventory: %w", setErr)
	}
	if buildErr := set.BuildDriverInfoList(nil, windows.SPDIT_CLASSDRIVER); buildErr != nil {
		return nil, fmt.Errorf("build Windows audio driver inventory: %w", buildErr)
	}
	defer func() {
		err = errors.Join(err, set.DestroyDriverInfoList(nil, windows.SPDIT_CLASSDRIVER))
	}()

	for index := 0; ; index++ {
		driver, enumErr := set.EnumDriverInfo(nil, windows.SPDIT_CLASSDRIVER, index)
		if errors.Is(enumErr, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if enumErr != nil {
			return nil, fmt.Errorf("enumerate Windows audio driver packages: %w", enumErr)
		}

		pkg, ok, pkgErr := prukkaAudioPackage(set, driver)
		if pkgErr != nil {
			return nil, pkgErr
		}
		if ok {
			packages = append(packages, pkg)
		}
	}

	return packages, nil
}

// prukkaAudioPackage resolves one enumerated driver into a removable
// Prukka package; ok is false for other vendors' drivers.
func prukkaAudioPackage(set windows.DevInfo, driver *windows.DrvInfoData) (windowsAudioPackage, bool, error) {
	detail, err := set.DriverInfoDetail(nil, driver)
	if err != nil {
		return windowsAudioPackage{}, false, fmt.Errorf("inspect Windows audio driver package: %w", err)
	}
	kind, matched := audioKind(detail.HardwareID(), detail.CompatIDs())
	if !matched || !strings.EqualFold(driver.ProviderName(), "Prukka") {
		return windowsAudioPackage{}, false, nil
	}

	inf, valid := publishedINF(detail.InfFileName())
	if !valid {
		return windowsAudioPackage{}, false, fmt.Errorf(
			"invalid published INF %q for Prukka %s driver", detail.InfFileName(), kind)
	}

	return windowsAudioPackage{kind: kind, inf: inf}, true, nil
}

func (setupAPIWindowsAudio) removeDevices(kind Kind) (removed, restart bool, err error) {
	want, ok := audioHardwareID(kind)
	if !ok {
		return false, false, fmt.Errorf("unsupported Windows audio device %q", kind)
	}

	set, err := windows.SetupDiGetClassDevsEx(nil, "", 0, windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return false, false, fmt.Errorf("enumerate Windows devices: %w", err)
	}
	defer func() { err = errors.Join(err, set.Close()) }()

	matches, err := matchingHardwareDevices(set, want)
	if err != nil {
		return false, false, err
	}

	for _, device := range matches {
		if removeErr := set.CallClassInstaller(windows.DIF_REMOVE, device); removeErr != nil {
			if errors.Is(removeErr, windows.ERROR_NO_SUCH_DEVINST) {
				continue
			}
			return removed, restart, fmt.Errorf("remove Windows %s device: %w", kind, removeErr)
		}
		removed = true

		params, paramsErr := set.DeviceInstallParams(device)
		if paramsErr != nil {
			return removed, restart, fmt.Errorf("read Windows %s removal result: %w", kind, paramsErr)
		}
		restart = restart || params.Flags&(windows.DI_NEEDREBOOT|windows.DI_NEEDRESTART) != 0
	}

	return removed, restart, nil
}

func matchingHardwareDevices(set windows.DevInfo, want string) ([]*windows.DevInfoData, error) {
	var matches []*windows.DevInfoData
	for index := 0; ; index++ {
		device, enumErr := set.EnumDeviceInfo(index)
		if errors.Is(enumErr, windows.ERROR_NO_MORE_ITEMS) {
			return matches, nil
		}
		if enumErr != nil {
			return nil, fmt.Errorf("enumerate Windows devices: %w", enumErr)
		}

		ids, idsErr := deviceHardwareIDs(set, device)
		if idsErr != nil {
			return nil, idsErr
		}
		if containsFold(ids, want) {
			matches = append(matches, device)
		}
	}
}

func (setupAPIWindowsAudio) removePackage(inf string) error {
	err := windows.SetupUninstallOEMInf(inf, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_NOT_AN_INSTALLED_OEM_INF) {
		return nil
	}

	return err
}

func deviceHardwareIDs(set windows.DevInfo, device *windows.DevInfoData) ([]string, error) {
	value, err := set.DeviceRegistryProperty(device, windows.SPDRP_HARDWAREID)
	if errors.Is(err, windows.ERROR_INVALID_DATA) || errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Windows device hardware IDs: %w", err)
	}

	switch ids := value.(type) {
	case []string:
		return ids, nil
	case string:
		return []string{ids}, nil
	default:
		return nil, fmt.Errorf("unexpected Windows device hardware ID type %T", value)
	}
}

func audioKind(hardwareID string, compatibleIDs []string) (Kind, bool) {
	ids := append([]string{hardwareID}, compatibleIDs...)
	for _, kind := range []Kind{Microphone, Speaker} {
		want, _ := audioHardwareID(kind)
		if containsFold(ids, want) {
			return kind, true
		}
	}

	return "", false
}

func audioHardwareID(kind Kind) (string, bool) {
	switch kind {
	case Microphone:
		return microphoneHardwareID, true
	case Speaker:
		return speakerHardwareID, true
	default:
		return "", false
	}
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}

	return false
}

func publishedINF(path string) (string, bool) {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	if !strings.HasPrefix(name, "oem") || !strings.HasSuffix(name, ".inf") {
		return "", false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(name, "oem"), ".inf")
	if digits == "" {
		return "", false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}

	return name, true
}

func mediaClassGUID() windows.GUID {
	return windows.GUID{
		Data1: 0x4d36e96c,
		Data2: 0xe325,
		Data3: 0x11ce,
		Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18},
	}
}
