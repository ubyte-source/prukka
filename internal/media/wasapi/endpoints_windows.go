//go:build windows

// COM interop: vtable dispatch crosses unsafe.Pointer by construction; no
// arithmetic ever touches a Go-managed pointer.

//nolint:gosec // G103/G115: COM interop pointer arithmetic; audited, maintainer-approved.
package wasapi

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Endpoint is one active render device (a speaker target).
type Endpoint struct {
	// ID is the full endpoint ID string device://audio/<id> accepts.
	ID string
	// Label is the endpoint's friendly name.
	Label string
}

var (
	procPropVariantClr = ole32.NewProc("PropVariantClear")
	pkeyDeviceFriendly = propertyKey{
		fmtid: guid{0xA45C254E, 0xDF1C, 0x4EFD, [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}},
		pid:   14,
	}
)

// propertyKey mirrors PROPERTYKEY.
type propertyKey struct {
	fmtid guid
	pid   uint32
}

// propVariant is an opaque 24-byte x64 PROPVARIANT; the accessors read
// its fixed ABI offsets (vt at 0, the pointer payload at 8).
type propVariant [24]byte

// vt returns the variant's type tag.
func (pv *propVariant) vt() uint16 { return *(*uint16)(unsafe.Pointer(&pv[0])) }

// ptr returns the pointer payload (VT_LPWSTR); foreign, never Go-managed.
func (pv *propVariant) ptr() unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&pv[8])) }

// vtLpwstr is the PROPVARIANT type tag for a wide string.
const vtLpwstr = 31

// deviceStateActive filters EnumAudioEndpoints to plugged-in endpoints.
const deviceStateActive = 1

// Endpoints lists the active render endpoints with their friendly names.
func Endpoints() ([]Endpoint, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ret, _, errno := procCoInitializeEx.Call(0, coinitMultithreaded)
	_ = errno

	if int32(ret) >= 0 {
		defer callProc(procCoUninitialize)
	}

	var enumerator com

	ret, _, errno = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator.p)))
	_ = errno

	if int32(ret) < 0 {
		return nil, fmt.Errorf("wasapi: CoCreateInstance: %#x", ret)
	}
	defer enumerator.release()

	// IMMDeviceEnumerator::EnumAudioEndpoints (slot 3)
	var collection com
	if ret = enumerator.call(3, eRender, deviceStateActive,
		uintptr(unsafe.Pointer(&collection.p))); int32(ret) < 0 {
		return nil, fmt.Errorf("wasapi: EnumAudioEndpoints: %#x", ret)
	}
	defer collection.release()

	// IMMDeviceCollection::GetCount (slot 3)
	var count uint32
	if ret = collection.call(3, uintptr(unsafe.Pointer(&count))); int32(ret) < 0 {
		return nil, fmt.Errorf("wasapi: GetCount: %#x", ret)
	}

	out := make([]Endpoint, 0, count)

	for i := range count {
		endpoint, err := describeEndpoint(collection, uintptr(i))
		if err != nil {
			return nil, err
		}

		out = append(out, endpoint)
	}

	return out, nil
}

// describeEndpoint reads one collection item's ID and friendly name.
func describeEndpoint(collection com, index uintptr) (Endpoint, error) {
	// IMMDeviceCollection::Item (slot 4)
	var device com
	if ret := collection.call(4, index, uintptr(unsafe.Pointer(&device.p))); int32(ret) < 0 {
		return Endpoint{}, fmt.Errorf("wasapi: Item(%d): %#x", index, ret)
	}
	defer device.release()

	// IMMDevice::GetId (slot 5) — a CoTaskMem wide string.
	var idPtr *uint16
	if ret := device.call(5, uintptr(unsafe.Pointer(&idPtr))); int32(ret) < 0 || idPtr == nil {
		return Endpoint{}, fmt.Errorf("wasapi: GetId(%d): %#x", index, ret)
	}

	id := utf16PtrToString(idPtr)

	callProc(procCoTaskMemFree, uintptr(unsafe.Pointer(idPtr)))

	// IMMDevice::OpenPropertyStore (slot 4), STGM_READ = 0.
	var store com
	if ret := device.call(4, 0, uintptr(unsafe.Pointer(&store.p))); int32(ret) < 0 {
		return Endpoint{}, fmt.Errorf("wasapi: OpenPropertyStore(%d): %#x", index, ret)
	}
	defer store.release()

	// IPropertyStore::GetValue (slot 5) for the friendly name.
	var pv propVariant
	if ret := store.call(5, uintptr(unsafe.Pointer(&pkeyDeviceFriendly)),
		uintptr(unsafe.Pointer(&pv))); int32(ret) < 0 {
		return Endpoint{}, fmt.Errorf("wasapi: GetValue(%d): %#x", index, ret)
	}

	label := ""
	if pv.vt() == vtLpwstr && pv.ptr() != nil {
		label = utf16PtrToString((*uint16)(pv.ptr()))
	}

	callProc(procPropVariantClr, uintptr(unsafe.Pointer(&pv)))

	return Endpoint{ID: id, Label: label}, nil
}

// utf16PtrToString reads a NUL-terminated wide string.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}

	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		n++
	}

	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
