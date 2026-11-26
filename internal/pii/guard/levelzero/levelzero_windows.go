//go:build windows && (amd64 || arm64)

// Package levelzero runs a compiled OpenVINO graph on an Intel NPU
// through Windows' system Level Zero loader (ze_loader.dll) and the
// driver's ZE_extension_graph, with no cgo and nothing extra to
// download.
package levelzero

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// blobMagic prefixes every cached native NPU binary alongside a SHA-256
// digest of its payload: ZE_GRAPH_FORMAT_NATIVE in Intel's Level Zero UMD
// parses the ELF header unchecked and crashes on truncated or foreign files.
var blobMagic = [8]byte{'M', 'G', 'L', 'Z', 'B', 'L', 'O', 'B'}

// Info describes the NPU driver and graph compiler on this computer.
type Info struct {
	Device        string
	DeviceID      uint32
	DriverVersion uint32
	CompilerMajor uint16
	CompilerMinor uint16
	MaxOpset      uint32
}

var npuDriverUUID = [16]byte{
	0x01, 0x7d, 0xe9, 0x31, 0x6b, 0x4d, 0x4f, 0xd4,
	0xaa, 0x9b, 0x5b, 0xed, 0x77, 0xfc, 0x8e, 0x89,
}

const (
	zeInitFlagVPUOnly       = 1 << 1
	zeInitDriverTypeFlagNPU = 1 << 1
	zeDeviceTypeVPU         = 5

	zeStypeDriverProps        = 0x1
	zeStypeDeviceProps        = 0x3
	zeStypeCmdQueueGroupProps = 0x6
	zeStypeContextDesc        = 0xd
	zeStypeCmdQueueDesc       = 0xe
	zeStypeCmdListDesc        = 0xf
	zeStypeHostMemAllocDesc   = 0x16
	zeStypeInitDriverTypeDesc = 0x00020021

	zeGraphStypeDeviceProps = 0x1
	zeGraphStypeDesc        = 0x2
	zeGraphStypeProps       = 0x3
	zeGraphStypeArgProps    = 0x4

	zeGraphFormatNative     = 0x1
	zeGraphFormatNGraphLite = 0x2

	zeGraphArgInput  = 0
	zeGraphArgOutput = 1
	zeGraphPrecFP32  = 0x1

	syncTimeoutNS = 30_000_000_000 // 30s
)

type zeInitDriverTypeDesc struct {
	stype uint32
	pNext uintptr
	flags uint32
}

type zeDriverProperties struct {
	stype         uint32
	pNext         uintptr
	uuid          [16]byte
	driverVersion uint32
}

type zeDriverExtProps struct {
	name    [256]byte
	version uint32
}

type zeDeviceProperties struct {
	stype                    uint32
	pNext                    uintptr
	dtype                    uint32
	vendorID                 uint32
	deviceID                 uint32
	flags                    uint32
	subdeviceID              uint32
	coreClockRate            uint32
	maxMemAllocSize          uint64
	maxHardwareContexts      uint32
	maxCommandQueuePriority  uint32
	numThreadsPerEU          uint32
	physicalEUSimdWidth      uint32
	numEUsPerSubslice        uint32
	numSubslicesPerSlice     uint32
	numSlices                uint32
	timerResolution          uint64
	timestampValidBits       uint32
	kernelTimestampValidBits uint32
	uuid                     [16]byte
	name                     [256]byte
}

type zeDeviceGraphProperties struct {
	stype                      uint32
	pNext                      uintptr
	graphExtensionVersion      uint32
	compilerMajor              uint16
	compilerMinor              uint16
	graphFormatsSupported      uint32
	maxOVOpsetVersionSupported uint32
}

type zeContextDesc struct {
	stype uint32
	pNext uintptr
	flags uint32
}

type zeCommandQueueGroupProperties struct {
	stype                    uint32
	pNext                    uintptr
	flags                    uint32
	maxMemoryFillPatternSize uintptr
	numQueues                uint32
}

type zeCommandQueueDesc struct {
	stype    uint32
	pNext    uintptr
	ordinal  uint32
	index    uint32
	flags    uint32
	mode     uint32
	priority uint32
}

type zeCommandListDesc struct {
	stype   uint32
	pNext   uintptr
	ordinal uint32
	flags   uint32
}

type zeHostMemAllocDesc struct {
	stype uint32
	pNext uintptr
	flags uint32
}

type zeGraphDesc struct {
	stype       uint32
	pNext       uintptr
	format      uint32
	inputSize   uintptr
	pInput      *byte
	pBuildFlags *byte
}

type zeGraphProperties struct {
	stype        uint32
	pNext        uintptr
	numGraphArgs uint32
}

type zeGraphArgumentProperties struct {
	stype            uint32
	pNext            uintptr
	name             [256]byte
	argType          uint32
	dims             [5]uint32
	networkPrecision uint32
	networkLayout    uint32
	devicePrecision  uint32
	deviceLayout     uint32
}

// zeGraphDDI is ZE_extension_graph's function table through v1.8.
type zeGraphDDI struct {
	pfnCreate                         uintptr // 1.0
	pfnDestroy                        uintptr
	pfnGetProperties                  uintptr
	pfnGetArgumentProperties          uintptr
	pfnSetArgumentValue               uintptr
	pfnAppendGraphInitialize          uintptr
	pfnAppendGraphExecute             uintptr
	pfnGetNativeBinary                uintptr
	pfnDeviceGetGraphProperties       uintptr
	pfnGraphGetArgumentMetadata       uintptr // 1.1
	pfnGetArgumentProperties2         uintptr
	pfnGetArgumentProperties3         uintptr // 1.2
	pfnQueryNetworkCreate             uintptr // 1.3
	pfnQueryNetworkDestroy            uintptr
	pfnQueryNetworkGetSupportedLayers uintptr
	pfnBuildLogGetString              uintptr // 1.4
	pfnCreate2                        uintptr // 1.5
	pfnQueryNetworkCreate2            uintptr
	pfnQueryContextMemory             uintptr
	pfnDeviceGetGraphProperties2      uintptr // 1.6
	pfnGetNativeBinary2               uintptr // 1.7
	pfnGetProperties2                 uintptr // 1.8
	pfnGraphInitialize                uintptr
}

type driverState struct {
	info    Info
	found   bool
	err     error
	hDriver uintptr
	hDevice uintptr
	ordinal uint32
	extVer  uint32
	ddi     *zeGraphDDI

	zeContextCreate                   *windows.LazyProc
	zeContextDestroy                  *windows.LazyProc
	zeCommandQueueCreate              *windows.LazyProc
	zeCommandQueueDestroy             *windows.LazyProc
	zeCommandQueueExecuteCommandLists *windows.LazyProc
	zeCommandQueueSynchronize         *windows.LazyProc
	zeCommandListCreate               *windows.LazyProc
	zeCommandListDestroy              *windows.LazyProc
	zeCommandListClose                *windows.LazyProc
	zeCommandListReset                *windows.LazyProc
	zeMemAllocHost                    *windows.LazyProc
	zeMemFree                         *windows.LazyProc
}

var (
	drvOnce sync.Once
	drv     driverState
)

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

func zeErr(what string, r uintptr) error {
	return fmt.Errorf("%s failed (%#x)", what, uint32(r))
}

func initDriver() {
	dll := windows.NewLazySystemDLL("ze_loader.dll")
	if err := dll.Load(); err != nil {
		return // no Level Zero runtime on this computer
	}
	zeInitDrivers := dll.NewProc("zeInitDrivers")
	zeInit := dll.NewProc("zeInit")
	zeDriverGet := dll.NewProc("zeDriverGet")
	zeDriverGetProperties := dll.NewProc("zeDriverGetProperties")
	zeDriverGetExtensionProperties := dll.NewProc("zeDriverGetExtensionProperties")
	zeDriverGetExtensionFunctionAddress := dll.NewProc("zeDriverGetExtensionFunctionAddress")
	zeDeviceGet := dll.NewProc("zeDeviceGet")
	zeDeviceGetProperties := dll.NewProc("zeDeviceGetProperties")
	zeDeviceGetCommandQueueGroupProperties := dll.NewProc("zeDeviceGetCommandQueueGroupProperties")

	for _, p := range []*windows.LazyProc{
		zeDriverGetProperties, zeDriverGetExtensionProperties,
		zeDriverGetExtensionFunctionAddress, zeDeviceGet,
		zeDeviceGetProperties, zeDeviceGetCommandQueueGroupProperties,
	} {
		if p.Find() != nil {
			return
		}
	}

	// Ask for NPU drivers alone so discrete GPUs stay asleep in D3cold.
	var drivers []uintptr
	if zeInitDrivers.Find() == nil {
		var count uint32
		desc := zeInitDriverTypeDesc{stype: zeStypeInitDriverTypeDesc, flags: zeInitDriverTypeFlagNPU}
		r, _, _ := zeInitDrivers.Call(uintptr(unsafe.Pointer(&count)), 0, uintptr(unsafe.Pointer(&desc)))
		if r == 0 && count > 0 {
			drivers = make([]uintptr, count)
			r, _, _ = zeInitDrivers.Call(uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&drivers[0])), uintptr(unsafe.Pointer(&desc)))
			if r != 0 {
				drivers = nil
			}
		}
	}
	if len(drivers) == 0 && zeInit.Find() == nil && zeDriverGet.Find() == nil {
		if r, _, _ := zeInit.Call(zeInitFlagVPUOnly); r == 0 {
			var count uint32
			if r, _, _ := zeDriverGet.Call(uintptr(unsafe.Pointer(&count)), 0); r == 0 && count > 0 {
				drivers = make([]uintptr, count)
				if r, _, _ := zeDriverGet.Call(uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&drivers[0]))); r != 0 {
					drivers = nil
				}
			}
		}
	}
	if len(drivers) == 0 {
		return // no NPU driver installed
	}

	// Pick the Intel NPU driver and its VPU device.
	var hDriver, hDevice uintptr
	var devProps zeDeviceProperties
	var drvProps zeDriverProperties
	for _, d := range drivers {
		dp := zeDriverProperties{stype: zeStypeDriverProps}
		if r, _, _ := zeDriverGetProperties.Call(d, uintptr(unsafe.Pointer(&dp))); r != 0 {
			continue
		}
		var devCount uint32
		if r, _, _ := zeDeviceGet.Call(d, uintptr(unsafe.Pointer(&devCount)), 0); r != 0 || devCount == 0 {
			continue
		}
		devs := make([]uintptr, devCount)
		if r, _, _ := zeDeviceGet.Call(d, uintptr(unsafe.Pointer(&devCount)), uintptr(unsafe.Pointer(&devs[0]))); r != 0 {
			continue
		}
		for _, dv := range devs {
			vp := zeDeviceProperties{stype: zeStypeDeviceProps}
			if r, _, _ := zeDeviceGetProperties.Call(dv, uintptr(unsafe.Pointer(&vp))); r != 0 {
				continue
			}
			if vp.dtype == zeDeviceTypeVPU {
				hDriver, hDevice, devProps, drvProps = d, dv, vp, dp
				if dp.uuid == npuDriverUUID {
					break
				}
			}
		}
		if hDevice != 0 && drvProps.uuid == npuDriverUUID {
			break
		}
	}
	if hDevice == 0 {
		return // no VPU/NPU device on this computer
	}

	drv.found = true
	drv.hDriver = hDriver
	drv.hDevice = hDevice
	drv.info.Device = strings.TrimSpace(cstr(devProps.name[:]))
	drv.info.DeviceID = devProps.deviceID
	drv.info.DriverVersion = drvProps.driverVersion

	// Bind required core procedures.
	drv.zeContextCreate = dll.NewProc("zeContextCreate")
	drv.zeContextDestroy = dll.NewProc("zeContextDestroy")
	drv.zeCommandQueueCreate = dll.NewProc("zeCommandQueueCreate")
	drv.zeCommandQueueDestroy = dll.NewProc("zeCommandQueueDestroy")
	drv.zeCommandQueueExecuteCommandLists = dll.NewProc("zeCommandQueueExecuteCommandLists")
	drv.zeCommandQueueSynchronize = dll.NewProc("zeCommandQueueSynchronize")
	drv.zeCommandListCreate = dll.NewProc("zeCommandListCreate")
	drv.zeCommandListDestroy = dll.NewProc("zeCommandListDestroy")
	drv.zeCommandListClose = dll.NewProc("zeCommandListClose")
	drv.zeCommandListReset = dll.NewProc("zeCommandListReset")
	drv.zeMemAllocHost = dll.NewProc("zeMemAllocHost")
	drv.zeMemFree = dll.NewProc("zeMemFree")
	for _, p := range []*windows.LazyProc{
		drv.zeContextCreate, drv.zeContextDestroy,
		drv.zeCommandQueueCreate, drv.zeCommandQueueDestroy,
		drv.zeCommandQueueExecuteCommandLists, drv.zeCommandQueueSynchronize,
		drv.zeCommandListCreate, drv.zeCommandListDestroy,
		drv.zeCommandListClose, drv.zeCommandListReset,
		drv.zeMemAllocHost, drv.zeMemFree,
	} {
		if err := p.Find(); err != nil {
			drv.err = err
			return
		}
	}

	// Find the highest supported ZE_extension_graph up to 1.8 (whose DDI layout we read).
	var extCount uint32
	if r, _, _ := zeDriverGetExtensionProperties.Call(hDriver, uintptr(unsafe.Pointer(&extCount)), 0); r != 0 || extCount == 0 {
		drv.err = errors.New("the NPU driver exposes no Level Zero extensions")
		return
	}
	exts := make([]zeDriverExtProps, extCount)
	if r, _, _ := zeDriverGetExtensionProperties.Call(hDriver, uintptr(unsafe.Pointer(&extCount)), uintptr(unsafe.Pointer(&exts[0]))); r != 0 {
		drv.err = zeErr("zeDriverGetExtensionProperties", r)
		return
	}
	var graphExtName string
	var graphExtVer uint32
	const maxVer = 1<<16 | 8
	for _, e := range exts {
		n := cstr(e.name[:])
		if (n == "ZE_extension_graph" || strings.HasPrefix(n, "ZE_extension_graph_1_")) && e.version>>16 == 1 {
			v := e.version
			if n == "ZE_extension_graph" && v > maxVer {
				continue // prefer a versioned 1.x table whose layout matches zeGraphDDI
			}
			if v > graphExtVer && v <= maxVer {
				graphExtVer, graphExtName = v, n
			}
		}
	}
	if graphExtName == "" {
		// Fall back to base ZE_extension_graph if no 1.1..1.8 alias was listed;
		// newer 1.x DDI tables only append fields past 1.8.
		for _, e := range exts {
			if cstr(e.name[:]) == "ZE_extension_graph" && e.version>>16 == 1 {
				graphExtVer, graphExtName = e.version, "ZE_extension_graph"
				break
			}
		}
	}
	if graphExtName == "" {
		drv.err = errors.New("the NPU driver has no ZE_extension_graph")
		return
	}

	var ddi *zeGraphDDI
	extBytes := append([]byte(graphExtName), 0)
	r, _, _ := zeDriverGetExtensionFunctionAddress.Call(hDriver, uintptr(unsafe.Pointer(&extBytes[0])), uintptr(unsafe.Pointer(&ddi)))
	runtime.KeepAlive(extBytes)
	if r != 0 || ddi == nil || ddi.pfnCreate == 0 || ddi.pfnDeviceGetGraphProperties == 0 {
		drv.err = errors.New("couldn't load ZE_extension_graph functions")
		return
	}
	drv.extVer = graphExtVer
	drv.ddi = ddi

	gp := zeDeviceGraphProperties{stype: zeGraphStypeDeviceProps}
	r, _, _ = syscall.SyscallN(ddi.pfnDeviceGetGraphProperties, hDevice, uintptr(unsafe.Pointer(&gp)))
	if r != 0 {
		drv.err = zeErr("pfnDeviceGetGraphProperties", r)
		return
	}
	if gp.maxOVOpsetVersionSupported < 8 {
		drv.err = fmt.Errorf("NPU compiler supports opset %d, need at least 8", gp.maxOVOpsetVersionSupported)
		return
	}
	drv.info.CompilerMajor = gp.compilerMajor
	drv.info.CompilerMinor = gp.compilerMinor
	drv.info.MaxOpset = gp.maxOVOpsetVersionSupported

	var qgCount uint32
	if r, _, _ := zeDeviceGetCommandQueueGroupProperties.Call(hDevice, uintptr(unsafe.Pointer(&qgCount)), 0); r == 0 && qgCount > 0 {
		qgs := make([]zeCommandQueueGroupProperties, qgCount)
		for i := range qgs {
			qgs[i].stype = zeStypeCmdQueueGroupProps
		}
		if r, _, _ := zeDeviceGetCommandQueueGroupProperties.Call(hDevice, uintptr(unsafe.Pointer(&qgCount)), uintptr(unsafe.Pointer(&qgs[0]))); r == 0 {
			for i, qg := range qgs {
				if qg.flags&1 != 0 { // ZE_COMMAND_QUEUE_GROUP_PROPERTY_FLAG_COMPUTE
					drv.ordinal = uint32(i)
					break
				}
			}
		}
	}
}

// Probe checks once whether an Intel NPU is available on this computer.
// When ok is false, no NPU is present and err is nil.
func Probe() (info Info, ok bool, err error) {
	drvOnce.Do(initDriver)
	return drv.info, drv.found, drv.err
}

// Compile compiles a serialized OpenVINO IR buffer into a native NPU binary blob.
func Compile(ir []byte) ([]byte, error) {
	if _, ok, err := Probe(); !ok {
		return nil, errors.New("no Intel NPU here")
	} else if err != nil {
		return nil, err
	}
	if len(ir) == 0 {
		return nil, errors.New("empty IR")
	}

	var hCtx uintptr
	ctxDesc := zeContextDesc{stype: zeStypeContextDesc}
	if r, _, _ := drv.zeContextCreate.Call(drv.hDriver, uintptr(unsafe.Pointer(&ctxDesc)), uintptr(unsafe.Pointer(&hCtx))); r != 0 {
		return nil, zeErr("zeContextCreate", r)
	}
	defer drv.zeContextDestroy.Call(hCtx)

	flags := append([]byte(`--inputs_precisions="x:FP32 mask:FP32" --outputs_precisions="logits:FP32" --config PERFORMANCE_HINT="LATENCY"`), 0)
	defer runtime.KeepAlive(ir)
	defer runtime.KeepAlive(flags)
	desc := zeGraphDesc{
		stype:       zeGraphStypeDesc,
		format:      zeGraphFormatNGraphLite,
		inputSize:   uintptr(len(ir)),
		pInput:      &ir[0],
		pBuildFlags: &flags[0],
	}
	defer runtime.KeepAlive(&desc)
	var hGraph uintptr
	r, _, _ := syscall.SyscallN(drv.ddi.pfnCreate, hCtx, drv.hDevice, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&hGraph)))
	if r != 0 {
		// pfnBuildLogGetString is in the table from 1.4 on; an older
		// driver's table ends before it.
		if drv.extVer >= (1<<16|4) && drv.ddi.pfnBuildLogGetString != 0 && hGraph != 0 {
			var sz uint32
			syscall.SyscallN(drv.ddi.pfnBuildLogGetString, hGraph, uintptr(unsafe.Pointer(&sz)), 0)
			if sz > 0 && sz < 1<<20 {
				buf := make([]byte, sz)
				syscall.SyscallN(drv.ddi.pfnBuildLogGetString, hGraph, uintptr(unsafe.Pointer(&sz)), uintptr(unsafe.Pointer(&buf[0])))
				syscall.SyscallN(drv.ddi.pfnDestroy, hGraph)
				return nil, fmt.Errorf("NPU graph compile (%#x): %s", uint32(r), strings.TrimSpace(cstr(buf)))
			}
		}
		if hGraph != 0 {
			syscall.SyscallN(drv.ddi.pfnDestroy, hGraph)
		}
		return nil, zeErr("NPU graph compile", r)
	}
	defer syscall.SyscallN(drv.ddi.pfnDestroy, hGraph)

	var size uintptr
	if r, _, _ := syscall.SyscallN(drv.ddi.pfnGetNativeBinary, hGraph, uintptr(unsafe.Pointer(&size)), 0); r != 0 || size == 0 {
		return nil, zeErr("pfnGetNativeBinary", r)
	}
	raw := make([]byte, size)
	if r, _, _ := syscall.SyscallN(drv.ddi.pfnGetNativeBinary, hGraph, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&raw[0]))); r != 0 {
		return nil, zeErr("pfnGetNativeBinary", r)
	}
	runtime.KeepAlive(raw)
	sum := sha256.Sum256(raw)
	out := make([]byte, 8+32+len(raw))
	copy(out[:8], blobMagic[:])
	copy(out[8:40], sum[:])
	copy(out[40:], raw)
	return out, nil
}

// handles holds the native Level Zero resources owned by a Model, kept in a
// value with no reference back to Model so runtime.AddCleanup can release it.
type handles struct {
	hCtx   uintptr
	hGraph uintptr
	hQueue uintptr
	hList  uintptr
	xPtr   *float32
	mPtr   *float32
	oPtr   *float32
}

func (h handles) close() {
	if h.hGraph != 0 {
		syscall.SyscallN(drv.ddi.pfnDestroy, h.hGraph)
	}
	if h.hList != 0 {
		drv.zeCommandListDestroy.Call(h.hList)
	}
	if h.hQueue != 0 {
		drv.zeCommandQueueDestroy.Call(h.hQueue)
	}
	if h.oPtr != nil {
		drv.zeMemFree.Call(h.hCtx, uintptr(unsafe.Pointer(h.oPtr)))
	}
	if h.mPtr != nil {
		drv.zeMemFree.Call(h.hCtx, uintptr(unsafe.Pointer(h.mPtr)))
	}
	if h.xPtr != nil {
		drv.zeMemFree.Call(h.hCtx, uintptr(unsafe.Pointer(h.xPtr)))
	}
	if h.hCtx != 0 {
		drv.zeContextDestroy.Call(h.hCtx)
	}
}

// Model is a compiled NPU graph opened on Level Zero; it is released when
// the last reference to it is collected.
type Model struct {
	mu     sync.Mutex
	hd     handles
	s, h   int
	k      int
	Device string
}

// Open loads a native NPU binary blob from path and prepares its execution
// command list for input x [1, s, h], mask [1, 1, 1, s] and output logits [1, s, k].
func Open(path string, s, h, k int) (*Model, error) {
	info, ok, err := Probe()
	if !ok {
		return nil, errors.New("no Intel NPU here")
	}
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) <= 40 || [8]byte(data[:8]) != blobMagic {
		return nil, errors.New("corrupt NPU blob")
	}
	blob := data[40:]
	if sha256.Sum256(blob) != [32]byte(data[8:40]) {
		return nil, errors.New("corrupt NPU blob")
	}
	// Intel's Level Zero UMD wraps pInput without copying in pfnCreate and
	// reads its ELF weight sections in pfnGraphInitialize /
	// pfnAppendGraphInitialize, so data must stay live until Open returns
	// (and past hd.close on any error path).
	defer runtime.KeepAlive(data)

	var hd handles
	fail := func(e error) (*Model, error) {
		hd.close()
		return nil, e
	}

	ctxDesc := zeContextDesc{stype: zeStypeContextDesc}
	if r, _, _ := drv.zeContextCreate.Call(drv.hDriver, uintptr(unsafe.Pointer(&ctxDesc)), uintptr(unsafe.Pointer(&hd.hCtx))); r != 0 {
		return fail(zeErr("zeContextCreate", r))
	}

	gdesc := zeGraphDesc{
		stype:     zeGraphStypeDesc,
		format:    zeGraphFormatNative,
		inputSize: uintptr(len(blob)),
		pInput:    &blob[0],
	}
	defer runtime.KeepAlive(&gdesc)
	r, _, _ := syscall.SyscallN(drv.ddi.pfnCreate, hd.hCtx, drv.hDevice, uintptr(unsafe.Pointer(&gdesc)), uintptr(unsafe.Pointer(&hd.hGraph)))
	if r != 0 || hd.hGraph == 0 {
		return fail(zeErr("loading NPU blob", r))
	}

	gp := zeGraphProperties{stype: zeGraphStypeProps}
	if r, _, _ := syscall.SyscallN(drv.ddi.pfnGetProperties, hd.hGraph, uintptr(unsafe.Pointer(&gp))); r != 0 {
		return fail(zeErr("pfnGetProperties", r))
	}
	if gp.numGraphArgs != 3 {
		return fail(fmt.Errorf("NPU graph has %d arguments, want 3", gp.numGraphArgs))
	}

	argIdx := map[string]uint32{}
	for i := uint32(0); i < gp.numGraphArgs; i++ {
		ap := zeGraphArgumentProperties{stype: zeGraphStypeArgProps}
		if r, _, _ := syscall.SyscallN(drv.ddi.pfnGetArgumentProperties, hd.hGraph, uintptr(i), uintptr(unsafe.Pointer(&ap))); r != 0 {
			return fail(zeErr("pfnGetArgumentProperties", r))
		}
		name := cstr(ap.name[:])
		if ap.devicePrecision != zeGraphPrecFP32 {
			return fail(fmt.Errorf("NPU argument %q has precision %d, want FP32", name, ap.devicePrecision))
		}
		switch name {
		case "x":
			if ap.argType != zeGraphArgInput || ap.dims[0] != 1 || int(ap.dims[1]) != s || int(ap.dims[2]) != h {
				return fail(fmt.Errorf("NPU input x has dims %v", ap.dims))
			}
		case "mask":
			if ap.argType != zeGraphArgInput || ap.dims[0] != 1 || ap.dims[1] != 1 || ap.dims[2] != 1 || int(ap.dims[3]) != s {
				return fail(fmt.Errorf("NPU input mask has dims %v", ap.dims))
			}
		case "logits":
			if ap.argType != zeGraphArgOutput || ap.dims[0] != 1 || int(ap.dims[1]) != s || int(ap.dims[2]) != k {
				return fail(fmt.Errorf("NPU output logits has dims %v", ap.dims))
			}
		default:
			return fail(fmt.Errorf("unexpected NPU argument %q", name))
		}
		argIdx[name] = i
	}
	if len(argIdx) != 3 {
		return fail(errors.New("missing expected NPU graph arguments"))
	}

	qDesc := zeCommandQueueDesc{stype: zeStypeCmdQueueDesc, ordinal: drv.ordinal}
	if r, _, _ := drv.zeCommandQueueCreate.Call(hd.hCtx, drv.hDevice, uintptr(unsafe.Pointer(&qDesc)), uintptr(unsafe.Pointer(&hd.hQueue))); r != 0 {
		return fail(zeErr("zeCommandQueueCreate", r))
	}
	lDesc := zeCommandListDesc{stype: zeStypeCmdListDesc, ordinal: drv.ordinal}
	if r, _, _ := drv.zeCommandListCreate.Call(hd.hCtx, drv.hDevice, uintptr(unsafe.Pointer(&lDesc)), uintptr(unsafe.Pointer(&hd.hList))); r != 0 {
		return fail(zeErr("zeCommandListCreate", r))
	}

	initDone := false
	if drv.extVer >= (1<<16|8) && drv.ddi.pfnGraphInitialize != 0 {
		if r, _, _ := syscall.SyscallN(drv.ddi.pfnGraphInitialize, hd.hGraph); r == 0 {
			initDone = true
		}
	}
	if !initDone && drv.ddi.pfnAppendGraphInitialize != 0 {
		if r, _, _ := syscall.SyscallN(drv.ddi.pfnAppendGraphInitialize, hd.hList, hd.hGraph, 0, 0, 0); r != 0 {
			return fail(zeErr("pfnAppendGraphInitialize", r))
		}
		if r, _, _ := drv.zeCommandListClose.Call(hd.hList); r != 0 {
			return fail(zeErr("zeCommandListClose", r))
		}
		if r, _, _ := drv.zeCommandQueueExecuteCommandLists.Call(hd.hQueue, 1, uintptr(unsafe.Pointer(&hd.hList)), 0); r != 0 {
			return fail(zeErr("zeCommandQueueExecuteCommandLists", r))
		}
		if r, _, _ := drv.zeCommandQueueSynchronize.Call(hd.hQueue, syncTimeoutNS); r != 0 {
			return fail(zeErr("zeCommandQueueSynchronize", r))
		}
		if r, _, _ := drv.zeCommandListReset.Call(hd.hList); r != 0 {
			return fail(zeErr("zeCommandListReset", r))
		}
	}

	hDesc := zeHostMemAllocDesc{stype: zeStypeHostMemAllocDesc}
	if r, _, _ := drv.zeMemAllocHost.Call(hd.hCtx, uintptr(unsafe.Pointer(&hDesc)), uintptr(s*h*4), 4096, uintptr(unsafe.Pointer(&hd.xPtr))); r != 0 || hd.xPtr == nil {
		return fail(zeErr("zeMemAllocHost(x)", r))
	}
	if r, _, _ := drv.zeMemAllocHost.Call(hd.hCtx, uintptr(unsafe.Pointer(&hDesc)), uintptr(s*4), 4096, uintptr(unsafe.Pointer(&hd.mPtr))); r != 0 || hd.mPtr == nil {
		return fail(zeErr("zeMemAllocHost(mask)", r))
	}
	if r, _, _ := drv.zeMemAllocHost.Call(hd.hCtx, uintptr(unsafe.Pointer(&hDesc)), uintptr(s*k*4), 4096, uintptr(unsafe.Pointer(&hd.oPtr))); r != 0 || hd.oPtr == nil {
		return fail(zeErr("zeMemAllocHost(logits)", r))
	}

	if r, _, _ := syscall.SyscallN(drv.ddi.pfnSetArgumentValue, hd.hGraph, uintptr(argIdx["x"]), uintptr(unsafe.Pointer(hd.xPtr))); r != 0 {
		return fail(zeErr("pfnSetArgumentValue(x)", r))
	}
	if r, _, _ := syscall.SyscallN(drv.ddi.pfnSetArgumentValue, hd.hGraph, uintptr(argIdx["mask"]), uintptr(unsafe.Pointer(hd.mPtr))); r != 0 {
		return fail(zeErr("pfnSetArgumentValue(mask)", r))
	}
	if r, _, _ := syscall.SyscallN(drv.ddi.pfnSetArgumentValue, hd.hGraph, uintptr(argIdx["logits"]), uintptr(unsafe.Pointer(hd.oPtr))); r != 0 {
		return fail(zeErr("pfnSetArgumentValue(logits)", r))
	}

	if r, _, _ := syscall.SyscallN(drv.ddi.pfnAppendGraphExecute, hd.hList, hd.hGraph, 0, 0, 0, 0); r != 0 {
		return fail(zeErr("pfnAppendGraphExecute", r))
	}
	if r, _, _ := drv.zeCommandListClose.Call(hd.hList); r != 0 {
		return fail(zeErr("zeCommandListClose", r))
	}

	m := &Model{hd: hd, s: s, h: h, k: k, Device: info.Device}
	runtime.AddCleanup(m, handles.close, hd)
	return m, nil
}

// Run computes logits [s, k] for x [s, h] and mask [s] on the NPU.
func (m *Model) Run(x, mask []float32, s, h, k int) ([]float32, error) {
	if len(x) != s*h || len(mask) != s || s != m.s || h != m.h || k != m.k {
		return nil, errors.New("inputs of another size")
	}
	out := make([]float32, s*k)
	m.mu.Lock()
	copy(unsafe.Slice(m.hd.xPtr, s*h), x)
	copy(unsafe.Slice(m.hd.mPtr, s), mask)
	r, _, _ := drv.zeCommandQueueExecuteCommandLists.Call(m.hd.hQueue, 1, uintptr(unsafe.Pointer(&m.hd.hList)), 0)
	if r == 0 {
		r, _, _ = drv.zeCommandQueueSynchronize.Call(m.hd.hQueue, syncTimeoutNS)
	}
	if r == 0 {
		copy(out, unsafe.Slice(m.hd.oPtr, s*k))
	}
	m.mu.Unlock()
	runtime.KeepAlive(m)
	if r != 0 {
		return nil, zeErr("NPU execute", r)
	}
	return out, nil
}
