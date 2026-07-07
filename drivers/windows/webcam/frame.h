// Prukka Webcam — the frame contract between the engine and the media
// source: one session-local shared-memory section holding the latest
// complete YUY2 frame. The feeder (PrukkaWebcamCtl feed) replaces it;
// the media source copies it out at frame rate. A branded splash shows
// before the first write, so the camera is never black.
#pragma once

#include <windows.h>

#include <cstdint>
#include <cstring>

namespace prukka {

constexpr uint32_t kWidth = 1280;
constexpr uint32_t kHeight = 720;
constexpr uint32_t kFrameBytes = kWidth * kHeight * 2; // YUY2
constexpr uint32_t kFrameRate = 30;
constexpr wchar_t kSharedName[] = L"Local\\PrukkaWebcamFrame";

// SharedFrame is the section layout: a write counter then the pixels.
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

// OpenShared maps the section, creating it (with the splash) on first use.
inline SharedFrame* OpenShared(HANDLE* mapping) {
    *mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, nullptr, PAGE_READWRITE, 0,
                                  sizeof(SharedFrame), kSharedName);
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
        InterlockedExchange(&shared->sequence, 1);
    }

    return shared;
}

} // namespace prukka
