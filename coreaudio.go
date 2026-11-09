package main

// Windows Core Audio (the Volume Mixer) and the small COM helpers the rest of MediaKeys uses.

import (
	"math"
	"strings"
	"syscall"
	"unsafe"
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	ole32               = syscall.NewLazyDLL("ole32.dll")
	pCoInitializeEx     = ole32.NewProc("CoInitializeEx")
	pCoCreateInstance   = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree      = ole32.NewProc("CoTaskMemFree")
	pOpenProcess        = kernel32.NewProc("OpenProcess")
	pCloseHandle        = kernel32.NewProc("CloseHandle")
	pQueryFullImageName = kernel32.NewProc("QueryFullProcessImageNameW")

	clsidMMDeviceEnumerator  = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator   = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioSessionManager2 = guid{0x77AA99A0, 0x1BD6, 0x484F, [8]byte{0x8B, 0xC7, 0x2C, 0x65, 0x4C, 0x9A, 0x9B, 0x6F}}
	iidIAudioSessionControl2 = guid{0xBFB7FF88, 0x7239, 0x4FC9, [8]byte{0x8F, 0xA2, 0x07, 0xC9, 0x50, 0xBE, 0x9C, 0x6D}}
	iidISimpleAudioVolume    = guid{0x87CE5498, 0x68D6, 0x44E5, [8]byte{0x92, 0x15, 0x6D, 0xA4, 0x7E, 0xF8, 0x83, 0xD8}}
)

const clsctxAll = 0x17

func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func release(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

func qi(obj uintptr, iid *guid) uintptr {
	var out uintptr
	if comCall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))) != 0 {
		return 0
	}
	return out
}

func initCOM() { pCoInitializeEx.Call(0, 2) } // apartment-threaded, on the main thread

func processName(pid uint32) string {
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if h == 0 {
		return ""
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := pQueryFullImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	p := syscall.UTF16ToString(buf[:n])
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	return strings.ToLower(p)
}

// changeAppVolume nudges the volume of every audio session owned by exeName
// (e.g. "spotify.exe") by delta (0..1 scale). Returns the new level 0-100,
// or -1 if the app has no audio session (not open / hasn't played anything yet).
func changeAppVolume(exeName string, delta float32) int {
	var enum uintptr
	if r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); r != 0 || enum == 0 {
		return -1
	}
	defer release(enum)

	var device uintptr
	if comCall(enum, 4, 0 /*eRender*/, 1 /*eMultimedia*/, uintptr(unsafe.Pointer(&device))) != 0 || device == 0 {
		return -1
	}
	defer release(device)

	var mgr uintptr
	if comCall(device, 3, uintptr(unsafe.Pointer(&iidIAudioSessionManager2)), clsctxAll, 0, uintptr(unsafe.Pointer(&mgr))) != 0 || mgr == 0 {
		return -1
	}
	defer release(mgr)

	var sessions uintptr
	if comCall(mgr, 5, uintptr(unsafe.Pointer(&sessions))) != 0 || sessions == 0 {
		return -1
	}
	defer release(sessions)

	var count int32
	comCall(sessions, 3, uintptr(unsafe.Pointer(&count)))

	result := -1
	for i := int32(0); i < count; i++ {
		var ctl uintptr
		if comCall(sessions, 4, uintptr(i), uintptr(unsafe.Pointer(&ctl))) != 0 || ctl == 0 {
			continue
		}
		func() {
			defer release(ctl)
			ctl2 := qi(ctl, &iidIAudioSessionControl2)
			if ctl2 == 0 {
				return
			}
			defer release(ctl2)
			var pid uint32
			comCall(ctl2, 14, uintptr(unsafe.Pointer(&pid)))
			if pid == 0 || processName(pid) != exeName {
				return
			}
			vol := qi(ctl, &iidISimpleAudioVolume)
			if vol == 0 {
				return
			}
			defer release(vol)
			var cur float32
			comCall(vol, 4, uintptr(unsafe.Pointer(&cur)))
			next := cur + delta
			if next < 0 {
				next = 0
			} else if next > 1 {
				next = 1
			}
			next = float32(math.Round(float64(next)*100) / 100)
			comCall(vol, 3, uintptr(math.Float32bits(next)), 0) // SetMasterVolume(level, NULL)
			if delta > 0 {
				comCall(vol, 5, 0, 0) // unmute when turning up
			}
			result = int(math.Round(float64(next) * 100))
		}()
	}
	return result
}
