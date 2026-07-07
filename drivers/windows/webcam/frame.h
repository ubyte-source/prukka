// Prukka Webcam — the frame contract between the engine and the media
// source: one shared-memory section holding the latest complete YUY2
// frame, guarded by a seqlock. The elevated installer creates it in the
// Global\ namespace because the media source runs inside the frame
// server service (LOCAL SERVICE, session 0) while the feeder runs in
// the user's session; the DACL grants the service read and the
// interactive user write. A branded splash shows before the first
// write, so the camera is never black.
#pragma once

#include <windows.h>

#include <sddl.h>

#include <cstdint>
#include <cstring>

namespace prukka {

constexpr uint32_t kWidth = 1280;
constexpr uint32_t kHeight = 720;
constexpr uint32_t kFrameBytes = kWidth * kHeight * 2; // YUY2
constexpr uint32_t kFrameRate = 30;
constexpr wchar_t kSharedName[] = L"Global\\PrukkaWebcamFrame";

// SYSTEM and Administrators own the section, LOCAL SERVICE (the frame
// server) reads it, the interactive user (the feeder) reads and writes.
constexpr wchar_t kSharedSddl[] = L"D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;GR;;;LS)(A;;GRGW;;;IU)";

// SharedFrame is the section layout: a seqlock counter then the pixels.
// The writer makes the counter odd before touching the pixels and even
// after; a reader that sees an odd or changed counter retries.
struct SharedFrame {
    volatile LONG sequence;
    uint8_t pixels[kFrameBytes];
};

// Splash paints the Prukka idle frame: brand blue with a white band.
inline void Splash(uint8_t* frame) {
    const uint8_t blue[4] = {0x8a, 0xa5, 0x8a, 0x60};
    const uint8_t white[4] = {0xeb, 0x80, 0xeb, 0x80};

    for (uint32_t row = 0; row < kHeight; row++) {
        bool band = row >= kHeight / 2 - 24 && row < kHeight / 2 + 24;
        uint8_t* line = frame + static_cast<size_t>(row) * kWidth * 2;

        for (uint32_t pair = 0; pair < kWidth / 2; pair++) {
            memcpy(line + pair * 4, band ? white : blue, 4);
        }
    }
}

// CreateShared creates (or reopens) the section with the cross-session
// DACL and paints the splash. Creating a Global\ section requires the
// SeCreateGlobalPrivilege the elevated installer holds.
inline SharedFrame* CreateShared(HANDLE* mapping) {
    SECURITY_ATTRIBUTES security = {sizeof(security), nullptr, FALSE};
    if (!ConvertStringSecurityDescriptorToSecurityDescriptorW(
            kSharedSddl, SDDL_REVISION_1, &security.lpSecurityDescriptor, nullptr)) {
        return nullptr;
    }

    *mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, &security, PAGE_READWRITE, 0,
                                  sizeof(SharedFrame), kSharedName);
    LocalFree(security.lpSecurityDescriptor);
    if (*mapping == nullptr) {
        return nullptr;
    }

    bool fresh = GetLastError() != ERROR_ALREADY_EXISTS;

    auto* shared = static_cast<SharedFrame*>(
        MapViewOfFile(*mapping, FILE_MAP_ALL_ACCESS, 0, 0, sizeof(SharedFrame)));
    if (shared == nullptr) {
        CloseHandle(*mapping);
        *mapping = nullptr;

        return nullptr;
    }

    if (fresh) {
        Splash(shared->pixels);
        InterlockedExchange(&shared->sequence, 0);
    }

    return shared;
}

// OpenShared maps the existing section — FILE_MAP_READ for the media
// source, FILE_MAP_WRITE for the feeder. It never creates: the section
// exists for as long as the installer keeps the camera plugged.
inline SharedFrame* OpenShared(HANDLE* mapping, DWORD access) {
    *mapping = OpenFileMappingW(access, FALSE, kSharedName);
    if (*mapping == nullptr) {
        return nullptr;
    }

    auto* shared = static_cast<SharedFrame*>(
        MapViewOfFile(*mapping, access, 0, 0, sizeof(SharedFrame)));
    if (shared == nullptr) {
        CloseHandle(*mapping);
        *mapping = nullptr;

        return nullptr;
    }

    return shared;
}

} // namespace prukka
