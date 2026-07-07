// Prukka virtual audio — WaveCyclic miniport: one render and one capture
// stream over the shared ring. The stream is its own fake DMA channel
// (the MSVAD pattern): the port's copy engine calls CopyTo on render —
// we append to the ring — and CopyFrom on capture — we drain it, clearing
// what was read so stale audio never loops (the contract the macOS
// harness pins). Positions advance on the QPC clock at the fixed rate.

#include "common.h"
#include "position.h"
#include "ring.h"

static const KSDATARANGE_AUDIO WaveRange = {
	{
		sizeof(KSDATARANGE_AUDIO),
		0,
		0,
		0,
		STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
		STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM),
		STATICGUIDOF(KSDATAFORMAT_SPECIFIER_WAVEFORMATEX),
	},
	PRUKKA_CHANNELS,
	PRUKKA_BITS,
	PRUKKA_BITS,
	PRUKKA_RATE,
	PRUKKA_RATE,
};

static const PKSDATARANGE WaveRanges[] = {
	const_cast<PKSDATARANGE>(reinterpret_cast<const KSDATARANGE*>(&WaveRange)),
};

static const KSDATARANGE BridgeRange = {
	sizeof(KSDATARANGE),
	0,
	0,
	0,
	STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
	STATICGUIDOF(KSDATAFORMAT_SUBTYPE_ANALOG),
	STATICGUIDOF(KSDATAFORMAT_SPECIFIER_NONE),
};

static const PKSDATARANGE BridgeRanges[] = {
	const_cast<PKSDATARANGE>(&BridgeRange),
};

static const PCPIN_DESCRIPTOR WavePins[] = {
	// kWavePinRender: apps/engine play here.
	{
		1, 1, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(WaveRanges),
			const_cast<PKSDATARANGE*>(WaveRanges),
			KSPIN_DATAFLOW_IN,
			KSPIN_COMMUNICATION_SINK,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
	// kWavePinRenderBridge → topology.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(BridgeRanges),
			const_cast<PKSDATARANGE*>(BridgeRanges),
			KSPIN_DATAFLOW_OUT,
			KSPIN_COMMUNICATION_NONE,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
	// kWavePinCapture: apps/engine record here.
	{
		1, 1, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(WaveRanges),
			const_cast<PKSDATARANGE*>(WaveRanges),
			KSPIN_DATAFLOW_OUT,
			KSPIN_COMMUNICATION_SINK,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
	// kWavePinCaptureBridge ← topology.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(BridgeRanges),
			const_cast<PKSDATARANGE*>(BridgeRanges),
			KSPIN_DATAFLOW_IN,
			KSPIN_COMMUNICATION_NONE,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
};

static const PCCONNECTION_DESCRIPTOR WaveConnections[] = {
	{ PCFILTER_NODE, kWavePinRender, PCFILTER_NODE, kWavePinRenderBridge },
	{ PCFILTER_NODE, kWavePinCaptureBridge, PCFILTER_NODE, kWavePinCapture },
};

static const PCFILTER_DESCRIPTOR WaveFilter = {
	0,
	nullptr,
	sizeof(PCPIN_DESCRIPTOR),
	SIZEOF_ARRAY(WavePins),
	const_cast<PCPIN_DESCRIPTOR*>(WavePins),
	sizeof(PCNODE_DESCRIPTOR),
	0,
	nullptr,
	SIZEOF_ARRAY(WaveConnections),
	const_cast<PCCONNECTION_DESCRIPTOR*>(WaveConnections),
	0,
	nullptr,
};

class CMiniportWaveCyclic;

// CWaveStream is one direction of the loopback: miniport stream and fake
// DMA channel in one object, plus the timer that asks the port to run
// its copy engine.
class CWaveStream : public IMiniportWaveCyclicStream,
		    public IDmaChannel,
		    public CUnknown {
  public:
	DECLARE_STD_UNKNOWN();

	CWaveStream(PUNKNOWN outer) : CUnknown(outer) {}
	~CWaveStream() override;

	NTSTATUS Setup(CMiniportWaveCyclic* miniport, BOOLEAN capture,
		       PSERVICEGROUP* serviceGroup);

	// IMiniportWaveCyclicStream
	STDMETHODIMP_(NTSTATUS) SetFormat(PKSDATAFORMAT format) override;
	STDMETHODIMP_(ULONG) SetNotificationFreq(ULONG interval,
						 PULONG framing) override;
	_IRQL_requires_(PASSIVE_LEVEL)
	STDMETHODIMP_(NTSTATUS) SetState(KSSTATE state) override;
	STDMETHODIMP_(NTSTATUS) GetPosition(PULONG position) override;
	STDMETHODIMP_(NTSTATUS)
	NormalizePhysicalPosition(PLONGLONG position) override;
	STDMETHODIMP_(void) Silence(PVOID buffer, ULONG bytes) override;

	// IDmaChannel — a fake channel over a nonpaged buffer.
	STDMETHODIMP_(NTSTATUS)
	AllocateBuffer(ULONG size, PPHYSICAL_ADDRESS restriction) override;
	STDMETHODIMP_(void) FreeBuffer() override;
	STDMETHODIMP_(ULONG) TransferCount() override { return m_BufferSize; }
	STDMETHODIMP_(ULONG) MaximumBufferSize() override {
		return PRUKKA_RING_BYTES;
	}
	STDMETHODIMP_(ULONG) AllocatedBufferSize() override {
		return m_Allocated;
	}
	STDMETHODIMP_(ULONG) BufferSize() override { return m_BufferSize; }
	STDMETHODIMP_(void) SetBufferSize(ULONG size) override {
		m_BufferSize = size;
	}
	STDMETHODIMP_(PVOID) SystemAddress() override { return m_Buffer; }
	STDMETHODIMP_(PHYSICAL_ADDRESS) PhysicalAddress() override {
		PHYSICAL_ADDRESS none;
		none.QuadPart = 0;

		return none;
	}
	STDMETHODIMP_(PADAPTER_OBJECT) GetAdapterObject() override {
		return nullptr;
	}
	STDMETHODIMP_(void) CopyTo(PVOID dest, PVOID source,
				   ULONG bytes) override;
	STDMETHODIMP_(void) CopyFrom(PVOID dest, PVOID source,
				     ULONG bytes) override;

	static void TimerDpc(PKDPC dpc, PVOID context, PVOID arg1, PVOID arg2);

  private:
	PSERVICEGROUP m_ServiceGroup = nullptr;
	PUCHAR m_Buffer = nullptr;
	ULONG m_Allocated = 0;
	ULONG m_BufferSize = 0;
	BOOLEAN m_Capture = FALSE;
	KSSTATE m_State = KSSTATE_STOP;
	KTIMER m_Timer;
	KDPC m_Dpc;
	FAST_MUTEX m_StateMutex;
	KSPIN_LOCK m_PositionLock;
	PrukkaPositionClock m_PositionClock = {};
	LARGE_INTEGER m_QpcFrequency = {};
};

class CMiniportWaveCyclic : public IMiniportWaveCyclic, public CUnknown {
  public:
	DECLARE_STD_UNKNOWN();

	CMiniportWaveCyclic(PUNKNOWN outer) : CUnknown(outer) {}
	~CMiniportWaveCyclic() override = default;

	// IMiniport
	STDMETHODIMP_(NTSTATUS)
	GetDescription(PPCFILTER_DESCRIPTOR* description) override {
		*description = const_cast<PPCFILTER_DESCRIPTOR>(&WaveFilter);

		return STATUS_SUCCESS;
	}

	STDMETHODIMP_(NTSTATUS)
	DataRangeIntersection(ULONG pin, PKSDATARANGE clientRange,
			      PKSDATARANGE myRange, ULONG outSize,
			      PVOID resultantFormat,
			      PULONG resultantSize) override;

	// IMiniportWaveCyclic
	STDMETHODIMP_(NTSTATUS)
	Init(PUNKNOWN adapter, PRESOURCELIST resources,
	     PPORTWAVECYCLIC port) override {
		UNREFERENCED_PARAMETER(adapter);
		UNREFERENCED_PARAMETER(resources);
		m_Port = port;

		return STATUS_SUCCESS;
	}

	STDMETHODIMP_(NTSTATUS)
	NewStream(PMINIPORTWAVECYCLICSTREAM* stream, PUNKNOWN outer,
		  POOL_TYPE poolType, ULONG pin, BOOLEAN capture,
		  PKSDATAFORMAT format, PDMACHANNEL* dmaChannel,
		  PSERVICEGROUP* serviceGroup) override;

  private:
	PPORTWAVECYCLIC m_Port = nullptr;
};

// MARK: stream

CWaveStream::~CWaveStream()
{
	NT_ASSERT(m_State != KSSTATE_RUN);
	FreeBuffer();

	if (m_ServiceGroup != nullptr) {
		m_ServiceGroup->Release();
	}
}

NTSTATUS CWaveStream::Setup(CMiniportWaveCyclic*, BOOLEAN capture,
			    PSERVICEGROUP* serviceGroup)
{
	m_Capture = capture;
	KeQueryPerformanceCounter(&m_QpcFrequency);
	KeInitializeTimerEx(&m_Timer, SynchronizationTimer);
	KeInitializeDpc(&m_Dpc, TimerDpc, this);
	ExInitializeFastMutex(&m_StateMutex);
	KeInitializeSpinLock(&m_PositionLock);

	NTSTATUS status = PcNewServiceGroup(&m_ServiceGroup, nullptr);

	if (!NT_SUCCESS(status)) {
		return status;
	}

	m_ServiceGroup->AddRef();
	*serviceGroup = m_ServiceGroup;

	return STATUS_SUCCESS;
}

void CWaveStream::TimerDpc(PKDPC, PVOID context, PVOID, PVOID)
{
	auto* stream = static_cast<CWaveStream*>(context);

	if (stream->m_ServiceGroup != nullptr) {
		stream->m_ServiceGroup->RequestService();
	}
}

STDMETHODIMP_(NTSTATUS) CWaveStream::SetFormat(PKSDATAFORMAT)
{
	// The one fixed format was validated at NewStream.
	return STATUS_SUCCESS;
}

STDMETHODIMP_(ULONG) CWaveStream::SetNotificationFreq(ULONG interval,
						      PULONG framing)
{
	*framing = (PRUKKA_BYTES_PER_SEC * interval) / 1000;

	return interval;
}

STDMETHODIMP_(NTSTATUS) CWaveStream::SetState(KSSTATE state)
{
	if (state < KSSTATE_STOP || state > KSSTATE_RUN) {
		return STATUS_INVALID_PARAMETER;
	}

	ExAcquireFastMutex(&m_StateMutex);

	const BOOLEAN enteringRun =
		state == KSSTATE_RUN && m_State != KSSTATE_RUN;
	const BOOLEAN leavingRun =
		state != KSSTATE_RUN && m_State == KSSTATE_RUN;
	const LARGE_INTEGER now = KeQueryPerformanceCounter(nullptr);

	if (enteringRun) {
		// The timer owns this reference until its DPC is drained.
		AddRef();
	}

	KIRQL irql;
	KeAcquireSpinLock(&m_PositionLock, &irql);
	m_PositionClock = PrukkaPositionTransition(
		m_PositionClock, state == KSSTATE_RUN, state == KSSTATE_STOP,
		static_cast<ULONGLONG>(now.QuadPart));
	m_State = state;
	KeReleaseSpinLock(&m_PositionLock, irql);

	if (leavingRun) {
		KeCancelTimer(&m_Timer);
		KeFlushQueuedDpcs();
	}

	if (enteringRun) {
		LARGE_INTEGER due;
		due.QuadPart = -10000LL * PRUKKA_TICK_MS;
		KeSetTimerEx(&m_Timer, due, PRUKKA_TICK_MS, &m_Dpc);
	}

	ExReleaseFastMutex(&m_StateMutex);

	if (leavingRun) {
		Release();
	}

	return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS) CWaveStream::GetPosition(PULONG position)
{
	const LARGE_INTEGER now = KeQueryPerformanceCounter(nullptr);

	KIRQL irql;
	KeAcquireSpinLock(&m_PositionLock, &irql);
	const KSSTATE state = m_State;
	const PrukkaPositionClock clock = m_PositionClock;
	const ULONG bufferSize = m_BufferSize;
	KeReleaseSpinLock(&m_PositionLock, irql);

	if (state == KSSTATE_STOP || bufferSize == 0) {
		*position = 0;

		return STATUS_SUCCESS;
	}

	const ULONGLONG elapsed = PrukkaPositionElapsed(
		clock, static_cast<ULONGLONG>(now.QuadPart));
	*position = static_cast<ULONG>(PrukkaCyclicPosition(
		elapsed, static_cast<ULONGLONG>(m_QpcFrequency.QuadPart),
		PRUKKA_BYTES_PER_SEC, PRUKKA_BLOCK_ALIGN, bufferSize));

	return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS)
CWaveStream::NormalizePhysicalPosition(PLONGLONG position)
{
	// Bytes → 100 ns units at the fixed byte rate.
	*position = (*position * 10000000LL) / PRUKKA_BYTES_PER_SEC;

	return STATUS_SUCCESS;
}

STDMETHODIMP_(void) CWaveStream::Silence(PVOID buffer, ULONG bytes)
{
	RtlZeroMemory(buffer, bytes);
}

STDMETHODIMP_(NTSTATUS)
CWaveStream::AllocateBuffer(ULONG size, PPHYSICAL_ADDRESS)
{
	size = min(size, static_cast<ULONG>(PRUKKA_RING_BYTES));
	size -= size % PRUKKA_BLOCK_ALIGN;

	m_Buffer = static_cast<PUCHAR>(ExAllocatePool2(
		POOL_FLAG_NON_PAGED, size, 'kurP'));

	if (m_Buffer == nullptr) {
		return STATUS_INSUFFICIENT_RESOURCES;
	}

	m_Allocated = size;
	m_BufferSize = size;

	return STATUS_SUCCESS;
}

STDMETHODIMP_(void) CWaveStream::FreeBuffer()
{
	if (m_Buffer != nullptr) {
		ExFreePoolWithTag(m_Buffer, 'kurP');
		m_Buffer = nullptr;
		m_Allocated = 0;
		m_BufferSize = 0;
	}
}

// CopyTo runs on render: the port hands us the freshly mixed audio; it
// lands in the fake DMA buffer and is appended to the shared ring.
STDMETHODIMP_(void) CWaveStream::CopyTo(PVOID dest, PVOID source, ULONG bytes)
{
	RtlCopyMemory(dest, source, bytes);

	KIRQL irql;
	KeAcquireSpinLock(&g_Ring.lock, &irql);

	const UCHAR* src = static_cast<const UCHAR*>(source);

	for (ULONG i = 0; i < bytes; i++) {
		g_Ring.data[(g_Ring.written + i) % PRUKKA_RING_BYTES] = src[i];
	}

	g_Ring.written += bytes;
	KeReleaseSpinLock(&g_Ring.lock, irql);
}

// CopyFrom runs on capture: fill the client from the ring, clearing what
// was read; with no writer the reader gets silence, never stale audio.
STDMETHODIMP_(void) CWaveStream::CopyFrom(PVOID dest, PVOID source,
					  ULONG bytes)
{
	UNREFERENCED_PARAMETER(source);

	UCHAR* out = static_cast<UCHAR*>(dest);

	KIRQL irql;
	KeAcquireSpinLock(&g_Ring.lock, &irql);

	const PrukkaRingWindow window = PrukkaRingReadWindow(
		g_Ring.written, g_Ring.read, PRUKKA_RING_BYTES);
	g_Ring.read = window.read;
	const ULONGLONG available = window.available;

	for (ULONG i = 0; i < bytes; i++) {
		if (i < available) {
			ULONG at = static_cast<ULONG>(
				(g_Ring.read + i) % PRUKKA_RING_BYTES);
			out[i] = g_Ring.data[at];
			g_Ring.data[at] = 0;
		} else {
			out[i] = 0;
		}
	}

	g_Ring.read += min(static_cast<ULONGLONG>(bytes), available);
	KeReleaseSpinLock(&g_Ring.lock, irql);
}

STDMETHODIMP CWaveStream::NonDelegatingQueryInterface(REFIID iid, PVOID* out)
{
	if (IsEqualGUIDAligned(iid, IID_IUnknown) ||
	    IsEqualGUIDAligned(iid, IID_IMiniportWaveCyclicStream)) {
		*out = static_cast<PMINIPORTWAVECYCLICSTREAM>(this);
	} else if (IsEqualGUIDAligned(iid, IID_IDmaChannel)) {
		*out = static_cast<PDMACHANNEL>(this);
	} else {
		*out = nullptr;

		return STATUS_INVALID_PARAMETER;
	}

	AddRef();

	return STATUS_SUCCESS;
}

// MARK: miniport

STDMETHODIMP_(NTSTATUS)
CMiniportWaveCyclic::DataRangeIntersection(ULONG, PKSDATARANGE clientRange,
					   PKSDATARANGE, ULONG outSize,
					   PVOID resultantFormat,
					   PULONG resultantSize)
{
	const ULONG need = sizeof(KSDATAFORMAT_WAVEFORMATEX);

	*resultantSize = need;

	if (outSize == 0) {
		return STATUS_BUFFER_OVERFLOW;
	}

	if (outSize < need) {
		return STATUS_BUFFER_TOO_SMALL;
	}

	UNREFERENCED_PARAMETER(clientRange);

	auto* format = static_cast<KSDATAFORMAT_WAVEFORMATEX*>(resultantFormat);
	RtlZeroMemory(format, need);

	format->DataFormat.FormatSize = need;
	format->DataFormat.MajorFormat = KSDATAFORMAT_TYPE_AUDIO;
	format->DataFormat.SubFormat = KSDATAFORMAT_SUBTYPE_PCM;
	format->DataFormat.Specifier = KSDATAFORMAT_SPECIFIER_WAVEFORMATEX;
	format->DataFormat.SampleSize = PRUKKA_BLOCK_ALIGN;

	format->WaveFormatEx.wFormatTag = WAVE_FORMAT_PCM;
	format->WaveFormatEx.nChannels = PRUKKA_CHANNELS;
	format->WaveFormatEx.nSamplesPerSec = PRUKKA_RATE;
	format->WaveFormatEx.nAvgBytesPerSec = PRUKKA_BYTES_PER_SEC;
	format->WaveFormatEx.nBlockAlign = PRUKKA_BLOCK_ALIGN;
	format->WaveFormatEx.wBitsPerSample = PRUKKA_BITS;

	return STATUS_SUCCESS;
}

STDMETHODIMP_(NTSTATUS)
CMiniportWaveCyclic::NewStream(PMINIPORTWAVECYCLICSTREAM* stream,
			       PUNKNOWN outer, POOL_TYPE poolType, ULONG pin,
			       BOOLEAN capture, PKSDATAFORMAT format,
			       PDMACHANNEL* dmaChannel,
			       PSERVICEGROUP* serviceGroup)
{
	UNREFERENCED_PARAMETER(poolType);
	UNREFERENCED_PARAMETER(format);

	if (pin != kWavePinRender && pin != kWavePinCapture) {
		return STATUS_INVALID_PARAMETER;
	}

	auto* wave = new (POOL_FLAG_NON_PAGED, 'kurP') CWaveStream(outer);

	if (wave == nullptr) {
		return STATUS_INSUFFICIENT_RESOURCES;
	}

	NTSTATUS status = wave->Setup(this, capture, serviceGroup);

	if (!NT_SUCCESS(status)) {
		delete wave;

		return status;
	}

	wave->AddRef();
	*stream = static_cast<PMINIPORTWAVECYCLICSTREAM>(wave);
	wave->AddRef();
	*dmaChannel = static_cast<PDMACHANNEL>(wave);

	return STATUS_SUCCESS;
}

STDMETHODIMP CMiniportWaveCyclic::NonDelegatingQueryInterface(REFIID iid,
							      PVOID* out)
{
	if (IsEqualGUIDAligned(iid, IID_IUnknown) ||
	    IsEqualGUIDAligned(iid, IID_IMiniport) ||
	    IsEqualGUIDAligned(iid, IID_IMiniportWaveCyclic)) {
		*out = static_cast<PMINIPORTWAVECYCLIC>(this);
		AddRef();

		return STATUS_SUCCESS;
	}

	*out = nullptr;

	return STATUS_INVALID_PARAMETER;
}

NTSTATUS CreateMiniportWaveCyclic(PUNKNOWN* unknown)
{
	auto* miniport =
		new (POOL_FLAG_NON_PAGED, 'kurP') CMiniportWaveCyclic(nullptr);

	if (miniport == nullptr) {
		return STATUS_INSUFFICIENT_RESOURCES;
	}

	miniport->AddRef();
	*unknown = static_cast<PUNKNOWN>(
		static_cast<PMINIPORTWAVECYCLIC>(miniport));

	return STATUS_SUCCESS;
}
