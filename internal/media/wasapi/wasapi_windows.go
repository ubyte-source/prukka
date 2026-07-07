//go:build windows

// COM interop: vtable dispatch crosses unsafe.Pointer by construction; no
// arithmetic ever touches a Go-managed pointer.

//nolint:gosec // G103/G115: COM interop; audited, maintainer-approved like internal/ring
package wasapi

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// device://audio/<id>: "default" selects the default render endpoint; any
// other id is a full endpoint ID string (mmdeviceapi).
const scheme = "device://audio/"

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioRenderClient   = guid{0xF294ACFC, 0x3146, 0x4483, [8]byte{0xA7, 0xBF, 0xAD, 0xDC, 0xA7, 0xC2, 0x60, 0xE2}}
)

const (
	coinitMultithreaded = 0x0
	clsctxAll           = 0x17
	eRender             = 0
	eMultimedia         = 1
	sharemodeShared     = 0
	bufferDuration100ns = 2_000_000 // 200 ms device buffer
)

// waveFormatEx mirrors WAVEFORMATEX; the shared-mode mix format that
// follows it (EXTENSIBLE) is IEEE float on every modern endpoint.
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

// com is a raw COM object — a foreign pointer, never Go-managed, so vtable
// walks stay inside unsafe.Add.
type com struct{ p unsafe.Pointer }

// call invokes vtable slot n with the object and args.
func (c com) call(slot uintptr, args ...uintptr) uintptr {
	vtable := *(*unsafe.Pointer)(c.p)
	method := *(*uintptr)(unsafe.Add(vtable, slot*unsafe.Sizeof(uintptr(0))))

	full := append([]uintptr{uintptr(c.p)}, args...)
	ret, _, errno := syscall.SyscallN(method, full...)
	_ = errno // COM reports failure via the HRESULT return

	return ret
}

// release drops one COM reference (IUnknown slot 2).
func (c com) release() {
	if c.p != nil {
		c.call(2)
	}
}

// writer owns the COM objects on one locked OS thread; Write converts and
// forwards frames to that thread over a channel.
type writer struct {
	frames chan []float32
	errs   chan error
	done   chan struct{}
	rate   int
	chans  int
}

// Open connects a device://audio/<id> target to its render endpoint,
// returning a WriteCloser for reference PCM.
func Open(target string) (io.WriteCloser, error) {
	id, ok := strings.CutPrefix(target, scheme)
	if !ok || id == "" {
		return nil, fmt.Errorf("wasapi: target %q is not device://audio/<id>", target)
	}

	w := &writer{
		frames: make(chan []float32, 4),
		errs:   make(chan error, 1),
		done:   make(chan struct{}),
	}

	format := make(chan [2]int, 1)

	go w.run(id, format)

	select {
	case err := <-w.errs:
		return nil, err
	case f := <-format:
		w.rate, w.chans = f[0], f[1]

		return w, nil
	}
}

// run is the COM thread: initialize, resolve the endpoint, start the
// client, then drain frames until Close.
func (w *writer) run(id string, format chan<- [2]int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)

	ret, _, errno := procCoInitializeEx.Call(0, coinitMultithreaded)
	_ = errno

	if int32(ret) < 0 {
		w.errs <- fmt.Errorf("wasapi: CoInitializeEx: %#x", ret)

		return
	}
	defer callProc(procCoUninitialize)

	client, render, wfx, err := openEndpoint(id)
	if err != nil {
		w.errs <- err

		return
	}
	defer client.release()
	defer render.release()

	format <- [2]int{int(wfx.SamplesPerSec), int(wfx.Channels)}

	// IAudioClient::GetBufferSize (slot 4).
	var bufferFrames uint32
	if ret := client.call(4, uintptr(unsafe.Pointer(&bufferFrames))); int32(ret) < 0 || bufferFrames == 0 {
		w.errs <- fmt.Errorf("wasapi: GetBufferSize: %#x (%d frames)", ret, bufferFrames)

		return
	}

	if ret := client.call(10); int32(ret) < 0 { // Start
		w.errs <- fmt.Errorf("wasapi: Start: %#x", ret)

		return
	}
	defer client.call(11) // Stop

	for frames := range w.frames {
		if err := submit(client, render, bufferFrames, uint32(wfx.Channels), frames); err != nil {
			w.errs <- err

			return
		}
	}
}

// openEndpoint resolves the device and initializes its audio client.
func openEndpoint(id string) (client, render com, wfx *waveFormatEx, err error) {
	var enumerator com

	ret, _, errno := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator.p)))
	_ = errno

	if int32(ret) < 0 {
		return com{}, com{}, nil, fmt.Errorf("wasapi: create enumerator: %#x", ret)
	}
	defer enumerator.release()

	device, err := resolveDevice(enumerator, id)
	if err != nil {
		return com{}, com{}, nil, err
	}
	defer device.release()

	// IMMDevice::Activate (slot 3)
	ret = device.call(3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0,
		uintptr(unsafe.Pointer(&client.p)))
	if int32(ret) < 0 {
		return com{}, com{}, nil, fmt.Errorf("wasapi: Activate: %#x", ret)
	}

	// IAudioClient::GetMixFormat (slot 8)
	var wfxPtr *waveFormatEx
	if ret = client.call(8, uintptr(unsafe.Pointer(&wfxPtr))); int32(ret) < 0 || wfxPtr == nil {
		client.release()

		return com{}, com{}, nil, fmt.Errorf("wasapi: GetMixFormat: %#x", ret)
	}

	mix := *wfxPtr

	// IAudioClient::Initialize (slot 3), shared mode at the mix format.
	ret = client.call(3, sharemodeShared, 0, bufferDuration100ns, 0,
		uintptr(unsafe.Pointer(wfxPtr)), 0)
	callProc(procCoTaskMemFree, uintptr(unsafe.Pointer(wfxPtr)))

	if int32(ret) < 0 {
		client.release()

		return com{}, com{}, nil, fmt.Errorf("wasapi: Initialize: %#x", ret)
	}

	// IAudioClient::GetService (slot 14) → IAudioRenderClient
	if ret = client.call(14, uintptr(unsafe.Pointer(&iidIAudioRenderClient)),
		uintptr(unsafe.Pointer(&render.p))); int32(ret) < 0 {
		client.release()

		return com{}, com{}, nil, fmt.Errorf("wasapi: GetService: %#x", ret)
	}

	return client, render, &mix, nil
}

// resolveDevice looks up "default" or a full endpoint ID string.
func resolveDevice(enumerator com, id string) (com, error) {
	var device com

	var ret uintptr

	if id == "default" {
		// IMMDeviceEnumerator::GetDefaultAudioEndpoint (slot 4)
		ret = enumerator.call(4, eRender, eMultimedia, uintptr(unsafe.Pointer(&device.p)))
	} else {
		wide, wideErr := syscall.UTF16PtrFromString(id)
		if wideErr != nil {
			return com{}, fmt.Errorf("wasapi: endpoint id: %w", wideErr)
		}

		// IMMDeviceEnumerator::GetDevice (slot 5)
		ret = enumerator.call(5, uintptr(unsafe.Pointer(wide)), uintptr(unsafe.Pointer(&device.p)))
	}

	if int32(ret) < 0 || device.p == nil {
		return com{}, fmt.Errorf("wasapi: endpoint %q: %#x", id, ret)
	}

	return device, nil
}

// submit copies frames into the endpoint buffer as space allows.
func submit(client, render com, bufferFrames, channels uint32, samples []float32) error {
	remaining := uint32(len(samples)) / channels

	for remaining > 0 {
		var padding uint32
		if ret := client.call(6, uintptr(unsafe.Pointer(&padding))); int32(ret) < 0 {
			return fmt.Errorf("wasapi: GetCurrentPadding: %#x", ret)
		}

		// Guard the subtraction: a bad padding must not underflow into a
		// frame count that overruns the endpoint buffer.
		if padding >= bufferFrames {
			callProc(procSleep, 5)

			continue
		}

		space := bufferFrames - padding

		frames := min(space, remaining)

		var data *float32
		// IAudioRenderClient::GetBuffer (slot 3)
		if ret := render.call(3, uintptr(frames), uintptr(unsafe.Pointer(&data))); int32(ret) < 0 {
			return fmt.Errorf("wasapi: GetBuffer: %#x", ret)
		}

		dst := unsafe.Slice(data, frames*channels)
		copy(dst, samples[:frames*channels])
		samples = samples[frames*channels:]
		remaining -= frames

		// IAudioRenderClient::ReleaseBuffer (slot 4)
		if ret := render.call(4, uintptr(frames), 0); int32(ret) < 0 {
			return fmt.Errorf("wasapi: ReleaseBuffer: %#x", ret)
		}
	}

	return nil
}

var procSleep = syscall.NewLazyDLL("kernel32.dll").NewProc("Sleep")

// callProc invokes a release-only procedure: these APIs report nothing
// actionable, so the syscall's always-set errno is consumed here once.
func callProc(p *syscall.LazyProc, args ...uintptr) {
	_, _, errno := syscall.SyscallN(p.Addr(), args...)
	_ = errno
}

// Write implements io.Writer over the engine's reference PCM.
func (w *writer) Write(p []byte) (int, error) {
	frames, err := convert(p, w.rate, w.chans)
	if err != nil {
		return 0, err
	}

	select {
	case w.frames <- frames:
		return len(p), nil
	case err := <-w.errs:
		return 0, err
	case <-w.done:
		return 0, io.ErrClosedPipe
	}
}

// Close stops the COM thread and waits for it to unwind.
func (w *writer) Close() error {
	close(w.frames)
	<-w.done

	select {
	case err := <-w.errs:
		return err
	default:
		return nil
	}
}
