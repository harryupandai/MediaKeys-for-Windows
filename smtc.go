package main

// Reads "what's playing" from Windows' media session (the same info the
// Windows volume flyout shows): title, artist, playback status, cover art.
// Runs on its own background thread and keeps a snapshot ready so the
// popup can appear instantly.

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	combase                     = syscall.NewLazyDLL("combase.dll")
	shcore                      = syscall.NewLazyDLL("shcore.dll")
	pRoInitialize               = combase.NewProc("RoInitialize")
	pRoGetActivationFactory     = combase.NewProc("RoGetActivationFactory")
	pWindowsCreateString        = combase.NewProc("WindowsCreateString")
	pWindowsDeleteString        = combase.NewProc("WindowsDeleteString")
	pWindowsGetStringRawBuffer  = combase.NewProc("WindowsGetStringRawBuffer")
	pCreateStreamOverRandAccess = shcore.NewProc("CreateStreamOverRandomAccessStream")

	iidSessionManagerStatics = guid{0x2050C4EE, 0x11A0, 0x57DE, [8]byte{0xAE, 0xD7, 0xC9, 0x7C, 0x70, 0x33, 0x82, 0x45}}
	iidIAsyncInfo            = guid{0x00000036, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIStream               = guid{0x0000000C, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

const (
	WM_MEDIAUPDATE = WM_APP + 8

	statusPlaying = 4
	statusPaused  = 5
)

type mediaSnap struct {
	valid      bool
	status     int
	title      string
	artist     string
	app        string
	art        []uint32 // n*n opaque pixels, already scaled for the popup
	artN       int
	artVersion int

	// Volume Mixer apps (Discord, games...) with no media info
	audioOnly bool
	exe       string
	muted     bool
	icon      []uint32 // n*n premultiplied
	iconN     int
}

var (
	snapMu    sync.Mutex
	snap      mediaSnap
	fastUntil atomic.Int64 // poll quickly until this unix-ms time
)

func getSnap() mediaSnap {
	snapMu.Lock()
	defer snapMu.Unlock()
	return snap
}

func pollFast(d time.Duration) { fastUntil.Store(time.Now().Add(d).UnixMilli()) }

func hstring(s string) uintptr {
	p, _ := syscall.UTF16FromString(s)
	var h uintptr
	pWindowsCreateString.Call(uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&h)))
	return h
}

func takeHString(h uintptr) string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := pWindowsGetStringRawBuffer.Call(h, uintptr(unsafe.Pointer(&n)))
	s := ""
	if p != 0 && n > 0 {
		s = syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(p)), n))
	}
	pWindowsDeleteString.Call(h)
	return s
}

func comOut(obj uintptr, idx int) uintptr {
	var r uintptr
	if comCall(obj, idx, uintptr(unsafe.Pointer(&r))) != 0 {
		return 0
	}
	return r
}

func comString(obj uintptr, idx int) string {
	var h uintptr
	if comCall(obj, idx, uintptr(unsafe.Pointer(&h))) != 0 {
		return ""
	}
	return takeHString(h)
}

// await waits for a WinRT IAsyncOperation and returns its result (or 0).
func await(op uintptr, timeout time.Duration) uintptr {
	if op == 0 {
		return 0
	}
	info := qi(op, &iidIAsyncInfo)
	if info == 0 {
		return 0
	}
	defer release(info)
	deadline := time.Now().Add(timeout)
	for {
		var st int32
		comCall(info, 7, uintptr(unsafe.Pointer(&st)))
		switch st {
		case 1: // completed
			return comOut(op, 8)
		case 2, 3: // canceled / error
			return 0
		}
		if time.Now().After(deadline) {
			comCall(info, 9)
			return 0
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func getSessionManager() uintptr {
	h := hstring("Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager")
	var factory uintptr
	r, _, _ := pRoGetActivationFactory.Call(h, uintptr(unsafe.Pointer(&iidSessionManagerStatics)), uintptr(unsafe.Pointer(&factory)))
	pWindowsDeleteString.Call(h)
	if r != 0 || factory == 0 {
		logf("media: no activation factory (0x%x)", r)
		return 0
	}
	defer release(factory)
	op := comOut(factory, 6) // RequestAsync
	defer release(op)
	return await(op, 5*time.Second)
}

func loadThumbnail(props uintptr, n int) []uint32 {
	ref := comOut(props, 15) // Thumbnail
	if ref == 0 {
		return nil
	}
	defer release(ref)
	op := comOut(ref, 6) // OpenReadAsync
	defer release(op)
	stream := await(op, 2*time.Second)
	if stream == 0 {
		return nil
	}
	defer release(stream)
	var is uintptr
	if r, _, _ := pCreateStreamOverRandAccess.Call(stream, uintptr(unsafe.Pointer(&iidIStream)), uintptr(unsafe.Pointer(&is))); r != 0 || is == 0 {
		return nil
	}
	defer release(is)
	var data []byte
	chunk := make([]byte, 64*1024)
	for len(data) < 8<<20 {
		var got uint32
		hr := comCall(is, 3, uintptr(unsafe.Pointer(&chunk[0])), uintptr(len(chunk)), uintptr(unsafe.Pointer(&got)))
		data = append(data, chunk[:got]...)
		if got == 0 || hr != 0 {
			break
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return scaleSquare(img, n)
}

// centre-crop to a square and box-filter down to n×n
func scaleSquare(img image.Image, n int) []uint32 {
	b := img.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	if side <= 0 || n <= 0 {
		return nil
	}
	ox := b.Min.X + (b.Dx()-side)/2
	oy := b.Min.Y + (b.Dy()-side)/2
	out := make([]uint32, n*n)
	for dy := 0; dy < n; dy++ {
		sy0 := oy + dy*side/n
		sy1 := oy + (dy+1)*side/n
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < n; dx++ {
			sx0 := ox + dx*side/n
			sx1 := ox + (dx+1)*side/n
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, cnt uint64
			for y := sy0; y < sy1; y++ {
				for x := sx0; x < sx1; x++ {
					cr, cg, cb, _ := img.At(x, y).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					cnt++
				}
			}
			r, g, bl = r/cnt>>8, g/cnt>>8, bl/cnt>>8
			out[dy*n+dx] = 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(bl)
		}
	}
	return out
}

// ---- watcher thread state ----

const (
	cmdNext = iota + 1
	cmdPrev
	cmdToggle
	cmdSwitchNext
	cmdSwitchPrev

	WM_SWITCHED = WM_APP + 11 // wParam = number of media apps
)

var cmdCh = make(chan int, 8)

func queueCmd(c int) {
	select {
	case cmdCh <- c:
	default:
	}
}

type watcher struct {
	mgr        uintptr
	n          int    // art size in px
	chosen     string // media app ID picked with Shift+Left/Right ("" = follow Windows)
	chosenExe  string // or a Volume Mixer app picked with Shift+Left/Right
	iconN      int
	iconCache  map[string][]uint32
	lastKey    string
	lastApp    string
	art        []uint32
	artTries   int
	artVersion int
	artCache   map[string][]uint32 // so switching back and forth is instant
	prevArt    []uint32            // previous song's cover, to spot stale artwork
	artAfter   time.Time
}

// sessionList returns every media session (each AddRef'd) with its app ID.
func (w *watcher) sessionList() (sess []uintptr, ids []string) {
	list := comOut(w.mgr, 7) // GetSessions -> IVectorView<Session>
	if list == 0 {
		return
	}
	defer release(list)
	var count uint32
	comCall(list, 7, uintptr(unsafe.Pointer(&count))) // get_Size
	for i := uint32(0); i < count && i < 32; i++ {
		if s := comOut1(list, 6, uintptr(i)); s != 0 { // GetAt
			sess = append(sess, s)
			ids = append(ids, comString(s, 6))
		}
	}
	return
}

// pick returns the session the keys should act on (caller releases it).
func (w *watcher) pick() uintptr {
	if w.chosenExe != "" {
		return 0 // a Volume Mixer app: no media controls
	}
	if w.chosen != "" {
		sess, ids := w.sessionList()
		var found uintptr
		for i, s := range sess {
			if found == 0 && ids[i] == w.chosen {
				found = s
			} else {
				release(s)
			}
		}
		if found != 0 {
			return found
		}
		w.chosen = "" // that app closed: follow Windows again
	}
	return comOut(w.mgr, 6) // GetCurrentSession
}

func (w *watcher) poll() {
	s := mediaSnap{}
	if w.chosenExe != "" {
		if s = w.audioSnap(w.chosenExe); !s.valid {
			w.chosenExe = "" // app closed: follow Windows again
		}
	}
	if s.audioOnly {
		// nothing more to read
	} else if sess := w.pick(); sess != 0 {
		s.valid = true
		s.app = comString(sess, 6)
		if pi := comOut(sess, 9); pi != 0 { // GetPlaybackInfo
			var st int32
			comCall(pi, 7, uintptr(unsafe.Pointer(&st)))
			s.status = int(st)
			release(pi)
		}
		op := comOut(sess, 7) // TryGetMediaPropertiesAsync
		props := await(op, time.Second)
		release(op)
		if props != 0 {
			s.title = comString(props, 6)
			s.artist = comString(props, 9)
			key := s.app + "|" + s.title + "|" + s.artist
			if key != w.lastKey {
				if w.art != nil {
					w.prevArt = w.art
				}
				w.lastKey, w.art, w.artTries, w.artAfter = key, nil, 0, time.Time{}
				if cached, ok := w.artCache[key]; ok {
					w.art = cached
					w.artVersion++
				} else if s.app == w.lastApp {
					w.artAfter = time.Now().Add(120 * time.Millisecond) // same app, new track: let it update its artwork
				}
				w.lastApp = s.app
			}
			if w.art == nil && w.artTries < 14 && time.Now().After(w.artAfter) {
				a := loadThumbnail(props, w.n)
				// apps sometimes still hand out the previous song's cover for a moment: skip it,
				// unless it keeps coming back (the new song really is from the same album)
				if a != nil && (!sameArt(a, w.prevArt) || w.artTries >= 6) {
					w.art = a
					w.artVersion++
					if len(w.artCache) > 12 {
						w.artCache = map[string][]uint32{}
					}
					w.artCache[key] = a
				}
				w.artTries++
			}
			release(props)
		}
		release(sess)
	}
	if w.art != nil && !s.audioOnly {
		s.art, s.artN, s.artVersion = w.art, w.n, w.artVersion
	}

	snapMu.Lock()
	changed := s.valid != snap.valid || s.status != snap.status || s.title != snap.title ||
		s.artist != snap.artist || s.app != snap.app || s.artVersion != snap.artVersion || (s.art == nil) != (snap.art == nil) ||
		s.audioOnly != snap.audioOnly || s.muted != snap.muted
	snap = s
	snapMu.Unlock()
	if changed {
		pPostMessageW.Call(hwnd, WM_MEDIAUPDATE, 0, 0)
	}
}

// Shift+Left/Right: move to the next/previous app. Media apps come first
// (Spotify, browsers...), then every other app in the Volume Mixer.
func (w *watcher) switchApp(dir int) {
	sess, ids := w.sessionList()
	for _, x := range sess {
		release(x)
	}
	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, "m:"+id)
	}
	covered := mediaExes(ids)
	for _, a := range audioApps() {
		if !covered[a.exe] {
			entries = append(entries, "a:"+a.exe)
		}
	}
	if len(entries) == 0 {
		pPostMessageW.Call(hwnd, WM_SWITCHED, 0, 0)
		return
	}
	cur := ""
	switch {
	case w.chosenExe != "":
		cur = "a:" + w.chosenExe
	case w.chosen != "":
		cur = "m:" + w.chosen
	default:
		if c := comOut(w.mgr, 6); c != 0 {
			cur = "m:" + comString(c, 6)
			release(c)
		}
	}
	idx := 0
	for i, e := range entries {
		if e == cur {
			idx = (i + dir + len(entries)) % len(entries)
			break
		}
	}
	pickd := entries[idx]
	if strings.HasPrefix(pickd, "a:") {
		w.chosen, w.chosenExe = "", pickd[2:]
	} else {
		w.chosen, w.chosenExe = pickd[2:], ""
	}
	w.poll() // refresh the snapshot for the new app right away
	pPostMessageW.Call(hwnd, WM_SWITCHED, uintptr(len(entries)), 0)
}

// Pause key: play/pause the chosen app directly
func (w *watcher) toggle() {
	defer guard("toggle")
	sess := w.pick()
	if sess == 0 {
		send(VK_MEDIA_PLAY_PAUSE)
		return
	}
	defer release(sess)
	idx := 0
	if pi := comOut(sess, 9); pi != 0 {
		var st int32
		comCall(pi, 7, uintptr(unsafe.Pointer(&st)))
		if c := comOut(pi, 6); c != 0 {
			switch {
			case comBool(c, 16): // IsPlayPauseToggleEnabled
				idx = 20 // TryTogglePlayPauseAsync
			case st == statusPlaying && comBool(c, 7):
				idx = 11 // TryPauseAsync
			case st != statusPlaying && comBool(c, 6):
				idx = 10 // TryPlayAsync
			}
			release(c)
		}
		release(pi)
	}
	if idx == 0 {
		send(VK_MEDIA_PLAY_PAUSE)
		return
	}
	op := comOut(sess, idx)
	ok := await(op, time.Second)&0xFF != 0 // the result is a bool
	release(op)
	if !ok {
		logf("media: play/pause request refused, using media key")
		send(VK_MEDIA_PLAY_PAUSE)
	}
}

func startMediaWatcher() {
	go func() {
		runtime.LockOSThread()
		defer guard("mediaWatcher")
		pRoInitialize.Call(1) // multithreaded
		mgr := getSessionManager()
		if mgr == 0 {
			logf("media: session manager unavailable, using fallback detection")
			return
		}
		w := &watcher{mgr: mgr, n: int(math.Round(artSize * scale)), artCache: map[string][]uint32{},
			iconN: int(math.Round(36 * scale)), iconCache: map[string][]uint32{}}
		for {
			w.poll()
			wait := 350 * time.Millisecond
			if time.Now().UnixMilli() < fastUntil.Load() {
				wait = 40 * time.Millisecond
			}
			select {
			case c := <-cmdCh:
				switch c {
				case cmdNext, cmdPrev:
					handleSkip(w, c == cmdNext)
				case cmdToggle:
					w.toggle()
				case cmdSwitchNext:
					w.switchApp(1)
				case cmdSwitchPrev:
					w.switchApp(-1)
				}
			case <-time.After(wait):
			}
		}
	}()
}

func sameArt(a, b []uint32) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := 0; i < len(a); i += 37 {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
