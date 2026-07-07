# Prukka Webcam (Windows 11)

> A Media Foundation virtual camera — Microsoft's native virtual-camera API, user mode, no kernel driver: every camera app sees "Prukka Webcam", with a branded splash before the first frame.

## Build

```bat
build.cmd    :: MSVC alone (from a VS developer prompt)
```

## Install

Run **elevated** (the frame server loads the DLL into multiple processes, so
the COM class must be registered under HKLM — a non-elevated register lands in
HKCU and the camera won't load):

```bat
:: from an Administrator prompt
build\PrukkaWebcamCtl install    :: registers the DLL under HKLM + plugs the camera in
```

Keep the install window open — the camera's lifetime is the session; closing it
unplugs it.

## Use

Feed it the session's video (in another prompt):

```bat
ffmpeg -i http://127.0.0.1:8080/<slug>/master.m3u8 ^
  -f rawvideo -pix_fmt yuyv422 -s 1280x720 - | build\PrukkaWebcamCtl feed
```

## Notes

Fixed format: 1280×720 YUY2 at 30 fps. Requires Windows 11 (the
`MFCreateVirtualCamera` frame-server API).
