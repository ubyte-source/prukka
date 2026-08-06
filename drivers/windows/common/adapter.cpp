// Prukka virtual audio — adapter: DriverEntry, device start, subdevice
// registration and the physical connections binding wave to topology.

// PUT_GUIDS_HERE makes portcls.h pull initguid.h in this one translation
// unit, so the port CLSIDs and miniport IIDs get their definitions here.
#define PUT_GUIDS_HERE

#include "common.h"

PrukkaRing g_Ring;

// The kernel-mode allocation operators declared in common.h: every
// miniport object lives in nonpaged pool under the driver's tag, and
// delete may run at dispatch level (CUnknown::Release from a DPC), so
// the definitions stay in the default (nonpaged) code segment. The
// basic operator delete(void*) comes from stdunk.lib.
#pragma code_seg()

PVOID operator new(size_t size, POOL_FLAGS flags, ULONG tag)
{
	return ExAllocatePool2(flags, size, tag);
}

void __cdecl operator delete(PVOID memory, size_t size)
{
	UNREFERENCED_PARAMETER(size);

	if (memory != nullptr) {
		ExFreePool(memory);
	}
}

extern "C" DRIVER_INITIALIZE DriverEntry;

// The PnP path below runs only at PASSIVE_LEVEL, so it lives in the
// pageable segment — matching the PAGED_CODE() assertions it carries.
#pragma code_seg("PAGE")

static NTSTATUS InstallSubdevice(PDEVICE_OBJECT device, PIRP irp, PWSTR name,
				 REFGUID portClass,
				 NTSTATUS (*factory)(PUNKNOWN*),
				 PRESOURCELIST resources, PUNKNOWN* outPort)
{
	PAGED_CODE();

	PPORT port = nullptr;
	NTSTATUS status = PcNewPort(&port, portClass);

	if (!NT_SUCCESS(status)) {
		return status;
	}

	PUNKNOWN miniport = nullptr;
	status = factory(&miniport);

	if (NT_SUCCESS(status)) {
		status = port->Init(device, irp, miniport, nullptr, resources);
	}

	if (NT_SUCCESS(status)) {
		status = PcRegisterSubdevice(device, name, port);
	}

	if (miniport != nullptr) {
		miniport->Release();
	}

	if (NT_SUCCESS(status) && outPort != nullptr) {
		*outPort = port;

		return status;
	}

	port->Release();

	return status;
}

static NTSTATUS StartDevice(PDEVICE_OBJECT device, PIRP irp,
			    PRESOURCELIST resources)
{
	PAGED_CODE();

	KeInitializeSpinLock(&g_Ring.lock);
	g_Ring.written = 0;
	g_Ring.read = 0;

	PUNKNOWN wave = nullptr;
	NTSTATUS status = InstallSubdevice(device, irp, const_cast<PWSTR>(L"Wave"),
					   CLSID_PortWaveCyclic,
					   CreateMiniportWaveCyclic, resources,
					   &wave);

	if (!NT_SUCCESS(status)) {
		return status;
	}

	PUNKNOWN topology = nullptr;
	status = InstallSubdevice(device, irp, const_cast<PWSTR>(L"Topology"),
				  CLSID_PortTopology, CreateMiniportTopology,
				  resources, &topology);

	if (NT_SUCCESS(status)) {
		// Render: wave bridge out → topology in; capture: topology
		// out → wave bridge in. These wires are what makes audiodg
		// build the two endpoints.
		status = PcRegisterPhysicalConnection(device, wave,
						      kWavePinRenderBridge,
						      topology,
						      kTopoPinRenderIn);

		if (NT_SUCCESS(status)) {
			status = PcRegisterPhysicalConnection(
				device, topology, kTopoPinCaptureOut, wave,
				kWavePinCaptureBridge);
		}
	}

	if (topology != nullptr) {
		topology->Release();
	}

	wave->Release();

	return status;
}

static NTSTATUS AddDevice(PDRIVER_OBJECT driver, PDEVICE_OBJECT physical)
{
	PAGED_CODE();

	return PcAddAdapterDevice(driver, physical, StartDevice, 2, 0);
}

#pragma code_seg()

extern "C" NTSTATUS DriverEntry(PDRIVER_OBJECT driver,
				PUNICODE_STRING registryPath)
{
	NTSTATUS status = PcInitializeAdapterDriver(driver, registryPath,
						    AddDevice);

	return status;
}
