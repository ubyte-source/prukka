// Prukka Webcam — DLL plumbing: the WRL module serves the activate class
// and DllRegisterServer writes the classic InprocServer32 registration
// (regsvr32 PrukkaWebcam.dll; PrukkaWebcamCtl does it for the user).

#include <windows.h>
#include <wrl/module.h>

#include "guids.h"

using Microsoft::WRL::InProc;
using Microsoft::WRL::Module;

STDAPI DllGetClassObject(REFCLSID clsid, REFIID riid, void** out) {
    return Module<InProc>::GetModule().GetClassObject(clsid, riid, out);
}

STDAPI DllCanUnloadNow() {
    return Module<InProc>::GetModule().Terminate() ? S_OK : S_FALSE;
}

STDAPI_(BOOL) DllMain(HINSTANCE, DWORD reason, void*) {
    if (reason == DLL_PROCESS_ATTACH) {
        DisableThreadLibraryCalls(GetModuleHandleW(nullptr));
    }

    return TRUE;
}

// registryPath is HKCR\CLSID\{...} for this DLL's activate class.
static HRESULT WriteRegistration() {
    wchar_t path[MAX_PATH] = {};
    if (GetModuleFileNameW(reinterpret_cast<HMODULE>(&__ImageBase), path, MAX_PATH) == 0) {
        return HRESULT_FROM_WIN32(GetLastError());
    }

    // The frame server loads this DLL into multiple processes, so the class
    // must live under HKLM, not HKCU. Write the machine hive explicitly:
    // HKEY_CLASSES_ROOT would silently redirect to HKCU for a non-elevated
    // caller and the camera would never load. Registration therefore needs
    // an elevated `PrukkaWebcamCtl install`.
    wchar_t key[160];
    swprintf_s(key, L"Software\\Classes\\CLSID\\%s\\InprocServer32", kActivateClsidString);

    HKEY handle = nullptr;
    LSTATUS status =
        RegCreateKeyExW(HKEY_LOCAL_MACHINE, key, 0, nullptr, 0, KEY_WRITE, nullptr, &handle,
                        nullptr);
    if (status != ERROR_SUCCESS) {
        return HRESULT_FROM_WIN32(status);
    }

    RegSetValueExW(handle, nullptr, 0, REG_SZ, reinterpret_cast<const BYTE*>(path),
                   static_cast<DWORD>((wcslen(path) + 1) * sizeof(wchar_t)));

    const wchar_t model[] = L"Both";
    RegSetValueExW(handle, L"ThreadingModel", 0, REG_SZ,
                   reinterpret_cast<const BYTE*>(model), sizeof(model));
    RegCloseKey(handle);

    return S_OK;
}

STDAPI DllRegisterServer() { return WriteRegistration(); }

STDAPI DllUnregisterServer() {
    wchar_t key[160];
    swprintf_s(key, L"Software\\Classes\\CLSID\\%s", kActivateClsidString);

    return HRESULT_FROM_WIN32(RegDeleteTreeW(HKEY_LOCAL_MACHINE, key));
}
