// Prukka Webcam — Media Foundation virtual-camera media source.
//
// Windows 11's frame server activates this DLL through the registered
// activate CLSID (MFCreateVirtualCamera in PrukkaWebcamCtl); the source
// exposes one YUY2 1280x720@30 stream whose pixels come from the shared
// frame the engine's feeder keeps current (frame.h). Every camera app
// then sees "Prukka Webcam". Fixed format, one stream — one known-good
// shape, exactly like the macOS and Linux Prukka cameras.

#include <initguid.h>

#include <mfapi.h>
#include <mferror.h>
#include <mfidl.h>
#include <mfobjects.h>
#include <windows.h>
#include <wrl/implements.h>
#include <wrl/module.h>

#include "frame.h"
#include "guids.h"

using Microsoft::WRL::ClassicCom;
using Microsoft::WRL::ComPtr;
using Microsoft::WRL::RuntimeClass;
using Microsoft::WRL::RuntimeClassFlags;

namespace prukka {

// RETURN_IF_FAILED keeps the HRESULT plumbing visible but terse.
#define RETURN_IF_FAILED(expr)                                                                     \
    do {                                                                                           \
        HRESULT hr_ = (expr);                                                                      \
        if (FAILED(hr_)) {                                                                         \
            return hr_;                                                                            \
        }                                                                                          \
    } while (0)

// BuildMediaType describes the one fixed format the camera speaks.
static HRESULT BuildMediaType(IMFMediaType** out) {
    ComPtr<IMFMediaType> type;
    RETURN_IF_FAILED(MFCreateMediaType(&type));
    RETURN_IF_FAILED(type->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video));
    RETURN_IF_FAILED(type->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_YUY2));
    RETURN_IF_FAILED(type->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive));
    RETURN_IF_FAILED(type->SetUINT32(MF_MT_ALL_SAMPLES_INDEPENDENT, TRUE));
    RETURN_IF_FAILED(MFSetAttributeSize(type.Get(), MF_MT_FRAME_SIZE, kWidth, kHeight));
    RETURN_IF_FAILED(MFSetAttributeRatio(type.Get(), MF_MT_FRAME_RATE, kFrameRate, 1));
    RETURN_IF_FAILED(MFSetAttributeRatio(type.Get(), MF_MT_PIXEL_ASPECT_RATIO, 1, 1));
    RETURN_IF_FAILED(
        type->SetUINT32(MF_MT_DEFAULT_STRIDE, static_cast<UINT32>(kWidth * 2)));
    RETURN_IF_FAILED(type->SetUINT32(MF_MT_SAMPLE_SIZE, kFrameBytes));

    *out = type.Detach();

    return S_OK;
}

class Source;

// Stream is the camera's single video pin: it answers RequestSample with
// a copy of the shared frame, timestamped on the QPC clock.
class Stream final : public RuntimeClass<RuntimeClassFlags<ClassicCom>, IMFMediaStream2,
                                         IMFMediaEventGenerator> {
  public:
    HRESULT RuntimeClassInitialize(Source* parent, IMFStreamDescriptor* descriptor) {
        parent_ = parent;
        descriptor_ = descriptor;
        RETURN_IF_FAILED(MFCreateEventQueue(&events_));
        RETURN_IF_FAILED(MFCreateAttributes(&attributes_, 3));
        RETURN_IF_FAILED(
            attributes_->SetGUID(MF_DEVICESTREAM_STREAM_CATEGORY, PINNAME_VIDEO_CAPTURE));
        RETURN_IF_FAILED(attributes_->SetUINT32(MF_DEVICESTREAM_STREAM_ID, 0));
        RETURN_IF_FAILED(attributes_->SetUINT32(MF_DEVICESTREAM_FRAMESERVER_SHARED, 1));
        RETURN_IF_FAILED(attributes_->SetUINT32(MF_DEVICESTREAM_ATTRIBUTE_FRAMESOURCE_TYPES,
                                                MFFrameSourceTypes_Color));
        shared_ = OpenShared(&mapping_);

        return shared_ != nullptr ? S_OK : E_OUTOFMEMORY;
    }

    ~Stream() override {
        if (shared_ != nullptr) {
            UnmapViewOfFile(shared_);
        }

        if (mapping_ != nullptr) {
            CloseHandle(mapping_);
        }
    }

    // IMFMediaEventGenerator — delegated to the MF event queue.
    IFACEMETHODIMP GetEvent(DWORD flags, IMFMediaEvent** event) override {
        return events_->GetEvent(flags, event);
    }
    IFACEMETHODIMP BeginGetEvent(IMFAsyncCallback* callback, IUnknown* state) override {
        return events_->BeginGetEvent(callback, state);
    }
    IFACEMETHODIMP EndGetEvent(IMFAsyncResult* result, IMFMediaEvent** event) override {
        return events_->EndGetEvent(result, event);
    }
    IFACEMETHODIMP QueueEvent(MediaEventType type, REFGUID ext, HRESULT status,
                              const PROPVARIANT* value) override {
        return events_->QueueEventParamVar(type, ext, status, value);
    }

    // IMFMediaStream
    IFACEMETHODIMP GetMediaSource(IMFMediaSource** source) override;

    IFACEMETHODIMP GetStreamDescriptor(IMFStreamDescriptor** descriptor) override {
        return descriptor_.CopyTo(descriptor);
    }

    IFACEMETHODIMP RequestSample(IUnknown* token) override {
        ComPtr<IMFSample> sample;
        RETURN_IF_FAILED(MFCreateSample(&sample));

        ComPtr<IMFMediaBuffer> buffer;
        RETURN_IF_FAILED(MFCreateMemoryBuffer(kFrameBytes, &buffer));

        BYTE* data = nullptr;
        RETURN_IF_FAILED(buffer->Lock(&data, nullptr, nullptr));
        memcpy(data, const_cast<const uint8_t*>(shared_->pixels), kFrameBytes);
        RETURN_IF_FAILED(buffer->Unlock());
        RETURN_IF_FAILED(buffer->SetCurrentLength(kFrameBytes));
        RETURN_IF_FAILED(sample->AddBuffer(buffer.Get()));

        RETURN_IF_FAILED(sample->SetSampleTime(MFGetSystemTime()));
        RETURN_IF_FAILED(sample->SetSampleDuration(10'000'000 / kFrameRate));

        if (token != nullptr) {
            RETURN_IF_FAILED(sample->SetUnknown(MFSampleExtension_Token, token));
        }

        return events_->QueueEventParamUnk(MEMediaSample, GUID_NULL, S_OK, sample.Get());
    }

    // IMFMediaStream2
    IFACEMETHODIMP SetStreamState(MF_STREAM_STATE state) override {
        state_ = state;

        return S_OK;
    }

    IFACEMETHODIMP GetStreamState(MF_STREAM_STATE* state) override {
        *state = state_;

        return S_OK;
    }

    HRESULT Start() {
        PROPVARIANT empty;
        PropVariantInit(&empty);
        state_ = MF_STREAM_STATE_RUNNING;

        return events_->QueueEventParamVar(MEStreamStarted, GUID_NULL, S_OK, &empty);
    }

    HRESULT Stop() {
        PROPVARIANT empty;
        PropVariantInit(&empty);
        state_ = MF_STREAM_STATE_STOPPED;

        return events_->QueueEventParamVar(MEStreamStopped, GUID_NULL, S_OK, &empty);
    }

    HRESULT Shutdown() {
        if (events_ != nullptr) {
            events_->Shutdown();
        }

        parent_ = nullptr;

        return S_OK;
    }

    IMFAttributes* Attributes() { return attributes_.Get(); }

  private:
    Source* parent_ = nullptr;
    ComPtr<IMFStreamDescriptor> descriptor_;
    ComPtr<IMFMediaEventQueue> events_;
    ComPtr<IMFAttributes> attributes_;
    MF_STREAM_STATE state_ = MF_STREAM_STATE_STOPPED;
    HANDLE mapping_ = nullptr;
    SharedFrame* shared_ = nullptr;
};

// Source is the camera: one stream, fixed descriptor, frame-server
// attributes. IMFMediaSourceEx is what the virtual-camera pipeline
// requires beyond the classic source contract.
class Source final : public RuntimeClass<RuntimeClassFlags<ClassicCom>, IMFMediaSourceEx,
                                         IMFMediaSource, IMFMediaEventGenerator, IMFGetService> {
  public:
    HRESULT RuntimeClassInitialize() {
        RETURN_IF_FAILED(MFCreateEventQueue(&events_));
        RETURN_IF_FAILED(MFCreateAttributes(&attributes_, 1));

        ComPtr<IMFMediaType> type;
        RETURN_IF_FAILED(BuildMediaType(&type));

        IMFMediaType* types[] = {type.Get()};
        ComPtr<IMFStreamDescriptor> stream;
        RETURN_IF_FAILED(MFCreateStreamDescriptor(0, 1, types, &stream));

        ComPtr<IMFMediaTypeHandler> handler;
        RETURN_IF_FAILED(stream->GetMediaTypeHandler(&handler));
        RETURN_IF_FAILED(handler->SetCurrentMediaType(type.Get()));

        IMFStreamDescriptor* streams[] = {stream.Get()};
        RETURN_IF_FAILED(MFCreatePresentationDescriptor(1, streams, &presentation_));
        RETURN_IF_FAILED(presentation_->SelectStream(0));

        return Microsoft::WRL::MakeAndInitialize<Stream>(&stream_, this, stream.Get());
    }

    // IMFMediaEventGenerator
    IFACEMETHODIMP GetEvent(DWORD flags, IMFMediaEvent** event) override {
        return events_->GetEvent(flags, event);
    }
    IFACEMETHODIMP BeginGetEvent(IMFAsyncCallback* callback, IUnknown* state) override {
        return events_->BeginGetEvent(callback, state);
    }
    IFACEMETHODIMP EndGetEvent(IMFAsyncResult* result, IMFMediaEvent** event) override {
        return events_->EndGetEvent(result, event);
    }
    IFACEMETHODIMP QueueEvent(MediaEventType type, REFGUID ext, HRESULT status,
                              const PROPVARIANT* value) override {
        return events_->QueueEventParamVar(type, ext, status, value);
    }

    // IMFMediaSource
    IFACEMETHODIMP GetCharacteristics(DWORD* characteristics) override {
        *characteristics = MFMEDIASOURCE_IS_LIVE;

        return S_OK;
    }

    IFACEMETHODIMP CreatePresentationDescriptor(IMFPresentationDescriptor** out) override {
        return presentation_->Clone(out);
    }

    IFACEMETHODIMP Start(IMFPresentationDescriptor* descriptor, const GUID* timeFormat,
                         const PROPVARIANT* startPosition) override {
        ComPtr<IUnknown> streamUnknown;
        RETURN_IF_FAILED(stream_.As(&streamUnknown));

        // The frame server expects the stream announced before the
        // source reports itself started.
        RETURN_IF_FAILED(events_->QueueEventParamUnk(MENewStream, GUID_NULL, S_OK,
                                                     streamUnknown.Get()));
        RETURN_IF_FAILED(stream_->Start());

        PROPVARIANT position;
        PropVariantInit(&position);
        position.vt = VT_I8;
        position.hVal.QuadPart = MFGetSystemTime();

        return events_->QueueEventParamVar(MESourceStarted, GUID_NULL, S_OK, &position);
    }

    IFACEMETHODIMP Stop() override {
        RETURN_IF_FAILED(stream_->Stop());

        PROPVARIANT empty;
        PropVariantInit(&empty);

        return events_->QueueEventParamVar(MESourceStopped, GUID_NULL, S_OK, &empty);
    }

    IFACEMETHODIMP Pause() override { return MF_E_INVALID_STATE_TRANSITION; }

    IFACEMETHODIMP Shutdown() override {
        if (stream_ != nullptr) {
            stream_->Shutdown();
        }

        if (events_ != nullptr) {
            events_->Shutdown();
        }

        return S_OK;
    }

    // IMFMediaSourceEx
    IFACEMETHODIMP GetSourceAttributes(IMFAttributes** out) override {
        return attributes_.CopyTo(out);
    }

    IFACEMETHODIMP GetStreamAttributes(DWORD /*streamId*/, IMFAttributes** out) override {
        ComPtr<IMFAttributes> attrs(stream_->Attributes());

        return attrs.CopyTo(out);
    }

    IFACEMETHODIMP SetD3DManager(IUnknown* /*manager*/) override { return S_OK; }

    // IMFGetService — mandatory for a Frame Server custom media source. This
    // source vends no extra service objects, so every request is answered
    // MF_E_UNSUPPORTED_SERVICE; the interface itself must be present for the
    // frame server to accept the source.
    IFACEMETHODIMP GetService(REFGUID /*service*/, REFIID /*riid*/, LPVOID* out) override {
        if (out == nullptr) {
            return E_POINTER;
        }

        *out = nullptr;

        return MF_E_UNSUPPORTED_SERVICE;
    }

  private:
    ComPtr<IMFMediaEventQueue> events_;
    ComPtr<IMFAttributes> attributes_;
    ComPtr<IMFPresentationDescriptor> presentation_;
    ComPtr<Stream> stream_;
};

IFACEMETHODIMP Stream::GetMediaSource(IMFMediaSource** source) {
    if (parent_ == nullptr) {
        return MF_E_SHUTDOWN;
    }

    return parent_->QueryInterface(IID_PPV_ARGS(source));
}

// Activate is what the frame server CoCreates by CLSID: it hands out the
// source and delegates attribute storage to a plain MF attribute store.
class Activate final
    : public RuntimeClass<RuntimeClassFlags<ClassicCom>, IMFActivate, IMFAttributes> {
  public:
    HRESULT RuntimeClassInitialize() { return MFCreateAttributes(&attributes_, 1); }

    // IMFActivate
    IFACEMETHODIMP ActivateObject(REFIID riid, void** out) override {
        if (source_ == nullptr) {
            RETURN_IF_FAILED(Microsoft::WRL::MakeAndInitialize<Source>(&source_));
        }

        return source_.CopyTo(riid, out);
    }

    IFACEMETHODIMP ShutdownObject() override {
        if (source_ != nullptr) {
            source_->Shutdown();
            source_.Reset();
        }

        return S_OK;
    }

    IFACEMETHODIMP DetachObject() override {
        source_.Reset();

        return S_OK;
    }

    // IMFAttributes — the whole surface delegates to the store.
    IFACEMETHODIMP GetItem(REFGUID key, PROPVARIANT* value) override {
        return attributes_->GetItem(key, value);
    }
    IFACEMETHODIMP GetItemType(REFGUID key, MF_ATTRIBUTE_TYPE* type) override {
        return attributes_->GetItemType(key, type);
    }
    IFACEMETHODIMP CompareItem(REFGUID key, REFPROPVARIANT value, BOOL* result) override {
        return attributes_->CompareItem(key, value, result);
    }
    IFACEMETHODIMP Compare(IMFAttributes* theirs, MF_ATTRIBUTES_MATCH_TYPE type,
                           BOOL* result) override {
        return attributes_->Compare(theirs, type, result);
    }
    IFACEMETHODIMP GetUINT32(REFGUID key, UINT32* value) override {
        return attributes_->GetUINT32(key, value);
    }
    IFACEMETHODIMP GetUINT64(REFGUID key, UINT64* value) override {
        return attributes_->GetUINT64(key, value);
    }
    IFACEMETHODIMP GetDouble(REFGUID key, double* value) override {
        return attributes_->GetDouble(key, value);
    }
    IFACEMETHODIMP GetGUID(REFGUID key, GUID* value) override {
        return attributes_->GetGUID(key, value);
    }
    IFACEMETHODIMP GetStringLength(REFGUID key, UINT32* length) override {
        return attributes_->GetStringLength(key, length);
    }
    IFACEMETHODIMP GetString(REFGUID key, LPWSTR value, UINT32 size,
                             UINT32* length) override {
        return attributes_->GetString(key, value, size, length);
    }
    IFACEMETHODIMP GetAllocatedString(REFGUID key, LPWSTR* value, UINT32* length) override {
        return attributes_->GetAllocatedString(key, value, length);
    }
    IFACEMETHODIMP GetBlobSize(REFGUID key, UINT32* size) override {
        return attributes_->GetBlobSize(key, size);
    }
    IFACEMETHODIMP GetBlob(REFGUID key, UINT8* buffer, UINT32 size, UINT32* used) override {
        return attributes_->GetBlob(key, buffer, size, used);
    }
    IFACEMETHODIMP GetAllocatedBlob(REFGUID key, UINT8** buffer, UINT32* size) override {
        return attributes_->GetAllocatedBlob(key, buffer, size);
    }
    IFACEMETHODIMP GetUnknown(REFGUID key, REFIID riid, LPVOID* out) override {
        return attributes_->GetUnknown(key, riid, out);
    }
    IFACEMETHODIMP SetItem(REFGUID key, REFPROPVARIANT value) override {
        return attributes_->SetItem(key, value);
    }
    IFACEMETHODIMP DeleteItem(REFGUID key) override { return attributes_->DeleteItem(key); }
    IFACEMETHODIMP DeleteAllItems() override { return attributes_->DeleteAllItems(); }
    IFACEMETHODIMP SetUINT32(REFGUID key, UINT32 value) override {
        return attributes_->SetUINT32(key, value);
    }
    IFACEMETHODIMP SetUINT64(REFGUID key, UINT64 value) override {
        return attributes_->SetUINT64(key, value);
    }
    IFACEMETHODIMP SetDouble(REFGUID key, double value) override {
        return attributes_->SetDouble(key, value);
    }
    IFACEMETHODIMP SetGUID(REFGUID key, REFGUID value) override {
        return attributes_->SetGUID(key, value);
    }
    IFACEMETHODIMP SetString(REFGUID key, LPCWSTR value) override {
        return attributes_->SetString(key, value);
    }
    IFACEMETHODIMP SetBlob(REFGUID key, const UINT8* buffer, UINT32 size) override {
        return attributes_->SetBlob(key, buffer, size);
    }
    IFACEMETHODIMP SetUnknown(REFGUID key, IUnknown* value) override {
        return attributes_->SetUnknown(key, value);
    }
    IFACEMETHODIMP LockStore() override { return attributes_->LockStore(); }
    IFACEMETHODIMP UnlockStore() override { return attributes_->UnlockStore(); }
    IFACEMETHODIMP GetCount(UINT32* count) override { return attributes_->GetCount(count); }
    IFACEMETHODIMP GetItemByIndex(UINT32 index, GUID* key, PROPVARIANT* value) override {
        return attributes_->GetItemByIndex(index, key, value);
    }
    IFACEMETHODIMP CopyAllItems(IMFAttributes* dest) override {
        return attributes_->CopyAllItems(dest);
    }

  private:
    ComPtr<IMFAttributes> attributes_;
    ComPtr<Source> source_;
};

CoCreatableClass(Activate);

} // namespace prukka
