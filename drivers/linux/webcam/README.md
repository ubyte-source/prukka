# Prukka Webcam (Linux)

> A native V4L2 device: the engine writes the session's video (captions burned in), every camera app sees "Prukka Webcam" — with a branded splash before the first frame, never a black square.

## Build

```bash
make    # builds prukka_webcam.ko against your running kernel's headers
```

## Install

```bash
sudo insmod prukka_webcam.ko     # unload with: sudo rmmod prukka_webcam
v4l2-ctl --list-devices          # note the /dev/videoN it registered
```

## Use

```bash
prukka session push <slug> device://video//dev/videoN --lang en --subs burn
```

## Notes

Fixed format: 1280×720 YUYV at 30 fps, mmap streaming. With Secure Boot, sign
and enroll the module via MOK.
