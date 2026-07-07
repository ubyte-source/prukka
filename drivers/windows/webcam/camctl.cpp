// PrukkaWebcamCtl — the Prukka Webcam control tool.
//
//   PrukkaWebcamCtl install   register the DLL and create the camera
//   PrukkaWebcamCtl feed      stdin rawvideo YUY2 1280x720 → the camera
//
// `install` keeps the virtual camera alive while it runs (session
// lifetime — close it and the camera unplugs). `feed` is what the engine
// pipes into:
//   ffmpeg -i <hls> -f rawvideo -pix_fmt yuyv422 -s 1280x720 - | \
//     PrukkaWebcamCtl feed

#include <mfapi.h>
#include <mfidl.h>
#include <mfvirtualcamera.h>
#include <windows.h>
#include <wrl/wrappers/corewrappers.h>

#include <cstdio>
#include <cstring>
#include <io.h>
#include <fcntl.h>

#include "frame.h"
#include "guids.h"

using Microsoft::WRL::ComPtr;

static int Install() {
    wchar_t self[MAX_PATH] = {};
    GetModuleFileNameW(nullptr, self, MAX_PATH);

    // The DLL sits beside this exe; register it for the current user.
    wchar_t dll[MAX_PATH] = {};
    wcscpy_s(dll, self);
    wchar_t* slash = wcsrchr(dll, L'\\');
    wcscpy_s(slash + 1, MAX_PATH - (slash + 1 - dll), L"PrukkaWebcam.dll");

    HMODULE module = LoadLibraryW(dll);
    if (module == nullptr) {
        fwprintf(stderr, L"PrukkaWebcam.dll not found beside the exe\n");
        return 1;
    }

    auto reg = reinterpret_cast<HRESULT (*)()>(GetProcAddress(module, "DllRegisterServer"));
    if (reg == nullptr || FAILED(reg())) {
        fwprintf(stderr, L"DLL registration failed (run once as administrator)\n");
        return 1;
    }

    if (FAILED(MFStartup(MF_VERSION))) {
        return 1;
    }

    ComPtr<IMFVirtualCamera> camera;
    HRESULT hr = MFCreateVirtualCamera(
        MFVirtualCameraType_SoftwareCameraSource, MFVirtualCameraLifetime_Session,
        MFVirtualCameraAccess_CurrentUser, kFriendlyName, kActivateClsidString, nullptr, 0,
        &camera);
    if (FAILED(hr)) {
        fwprintf(stderr, L"MFCreateVirtualCamera: 0x%08lx (Windows 11 required)\n", hr);
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
    MFShutdown();

    return 0;
}

static int Feed() {
    HANDLE mapping = nullptr;
    prukka::SharedFrame* shared = prukka::OpenShared(&mapping);
    if (shared == nullptr) {
        fwprintf(stderr, L"cannot map the shared frame\n");
        return 1;
    }

    _setmode(_fileno(stdin), _O_BINARY);

    // Whole frames only: a torn read means the pipe ended.
    static uint8_t frame[prukka::kFrameBytes];
    for (;;) {
        size_t have = 0;
        while (have < sizeof(frame)) {
            size_t got = fread(frame + have, 1, sizeof(frame) - have, stdin);
            if (got == 0) {
                return 0;
            }

            have += got;
        }

        memcpy(const_cast<uint8_t*>(shared->pixels), frame, sizeof(frame));
        InterlockedIncrement(&shared->sequence);
    }
}

int wmain(int argc, wchar_t** argv) {
    if (argc == 2 && wcscmp(argv[1], L"install") == 0) {
        return Install();
    }

    if (argc == 2 && wcscmp(argv[1], L"feed") == 0) {
        return Feed();
    }

    fwprintf(stderr, L"usage: PrukkaWebcamCtl install | feed\n");

    return 2;
}
