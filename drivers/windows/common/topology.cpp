// Prukka virtual audio — topology miniport: the endpoint-facing side of
// the loopback. audiodg reads these pins to build the two endpoints; the
// identity names them in the INF, this filter shapes them (a speaker
// node on the render path, a microphone node on the capture path).

#include "common.h"

static const KSDATARANGE TopoBridge = {
	sizeof(KSDATARANGE),
	0,
	0,
	0,
	STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
	STATICGUIDOF(KSDATAFORMAT_SUBTYPE_ANALOG),
	STATICGUIDOF(KSDATAFORMAT_SPECIFIER_NONE),
};

static const PKSDATARANGE TopoRanges[] = {
	const_cast<PKSDATARANGE>(&TopoBridge),
};

static const PCPIN_DESCRIPTOR TopoPins[] = {
	// kTopoPinRenderIn ← wave render bridge.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(TopoRanges),
			const_cast<PKSDATARANGE*>(TopoRanges),
			KSPIN_DATAFLOW_IN,
			KSPIN_COMMUNICATION_NONE,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
	// kTopoPinSpeaker: the render endpoint apps see.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(TopoRanges),
			const_cast<PKSDATARANGE*>(TopoRanges),
			KSPIN_DATAFLOW_OUT,
			KSPIN_COMMUNICATION_NONE,
			&KSNODETYPE_SPEAKER,
			nullptr, 0,
		},
	},
	// kTopoPinMicrophone: the capture endpoint apps see.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(TopoRanges),
			const_cast<PKSDATARANGE*>(TopoRanges),
			KSPIN_DATAFLOW_IN,
			KSPIN_COMMUNICATION_NONE,
			&KSNODETYPE_MICROPHONE,
			nullptr, 0,
		},
	},
	// kTopoPinCaptureOut → wave capture bridge.
	{
		0, 0, 0,
		nullptr,
		{
			0, nullptr, 0, nullptr,
			SIZEOF_ARRAY(TopoRanges),
			const_cast<PKSDATARANGE*>(TopoRanges),
			KSPIN_DATAFLOW_OUT,
			KSPIN_COMMUNICATION_NONE,
			&KSCATEGORY_AUDIO,
			nullptr, 0,
		},
	},
};

static const PCCONNECTION_DESCRIPTOR TopoConnections[] = {
	{ PCFILTER_NODE, kTopoPinRenderIn, PCFILTER_NODE, kTopoPinSpeaker },
	{ PCFILTER_NODE, kTopoPinMicrophone, PCFILTER_NODE, kTopoPinCaptureOut },
};

static const PCFILTER_DESCRIPTOR TopologyFilter = {
	0,
	nullptr,
	sizeof(PCPIN_DESCRIPTOR),
	SIZEOF_ARRAY(TopoPins),
	const_cast<PCPIN_DESCRIPTOR*>(TopoPins),
	sizeof(PCNODE_DESCRIPTOR),
	0,
	nullptr,
	SIZEOF_ARRAY(TopoConnections),
	const_cast<PCCONNECTION_DESCRIPTOR*>(TopoConnections),
	0,
	nullptr,
};

class CMiniportTopology : public IMiniportTopology, public CUnknown {
  public:
	DECLARE_STD_UNKNOWN();

	CMiniportTopology(PUNKNOWN outer) : CUnknown(outer) {}
	~CMiniportTopology() override = default;

	STDMETHODIMP_(NTSTATUS)
	GetDescription(PPCFILTER_DESCRIPTOR* description) override {
		*description =
			const_cast<PPCFILTER_DESCRIPTOR>(&TopologyFilter);

		return STATUS_SUCCESS;
	}

	STDMETHODIMP_(NTSTATUS)
	DataRangeIntersection(ULONG, PKSDATARANGE, PKSDATARANGE, ULONG, PVOID,
			      PULONG) override {
		return STATUS_NOT_IMPLEMENTED;
	}

	STDMETHODIMP_(NTSTATUS)
	Init(PUNKNOWN, PRESOURCELIST, PPORTTOPOLOGY) override {
		return STATUS_SUCCESS;
	}
};

STDMETHODIMP CMiniportTopology::NonDelegatingQueryInterface(REFIID iid,
							    PVOID* out)
{
	if (IsEqualGUIDAligned(iid, IID_IUnknown) ||
	    IsEqualGUIDAligned(iid, IID_IMiniport) ||
	    IsEqualGUIDAligned(iid, IID_IMiniportTopology)) {
		*out = static_cast<PMINIPORTTOPOLOGY>(this);
		AddRef();

		return STATUS_SUCCESS;
	}

	*out = nullptr;

	return STATUS_INVALID_PARAMETER;
}

NTSTATUS CreateMiniportTopology(PUNKNOWN* unknown)
{
	auto* miniport =
		new (POOL_FLAG_NON_PAGED, 'kurP') CMiniportTopology(nullptr);

	if (miniport == nullptr) {
		return STATUS_INSUFFICIENT_RESOURCES;
	}

	miniport->AddRef();
	*unknown = static_cast<PUNKNOWN>(
		static_cast<PMINIPORTTOPOLOGY>(miniport));

	return STATUS_SUCCESS;
}
