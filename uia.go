package main

// Pressing Spotify's own Play button for a song, through Windows UI
// Automation (the accessibility system screen readers use). Spotify doesn't
// let apps start a song without a login, but its buttons are labelled
// "Play <song> by <artist>", so after opening its search we can press one.

import (
	"net/url"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const WM_PLAYED = WM_APP + 23 // wParam 1 = started playing, 0 = couldn't

var (
	oleaut32         = syscall.NewLazyDLL("oleaut32.dll")
	pSysFreeString   = oleaut32.NewProc("SysFreeString")
	clsidCUIAuto     = guid{0xFF48DBA4, 0x60EF, 0x4201, [8]byte{0xAA, 0x87, 0x54, 0x10, 0x3E, 0xEF, 0x59, 0x4E}}
	iidIUIAutomation = guid{0x30CBE57D, 0xD9D0, 0x452A, [8]byte{0xAB, 0x13, 0x7A, 0xC5, 0xAC, 0x48, 0x25, 0xEE}}

	playBusy   bool    // main thread: an auto-play is in progress
	srchPrevFg uintptr // window that had focus before the search bar opened

	pIsWindowVisible = user32.NewProc("IsWindowVisible")

	// Spotify's window as it was before we sent it to a song, to put it back afterwards
	playTitle, playArtist string // the song or playlist being started, for the recent list
	playKind, playURI     string
	playArt               []uint32
	spotPrevHwnd          uintptr
	spotWasMinimized      bool
	spotWasHidden         bool
)

type variant struct {
	VT         uint16
	r1, r2, r3 uint16
	Val        int64
	pad        int64
}

func takeBSTR(b uintptr) string {
	if b == 0 {
		return ""
	}
	n := *(*uint32)(unsafe.Pointer(b - 4)) / 2
	s := syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(b)), n))
	pSysFreeString.Call(b)
	return s
}

// findSpotifyHwnd: Spotify's main window. Spotify also owns hidden helper
// windows with titles, so prefer the visible one with a title bar, then the largest.
var (
	fsBest             uintptr
	fsBestScore        int64
	pGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
	pGetWindowRect     = user32.NewProc("GetWindowRect")
	fsCB               = syscall.NewCallback(func(h, _ uintptr) uintptr {
		cls := make([]uint16, 64)
		pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&cls[0])), 64)
		if !strings.HasPrefix(syscall.UTF16ToString(cls), "Chrome_WidgetWin") {
			return 1
		}
		t := make([]uint16, 8)
		if n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&t[0])), 8); n == 0 {
			return 1
		}
		var pid uint32
		pGetWindowThreadPid.Call(h, uintptr(unsafe.Pointer(&pid)))
		if processName(pid) != "spotify.exe" {
			return 1
		}
		var score int64 = 1
		if v, _, _ := pIsWindowVisible.Call(h); v != 0 {
			score += 1 << 40
		}
		if st, _, _ := pGetWindowLongPtrW.Call(h, ^uintptr(15)); st&0x00C00000 == 0x00C00000 { // GWL_STYLE has WS_CAPTION
			score += 1 << 39
		}
		if st, _, _ := pGetWindowLongPtrW.Call(h, ^uintptr(15)); st&0x20000000 != 0 { // WS_MINIMIZE
			score += 1 << 38
		}
		var r rect
		pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		if w, hh := int64(r.R-r.L), int64(r.B-r.T); w > 0 && hh > 0 {
			score += w * hh
		}
		if score > fsBestScore {
			fsBest, fsBestScore = h, score
		}
		return 1 // keep looking for a better one
	})
	fsMu sync.Mutex
)

func findSpotifyHwnd() uintptr {
	fsMu.Lock()
	defer fsMu.Unlock()
	fsBest, fsBestScore = 0, 0
	pEnumWindows.Call(fsCB, 0)
	return fsBest
}

// playButtonScore: how well a button label fits the song (0 = not it)
func playButtonScore(label, title, artist string) int {
	l := normText(label)
	if !strings.HasPrefix(l, "play ") {
		return 0
	}
	t := normText(coreTitle(title))
	if t == "" || !strings.Contains(l, t) {
		return 0
	}
	score := 1
	if aw := strings.Fields(normText(artist)); len(aw) > 0 && strings.Contains(l, aw[0]) {
		score++
	}
	if strings.HasPrefix(l, "play "+t) {
		score++
	}
	if l == "play "+t || strings.HasPrefix(l, "play "+t+" by ") {
		score++ // exact title, not "… (Remix)" or "… (Live)"
	}
	return score
}

// isTrackButton: a song row's Play button ("Play <song> by <artist>"), not the playlist's own
func isTrackButton(name, playlist string) bool {
	l := normText(name)
	return strings.HasPrefix(l, "play ") && strings.Contains(l, " by ") && l != "play "+normText(playlist)
}

// pressPlay looks for the song's Play button in Spotify for up to `wait`.
// For a playlist (fromStart), it starts the playlist fresh instead of resuming
// where it was left: the first song, or a random one when shuffle is on.
func pressPlay(title, artist string, wait time.Duration, fromStart bool) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0) // multithreaded
	var ua uintptr
	if r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAuto)), 0, 1|4, // in-proc / local server
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&ua))); r != 0 || ua == 0 {
		logf("autoplay: UI Automation unavailable (0x%x)", r)
		return false
	}
	defer release(ua)

	// condition: buttons only
	v := variant{VT: 3, Val: 50000} // VT_I4, UIA_ButtonControlTypeId
	var cond uintptr
	comCall(ua, 23, 30003, uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&cond))) // CreatePropertyCondition(ControlType)
	if cond == 0 {
		return false
	}
	defer release(cond)

	deadline := time.Now().Add(wait)
	broughtUp := false
	start := time.Now()
	var pageSeen time.Time // when the playlist's own Play button first appeared
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		h := findSpotifyHwnd()
		if h == 0 {
			continue
		}
		// Spotify only draws its page while visible: if Windows kept it in the
		// background (taskbar flashing), bring it forward ourselves
		if fg, _, _ := pGetForegroundWindow.Call(); fg != h && !broughtUp && time.Since(start) > 300*time.Millisecond {
			broughtUp = true
			if r, _, _ := pIsIconic.Call(h); r != 0 {
				pShowWindow.Call(h, 9) // SW_RESTORE
			}
			forceForeground(h)
		}
		var root uintptr
		if comCall(ua, 6, h, uintptr(unsafe.Pointer(&root))) != 0 || root == 0 { // ElementFromHandle
			continue
		}
		var arr uintptr
		comCall(root, 6, 4, cond, uintptr(unsafe.Pointer(&arr))) // FindAll(TreeScope_Descendants)
		release(root)
		if arr == 0 {
			continue
		}
		var n int32
		comCall(arr, 3, uintptr(unsafe.Pointer(&n))) // get_Length
		best, bestScore := uintptr(0), 0
		firstTrack := uintptr(0)
		shuffleOn := false
		for i := int32(0); i < n && i < 2000; i++ {
			var el uintptr
			comCall(arr, 4, uintptr(i), uintptr(unsafe.Pointer(&el))) // GetElement
			if el == 0 {
				continue
			}
			var bname uintptr
			comCall(el, 23, uintptr(unsafe.Pointer(&bname))) // get_CurrentName
			name := takeBSTR(bname)
			if fromStart {
				ln := strings.ToLower(name)
				if strings.Contains(ln, "disable shuffle") || strings.Contains(ln, "disable smart shuffle") {
					shuffleOn = true // the shuffle button offers to turn it off: it's on
				}
				if isTrackButton(name, title) {
					if firstTrack == 0 {
						firstTrack = el // song rows come in playlist order
					} else {
						release(el)
					}
					continue
				}
			}
			if sc := playButtonScore(name, title, artist); sc > bestScore {
				if best != 0 {
					release(best)
				}
				best, bestScore = el, sc
				continue
			}
			release(el)
		}
		release(arr)
		if fromStart && best != 0 {
			if pageSeen.IsZero() {
				pageSeen = time.Now()
			}
			switch {
			case shuffleOn:
				// press the playlist's Play, then skip: Spotify jumps to a random song from its start
				if firstTrack != 0 {
					release(firstTrack)
				}
				if invokeButton(best) {
					go func() { time.Sleep(450 * time.Millisecond); send(VK_MEDIA_NEXT) }()
					logf("autoplay: started playlist on shuffle")
					return true
				}
				continue
			case firstTrack != 0:
				release(best)
				if invokeButton(firstTrack) {
					logf("autoplay: started playlist from the first song")
					return true
				}
				continue
			case time.Since(pageSeen) < 2500*time.Millisecond:
				release(best) // the song list hasn't loaded yet: wait a moment
				continue
			}
			// no song list: fall back to the playlist's own Play button
		} else if firstTrack != 0 {
			release(firstTrack)
		}
		if best != 0 {
			var pat uintptr
			comCall(best, 16, 10000, uintptr(unsafe.Pointer(&pat))) // GetCurrentPattern(Invoke)
			release(best)
			if pat != 0 {
				hr := comCall(pat, 3) // Invoke
				release(pat)
				if hr == 0 {
					logf("autoplay: pressed Play")
					return true
				}
			}
		}
	}
	logf("autoplay: no Play button found")
	return false
}

// playInSpotify: open Spotify's search for the song, press its Play button,
// then hand focus back to whatever you were doing.
func playInSpotify(title, artist string, art []uint32) {
	q := strings.TrimSpace(title + " " + artist)
	startPlay("spotify:search:"+url.PathEscape(q), "https://open.spotify.com/search/"+url.PathEscape(q), title, artist, art, "", "")
}

// playPlaylist: open one of your playlists (by its ID when known) and press its Play button
func playPlaylist(p playlistEntry) {
	target, web := p.URI, ""
	if target == "" {
		target = "spotify:search:" + url.PathEscape(p.Title)
	} else {
		web = "https://open.spotify.com/playlist/" + strings.TrimPrefix(p.URI, "spotify:playlist:")
	}
	startPlay(target, web, p.Title, "", playlistArt(p.URI, p.Title), "playlist", p.URI)
}

func startPlay(target, web, title, artist string, art []uint32, kind, uri string) {
	playTitle, playArtist, playArt, playKind, playURI = title, artist, art, kind, uri
	// remember how Spotify's window was (minimized, in the tray, or open)
	spotPrevHwnd = findSpotifyHwnd()
	spotWasMinimized, spotWasHidden = false, spotPrevHwnd == 0
	if spotPrevHwnd != 0 {
		if r, _, _ := pIsIconic.Call(spotPrevHwnd); r != 0 {
			spotWasMinimized = true
		}
		if r, _, _ := pIsWindowVisible.Call(spotPrevHwnd); r == 0 {
			spotWasHidden = true
		}
	}
	logf("autoplay: spotify window %x minimized=%v hidden=%v", spotPrevHwnd, spotWasMinimized, spotWasHidden)
	pAllowSetForegroundWnd.Call(^uintptr(0)) // let Spotify come to the front (instead of flashing in the taskbar)
	if !shellOpen(target) {
		if web != "" {
			shellOpen(web) // no Spotify app: can't automate the website
		}
		return
	}
	if title == "" {
		return
	}
	playBusy = true
	skipFromTitle = getSnap().title
	// Spotify only draws its page while it's visible, so it has to come to the
	// front for a moment; we switch back the instant Play is pressed.
	go func() {
		defer guard("playInSpotify")
		ok := pressPlay(title, artist, 9*time.Second, kind == "playlist" && uri != "") // allow time for Spotify to start if it was closed
		if ok && (len(plStore.Playlists) == 0 || time.Since(plLastScan) > 10*time.Minute) {
			// Spotify is on screen right now: a good moment to read your playlists
			pPostMessageW.Call(hwnd, WM_PLAYLISTSCAN, 0, 0)
		}
		v := uintptr(0)
		if ok {
			v = 1
		}
		pPostMessageW.Call(hwnd, WM_PLAYED, v, 0)
	}()
}

func onPlayed(ok bool) {
	playBusy = false
	if !ok {
		// couldn't press Play: show Spotify on the song's search so you can
		if h := findSpotifyHwnd(); h != 0 {
			pShowWindow.Call(h, 9) // SW_RESTORE
			forceForeground(h)
		}
		return
	}
	recordSearchPlay(playKind, playTitle, playArtist, playURI, playArt) // searched & played: goes on the recent list
	if srchPrevFg != 0 {
		forceForeground(srchPrevFg) // back to where you were
	}
	// and Spotify back to how it was
	if h := findSpotifyHwnd(); h != 0 {
		switch {
		case spotWasHidden && spotPrevHwnd != 0:
			pShowWindow.Call(h, 0) // was in the tray
		case spotWasMinimized:
			// ask Spotify to minimize itself, like clicking its minimize button
			pPostMessageW.Call(h, 0x0112, 0xF020, 0) // WM_SYSCOMMAND, SC_MINIMIZE
			go func() {
				time.Sleep(400 * time.Millisecond)
				if r, _, _ := pIsIconic.Call(h); r == 0 {
					pShowWindow.Call(h, 7) // SW_SHOWMINNOACTIVE, if it didn't
				}
				r, _, _ := pIsIconic.Call(h)
				logf("autoplay: re-minimized spotify: %v", r != 0)
			}()
		}
	}
	// show the new song once Spotify reports it
	skipPending = actNext
	artWaitStart = 0
	pollFast(2500 * time.Millisecond)
	pSetTimer.Call(hwnd, skipTimer, 1800, 0)
}

const WM_PLAYLISTSCAN = WM_APP + 25

// invokeButton presses a UI Automation button and releases it
func invokeButton(el uintptr) bool {
	var pat uintptr
	comCall(el, 16, 10000, uintptr(unsafe.Pointer(&pat))) // GetCurrentPattern(Invoke)
	release(el)
	if pat == 0 {
		return false
	}
	hr := comCall(pat, 3) // Invoke
	release(pat)
	return hr == 0
}
