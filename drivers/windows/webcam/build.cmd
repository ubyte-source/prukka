@echo off
rem Build the Prukka Webcam with the native MSVC toolchain alone.
rem Run from a VS developer prompt, or let CI call vcvars64 first.
setlocal
cd /d "%~dp0"

if not exist build mkdir build

echo ==^> compiling PrukkaWebcam.dll
cl /nologo /std:c++17 /W4 /WX /O2 /EHsc /LD source.cpp dllmain.cpp ^
  /Fo:build\ /Fe:build\PrukkaWebcam.dll ^
  /link /WX mfplat.lib mfuuid.lib mfsensorgroup.lib ole32.lib advapi32.lib runtimeobject.lib ^
  /EXPORT:DllGetClassObject,PRIVATE /EXPORT:DllCanUnloadNow,PRIVATE ^
  /EXPORT:DllRegisterServer,PRIVATE /EXPORT:DllUnregisterServer,PRIVATE
if errorlevel 1 exit /b 1

echo ==^> compiling PrukkaWebcamCtl.exe
cl /nologo /std:c++17 /W4 /WX /O2 /EHsc camctl.cpp ^
  /Fo:build\ /Fe:build\PrukkaWebcamCtl.exe ^
  /link /WX mfplat.lib mfuuid.lib mfsensorgroup.lib ole32.lib advapi32.lib runtimeobject.lib shell32.lib uuid.lib
if errorlevel 1 exit /b 1

echo ==^> built: build\PrukkaWebcam.dll + build\PrukkaWebcamCtl.exe
echo Install: PrukkaWebcamCtl install   (keeps the camera plugged while open)
echo Feed:    ffmpeg ... -f rawvideo -pix_fmt yuyv422 -s 1280x720 - ^| PrukkaWebcamCtl feed
