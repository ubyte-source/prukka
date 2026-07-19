// PrukkaWebcamCtl — the Prukka Webcam control tool.
//
//   PrukkaWebcamCtl install   register the DLL and create the camera
//                             (administrator required)
//   PrukkaWebcamCtl uninstall remove the camera and unregister the DLL
//   PrukkaWebcamCtl feed      stdin rawvideo YUY2 1280x720 → the camera
//   PrukkaWebcamCtl probe     verify that the resident camera is live
//
// `install` copies the DLL to ProgramData, registers it under HKLM,
// creates the cross-session frame section, and keeps the virtual camera
// alive while it runs (session lifetime — close it and the camera
// unplugs). `feed` is what the engine pipes into:
//   ffmpeg -i <hls> -f rawvideo -pix_fmt yuyv422 -s 1280x720 - | \
//     PrukkaWebcamCtl feed

#include <windows.h>

#include <mfapi.h>
#include <mfidl.h>
#include <mfvirtualcamera.h>
#include <shlobj.h>
#include <wrl/client.h>

#include <cstdio>
#include <cstring>
#include <io.h>
#include <fcntl.h>

#include "frame.h"
#include "guids.h"

using Microsoft::WRL::ComPtr;

using CreateVirtualCameraFn = HRESULT(WINAPI*)(
    MFVirtualCameraType, MFVirtualCameraLifetime, MFVirtualCameraAccess,
    LPCWSTR, LPCWSTR, const GUID*, ULONG, IMFVirtualCamera**);

static bool InstalledPaths(wchar_t* dir, size_t dirCount, wchar_t* dll, size_t dllCount) {
    // Resolved from the OS, never the environment: this process runs
    // elevated and later loads and registers a DLL from this directory,
    // so an attacker-shaped %ProgramData% must not redirect it.
    PWSTR programData = nullptr;
    if (FAILED(SHGetKnownFolderPath(FOLDERID_ProgramData, KF_FLAG_DEFAULT, nullptr,
                                    &programData))) {
        return false;
    }

    const bool ok = swprintf_s(dir, dirCount, L"%s\\Prukka", programData) >= 0 &&
                    swprintf_s(dll, dllCount, L"%s\\PrukkaWebcam.dll", dir) >= 0;
    CoTaskMemFree(programData);

    return ok;
}

static CreateVirtualCameraFn LoadVirtualCameraFactory(HMODULE* sensorGroup) {
    *sensorGroup = LoadLibraryW(L"mfsensorgroup.dll");
    if (*sensorGroup == nullptr) {
        return nullptr;
    }

    return reinterpret_cast<CreateVirtualCameraFn>(
        GetProcAddress(*sensorGroup, "MFCreateVirtualCamera"));
}

static int Uninstall();

// InstallSession unwinds Install()'s acquisitions in reverse order on
// every return path, so each failure branch is a plain `return 1` and
// the compiler proves the cleanup instead of six hand-rolled ladders.
// Success flips keepInstalled: the DLL registration outlives the run.
struct InstallSession {
    prukka::SharedFrame* shared = nullptr;
    HANDLE mapping = nullptr;
    HMODULE sensorGroup = nullptr;
    bool comReady = false;
    bool mfReady = false;
    bool keepInstalled = false;

    ~InstallSession() {
        if (sensorGroup != nullptr) {
            FreeLibrary(sensorGroup);
        }
        if (mfReady) {
            MFShutdown();
        }
        if (comReady) {
            CoUninitialize();
        }
        if (shared != nullptr) {
            UnmapViewOfFile(shared);
        }
        if (mapping != nullptr) {
            CloseHandle(mapping);
        }
        if (!keepInstalled) {
            Uninstall();
        }
    }
};

static int Install() {
    wchar_t self[MAX_PATH] = {};
    const DWORD selfLength = GetModuleFileNameW(nullptr, self, MAX_PATH);
    if (selfLength == 0 || selfLength >= MAX_PATH) {
        fwprintf(stderr, L"cannot locate PrukkaWebcamCtl.exe\n");
        return 1;
    }

    wchar_t source[MAX_PATH] = {};
    wcscpy_s(source, self);
    wchar_t* slash = wcsrchr(source, L'\\');
    if (slash == nullptr) {
        fwprintf(stderr, L"cannot locate PrukkaWebcam.dll beside the controller\n");
        return 1;
    }
    wcscpy_s(slash + 1, MAX_PATH - (slash + 1 - source), L"PrukkaWebcam.dll");

    // The frame server runs as LOCAL SERVICE and cannot read the user
    // profile, so the DLL is copied to ProgramData and registered from
    // there. The copy also sheds any mark-of-the-web the download left.
    wchar_t dir[MAX_PATH] = {};
    wchar_t dll[MAX_PATH] = {};
    if (!InstalledPaths(dir, MAX_PATH, dll, MAX_PATH)) {
        return 1;
    }

    if (!CreateDirectoryW(dir, nullptr) && GetLastError() != ERROR_ALREADY_EXISTS) {
        fwprintf(stderr, L"cannot create %s (run as administrator)\n", dir);
        return 1;
    }
    if (!CopyFileW(source, dll, FALSE)) {
        fwprintf(stderr,
                 L"cannot update PrukkaWebcam.dll at %s; close the active Prukka Webcam "
                 L"controller and camera applications, then retry\n",
                 dll);
        return 1;
    }

    wchar_t zone[MAX_PATH + 16];
    swprintf_s(zone, L"%s:Zone.Identifier", dll);
    DeleteFileW(zone);

    HMODULE module = LoadLibraryW(dll);
    if (module == nullptr) {
        fwprintf(stderr, L"cannot load %s\n", dll);
        DeleteFileW(dll);
        RemoveDirectoryW(dir);
        return 1;
    }

    auto reg = reinterpret_cast<HRESULT (*)()>(GetProcAddress(module, "DllRegisterServer"));
    const HRESULT registerResult = reg == nullptr ? E_NOINTERFACE : reg();
    FreeLibrary(module);
    if (FAILED(registerResult)) {
        fwprintf(stderr, L"DLL registration failed (run as administrator)\n");
        DeleteFileW(dll);
        RemoveDirectoryW(dir);
        return 1;
    }

    // The cross-session frame section must exist before the frame server
    // activates the source, and must stay alive as long as the camera is
    // plugged — this process holds it. camera is declared after session
    // so it releases first, before the library it came from unloads.
    InstallSession session;
    ComPtr<IMFVirtualCamera> camera;
    session.shared = prukka::CreateShared(&session.mapping);
    if (session.shared == nullptr) {
        fwprintf(stderr, L"cannot create the shared frame (run as administrator)\n");
        return 1;
    }

    if (FAILED(CoInitializeEx(nullptr, COINIT_MULTITHREADED))) {
        return 1;
    }
    session.comReady = true;

    if (FAILED(MFStartup(MF_VERSION))) {
        return 1;
    }
    session.mfReady = true;

    // MFCreateVirtualCamera exists only on Windows 11: bound statically,
    // the loader would kill this process on Windows 10 with an opaque
    // missing-entry-point dialog before any message could print.
    CreateVirtualCameraFn createCamera = LoadVirtualCameraFactory(&session.sensorGroup);
    if (createCamera == nullptr) {
        fwprintf(stderr, L"virtual cameras need Windows 11 (MFCreateVirtualCamera is missing)\n");
        return 1;
    }

    HRESULT hr = createCamera(
        MFVirtualCameraType_SoftwareCameraSource, MFVirtualCameraLifetime_Session,
        MFVirtualCameraAccess_CurrentUser, kFriendlyName, kActivateClsidString, nullptr, 0,
        &camera);
    if (FAILED(hr)) {
        fwprintf(stderr, L"MFCreateVirtualCamera: 0x%08lx\n", hr);
        return 1;
    }

    hr = camera->Start(nullptr);
    if (FAILED(hr)) {
        fwprintf(stderr, L"camera Start: 0x%08lx\n", hr);
        return 1;
    }

    wprintf(L"Prukka Webcam is live — every camera app can select it now.\n");
    wprintf(L"Keep this window open; press Enter to unplug.\n");
    getwchar();

    camera->Remove();
    session.keepInstalled = true;

    return 0;
}

static HRESULT RemoveVirtualCamera() {
    const HRESULT initialized = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    if (FAILED(initialized)) {
        return initialized;
    }

    HRESULT result = MFStartup(MF_VERSION);
    if (FAILED(result)) {
        CoUninitialize();
        return result;
    }

    HMODULE sensorGroup = nullptr;
    CreateVirtualCameraFn createCamera = LoadVirtualCameraFactory(&sensorGroup);
    if (createCamera == nullptr) {
        result = HRESULT_FROM_WIN32(ERROR_PROC_NOT_FOUND);
    } else {
        ComPtr<IMFVirtualCamera> camera;
        result = createCamera(
            MFVirtualCameraType_SoftwareCameraSource, MFVirtualCameraLifetime_Session,
            MFVirtualCameraAccess_CurrentUser, kFriendlyName, kActivateClsidString, nullptr, 0,
            &camera);
        if (SUCCEEDED(result)) {
            result = camera->Remove();
        }
    }

    if (sensorGroup != nullptr) {
        FreeLibrary(sensorGroup);
    }
    MFShutdown();
    CoUninitialize();

    return result;
}

static bool CameraIsActive() {
    HANDLE mapping = nullptr;
    prukka::SharedFrame* shared = prukka::OpenShared(&mapping, FILE_MAP_READ);
    if (shared == nullptr) {
        return false;
    }

    UnmapViewOfFile(shared);
    CloseHandle(mapping);

    return true;
}

static int Uninstall() {
    wchar_t dir[MAX_PATH] = {};
    wchar_t dll[MAX_PATH] = {};
    if (!InstalledPaths(dir, MAX_PATH, dll, MAX_PATH)) {
        return 1;
    }

    // Whole-HRESULT compare: HRESULT_CODE would strip the facility and
    // treat ANY failure whose low 16 bits are 127 as the benign
    // "Windows 10, no MFCreateVirtualCamera" case.
    const HRESULT cameraResult = CameraIsActive() ? RemoveVirtualCamera() : S_OK;
    const bool cameraFailed = FAILED(cameraResult) &&
                              cameraResult != HRESULT_FROM_WIN32(ERROR_PROC_NOT_FOUND);
    if (cameraFailed) {
        fwprintf(stderr, L"virtual camera removal failed: 0x%08lx\n", cameraResult);
        return 1;
    }

    HRESULT unregisterResult = S_OK;
    HMODULE module = LoadLibraryW(dll);
    if (module != nullptr) {
        auto unregister = reinterpret_cast<HRESULT (*)()>(
            GetProcAddress(module, "DllUnregisterServer"));
        unregisterResult = unregister == nullptr ? E_NOINTERFACE : unregister();
        FreeLibrary(module);
    } else {
        const DWORD loadError = GetLastError();
        if (loadError != ERROR_MOD_NOT_FOUND && loadError != ERROR_FILE_NOT_FOUND) {
            unregisterResult = HRESULT_FROM_WIN32(loadError);
        } else {
            wchar_t key[160];
            swprintf_s(key, L"Software\\Classes\\CLSID\\%s", kActivateClsidString);
            const LSTATUS status = RegDeleteTreeW(HKEY_LOCAL_MACHINE, key);
            if (status != ERROR_SUCCESS && status != ERROR_FILE_NOT_FOUND) {
                unregisterResult = HRESULT_FROM_WIN32(status);
            }
        }
    }

    if (FAILED(unregisterResult)) {
        fwprintf(stderr, L"DLL unregistration failed: 0x%08lx (run as administrator)\n",
                 unregisterResult);
    }

    bool deleted = false;
    for (int attempt = 0; attempt < 20; ++attempt) {
        if (DeleteFileW(dll) || GetLastError() == ERROR_FILE_NOT_FOUND) {
            deleted = true;
            break;
        }
        Sleep(100);
    }
    if (!deleted) {
        fwprintf(stderr, L"cannot remove %s; close camera applications and retry\n", dll);
    }

    RemoveDirectoryW(dir);

    return (FAILED(unregisterResult) || !deleted) ? 1 : 0;
}

static int Feed() {
    HANDLE mapping = nullptr;
    prukka::SharedFrame* shared = prukka::OpenShared(&mapping, FILE_MAP_WRITE);
    if (shared == nullptr) {
        fwprintf(stderr, L"shared frame unavailable — run PrukkaWebcamCtl install first\n");
        return 1;
    }

    _setmode(_fileno(stdin), _O_BINARY);
    printf("ready\n");
    fflush(stdout);

    // Whole frames only: a torn read means the pipe ended. The seqlock
    // goes odd for the duration of the copy so readers never see a torn
    // frame.
    static uint8_t frame[prukka::kFrameBytes];
    for (;;) {
        size_t have = 0;
        while (have < sizeof(frame)) {
            size_t got = fread(frame + have, 1, sizeof(frame) - have, stdin);
            if (got == 0) {
                int status = ferror(stdin) == 0 ? 0 : 1;
                UnmapViewOfFile(shared);
                CloseHandle(mapping);

                return status;
            }

            have += got;
        }

        InterlockedIncrement(&shared->sequence);
        memcpy(const_cast<uint8_t*>(shared->pixels), frame, sizeof(frame));
        InterlockedIncrement(&shared->sequence);
    }
}

static int Probe() {
    HANDLE mapping = nullptr;
    prukka::SharedFrame* shared = prukka::OpenShared(&mapping, FILE_MAP_READ);
    if (shared == nullptr) {
        return 1;
    }

    UnmapViewOfFile(shared);
    CloseHandle(mapping);

    return 0;
}

int wmain(int argc, wchar_t** argv) {
    if (argc == 2 && wcscmp(argv[1], L"install") == 0) {
        return Install();
    }

    if (argc == 2 && wcscmp(argv[1], L"uninstall") == 0) {
        return Uninstall();
    }

    if (argc == 2 && wcscmp(argv[1], L"feed") == 0) {
        return Feed();
    }

    if (argc == 2 && wcscmp(argv[1], L"probe") == 0) {
        return Probe();
    }

    fwprintf(stderr, L"usage: PrukkaWebcamCtl install | uninstall | feed | probe\n");

    return 2;
}
