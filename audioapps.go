package main

// Apps that make sound but don't share "now playing" info (Discord, games...).
// They come from the Windows Volume Mixer: we can change their volume and
// mute them, and show their real name and icon.

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	version                  = syscall.NewLazyDLL("version.dll")
	pGetFileVersionInfoSizeW = version.NewProc("GetFileVersionInfoSizeW")
	pGetFileVersionInfoW     = version.NewProc("GetFileVersionInfoW")
	pVerQueryValueW          = version.NewProc("VerQueryValueW")
	pPrivateExtractIconsW    = user32.NewProc("PrivateExtractIconsW")
	pDrawIconEx              = user32.NewProc("DrawIconEx")
	pDestroyIcon             = user32.NewProc("DestroyIcon")
	pDeleteDC                = gdi32.NewProc("DeleteDC")

	nameCacheMu sync.Mutex
	nameCache   = map[string]string{}
)

func processPath(pid uint32) string {
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := pQueryFullImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

// appDisplayName reads the program's own name ("Discord", "Counter-Strike 2")
// from its file details, falling back to a tidied-up exe name.
func appDisplayName(path, exe string) string {
	nameCacheMu.Lock()
	if n, ok := nameCache[exe]; ok {
		nameCacheMu.Unlock()
		return n
	}
	nameCacheMu.Unlock()

	name := ""
	if path != "" {
		p := u16(path)
		size, _, _ := pGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p)), 0)
		if size > 0 && size < 4<<20 {
			data := make([]byte, size)
			if r, _, _ := pGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&data[0]))); r != 0 {
				var tr *[2]uint16
				var trLen uint32
				if r, _, _ := pVerQueryValueW.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(u16(`\VarFileInfo\Translation`))),
					uintptr(unsafe.Pointer(&tr)), uintptr(unsafe.Pointer(&trLen))); r != 0 && tr != nil && trLen >= 4 {
					for _, field := range []string{"FileDescription", "ProductName"} {
						q := `\StringFileInfo\` + hex4(tr[0]) + hex4(tr[1]) + `\` + field
						var val *uint16
						var vl uint32
						if r, _, _ := pVerQueryValueW.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(u16(q))),
							uintptr(unsafe.Pointer(&val)), uintptr(unsafe.Pointer(&vl))); r != 0 && val != nil && vl > 1 {
							s := strings.TrimSpace(syscall.UTF16ToString(unsafe.Slice(val, vl)))
							if s != "" && len(s) < 48 {
								name = s
								break
							}
						}
					}
				}
			}
		}
	}
	if name == "" {
		name = friendlyName(exe)
	}
	if fixed, ok := map[string]string{"spotify.exe": "Spotify", "chrome.exe": "Chrome", "msedge.exe": "Edge", "firefox.exe": "Firefox"}[exe]; ok {
		name = fixed // their file descriptions are long ("Google Chrome", "Microsoft Edge"...)
	}
	nameCacheMu.Lock()
	nameCache[exe] = name
	nameCacheMu.Unlock()
	return name
}

func hex4(v uint16) string {
	const d = "0123456789abcdef"
	return string([]byte{d[v>>12&0xF], d[v>>8&0xF], d[v>>4&0xF], d[v&0xF]})
}

// loadAppIcon draws the program's icon at n×n, premultiplied BGRA.
func loadAppIcon(path string, n int) []uint32 {
	if path == "" || n <= 0 {
		return nil
	}
	var hicon, id uintptr
	got, _, _ := pPrivateExtractIconsW.Call(uintptr(unsafe.Pointer(u16(path))), 0, uintptr(n), uintptr(n),
		uintptr(unsafe.Pointer(&hicon)), uintptr(unsafe.Pointer(&id)), 1, 0)
	if got == 0 || got == 0xFFFFFFFF || hicon == 0 {
		return nil
	}
	defer pDestroyIcon.Call(hicon)

	bi := bitmapinfo{Width: int32(n), Height: -int32(n), Planes: 1, BitCount: 32}
	bi.Size = 40
	dc, _, _ := pCreateCompatibleDC.Call(0)
	defer pDeleteDC.Call(dc)
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return nil
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dc, bmp)
	pDrawIconEx.Call(dc, 0, 0, hicon, uintptr(n), uintptr(n), 0, 0, 3) // DI_NORMAL
	pSelectObject.Call(dc, old)

	px := make([]uint32, n*n)
	copy(px, unsafe.Slice((*uint32)(unsafe.Pointer(bits)), n*n))
	hasAlpha := false
	for _, p := range px {
		if p>>24 != 0 {
			hasAlpha = true
			break
		}
	}
	if !hasAlpha { // old-style icon without an alpha channel
		for i, p := range px {
			if p&0xFFFFFF != 0 {
				px[i] = p | 0xFF000000
			}
		}
	}
	return px
}

type audioApp struct {
	exe  string
	pid  uint32
	peak float32
}

// audioApps lists apps in the Volume Mixer, one entry per program.
func audioApps() []audioApp {
	sessions, cleanup := listSessions()
	defer cleanup()
	byExe := map[string]*audioApp{}
	var order []string
	for _, s := range sessions {
		if s.exe == "" || strings.HasPrefix(s.exe, "mediakeys") {
			continue
		}
		if a, ok := byExe[s.exe]; ok {
			if s.peak > a.peak {
				a.peak = s.peak
			}
			continue
		}
		byExe[s.exe] = &audioApp{exe: s.exe, pid: s.pid, peak: s.peak}
		order = append(order, s.exe)
	}
	out := make([]audioApp, 0, len(order))
	for _, e := range order {
		out = append(out, *byExe[e])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].exe < out[j].exe })
	return out
}

// appMute reads (and optionally flips) the mute state of every session of exe.
func appMute(exe string, flip bool) (muted bool, ok bool) {
	sessions, cleanup := listSessions()
	defer cleanup()
	first := true
	for _, s := range sessions {
		if s.exe != exe {
			continue
		}
		v := qi(s.ctl, &iidISimpleAudioVolume)
		if v == 0 {
			continue
		}
		if first {
			var m int32
			comCall(v, 6, uintptr(unsafe.Pointer(&m))) // GetMute
			muted, first, ok = m != 0, false, true
			if flip {
				muted = !muted
			}
		}
		if flip {
			b := uintptr(0)
			if muted {
				b = 1
			}
			comCall(v, 5, b, 0) // SetMute
		}
		release(v)
	}
	return
}

// snapshot for a Volume Mixer app (watcher thread)
func (w *watcher) audioSnap(exe string) mediaSnap {
	for _, a := range audioApps() {
		if a.exe != exe {
			continue
		}
		path := processPath(a.pid)
		s := mediaSnap{valid: true, audioOnly: true, exe: exe, app: exe, title: appDisplayName(path, exe)}
		s.muted, _ = appMute(exe, false)
		s.status = statusPaused
		if a.peak > 0.001 && !s.muted {
			s.status = statusPlaying
		}
		icon, ok := w.iconCache[exe]
		if !ok {
			icon = loadAppIcon(path, w.iconN)
			w.iconCache[exe] = icon
		}
		s.icon, s.iconN = icon, w.iconN
		return s
	}
	return mediaSnap{}
}

func appNameFor(s mediaSnap) string {
	if s.audioOnly {
		return s.title
	}
	if h := exeHint(s.app); h != "" {
		return friendlyName(h)
	}
	return "this app"
}

func mediaExes(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		if h := exeHint(id); h != "" {
			m[h] = true
		}
		m[strings.ToLower(filepath.Base(id))] = true
	}
	return m
}
