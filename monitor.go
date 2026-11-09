package main

// Work area (screen minus taskbar) of the monitor you're using, so cards and
// windows centre on the screen you're looking at, not always the main one.

import "unsafe"

var (
	pMonitorFromPoint = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW  = user32.NewProc("GetMonitorInfoW")
)

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// activeWorkArea: the work area of the monitor under the mouse pointer
func activeWorkArea() rect {
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	packed := uintptr(uint32(pt.X)) | uintptr(uint32(pt.Y))<<32 // POINT passed by value
	mon, _, _ := pMonitorFromPoint.Call(packed, 2)              // MONITOR_DEFAULTTONEAREST
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if mon != 0 {
		if r, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			return mi.Work
		}
	}
	var wa rect
	pSystemParametersInfo.Call(0x30, 0, uintptr(unsafe.Pointer(&wa)), 0) // SPI_GETWORKAREA
	return wa
}
