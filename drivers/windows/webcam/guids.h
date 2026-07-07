// Prukka Webcam — the COM identity the frame server activates.
#pragma once

#include <windows.h>

// {81530786-7639-4DEF-BB04-85C9482CD274}
inline constexpr CLSID CLSID_PrukkaWebcamActivate = {
    0x81530786, 0x7639, 0x4def, {0xbb, 0x04, 0x85, 0xc9, 0x48, 0x2c, 0xd2, 0x74}};

inline constexpr wchar_t kActivateClsidString[] = L"{81530786-7639-4DEF-BB04-85C9482CD274}";
inline constexpr wchar_t kFriendlyName[] = L"Prukka Webcam";
