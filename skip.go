package main

// Smart skip for Left/Right:
//   1. the app offers next/previous  -> skip to the next/previous item
//   2. YouTube in front, no "next"   -> YouTube's own Shift+N (next video)
//   3. the app allows seeking        -> jump to the end of the video / restart it
//   4. otherwise                     -> plain media key

import (
	"strings"
	"time"
	"unsafe"
)

const (
	WM_SKIP       = WM_APP + 9  // wParam 1 = next, 0 = previous
	WM_SKIPRESULT = WM_APP + 10 // wParam = action, lParam = seconds

	actNone = iota
	actNext
	actPrev
	actFwd
	actBack
	actYouTube
	actKey
	actEnd     // jumped to the end of the video
	actRestart // restarted the video

	skipTimer = 3
)

var (
	pGetForegroundWindow     = user32.NewProc("GetForegroundWindow")
	pGetSystemTimeAsFileTime = kernel32.NewProc("GetSystemTimeAsFileTime")

	// main-thread state while waiting for the new song to show up
	skipPending   int
	skipFromTitle string
)

// ---- main thread ----

var artWaitStart uint64

func onSkip(next bool) {
	defer guard("onSkip")
	s := getSnap()
	if s.valid && s.audioOnly {
		showAudioCard(s, "CAN'T SKIP IN "+strings.ToUpper(s.title), s.title, "Pause mutes · Shift ↑↓ volume")
		return
	}
	if !s.valid {
		if next {
			send(VK_MEDIA_NEXT)
		} else {
			send(VK_MEDIA_PREV)
		}
		return
	}
	// instant feedback: the current song, labelled "skipping"; the new one slides in when it loads
	act, label := actNext, "SKIPPING"
	if !next {
		act, label = actPrev, "GOING BACK"
	}
	skipFromTitle, skipPending, artWaitStart = s.title, act, 0
	showSkip(act, label, s)
	if next {
		queueCmd(cmdNext)
	} else {
		queueCmd(cmdPrev)
	}
	pollFast(2500 * time.Millisecond)
	pSetTimer.Call(hwnd, skipTimer, 1600, 0) // in case nothing changes
}

func onSkipResult(act, secs int) {
	defer guard("onSkipResult")
	if act == actRestart && skipPending != 0 {
		skipPending = 0
		pKillTimer.Call(hwnd, skipTimer)
		showSkip(actPrev, "RESTARTED", getSnap())
	}
}

// called when the media watcher sees a change; true = handled here
func checkSkipUpdate() bool {
	if skipPending == 0 {
		return false
	}
	s := getSnap()
	if !s.valid || s.title == skipFromTitle {
		return true // still the old song
	}
	if s.art == nil && !s.audioOnly {
		// new song is here but its cover isn't yet: wait a moment so the card arrives complete
		if artWaitStart == 0 {
			artWaitStart = tick()
			pSetTimer.Call(hwnd, skipTimer, 450, 0)
		}
		return true
	}
	finishSkip(s)
	return true
}

func onSkipTimer() {
	pKillTimer.Call(hwnd, skipTimer)
	if skipPending == 0 {
		return
	}
	s := getSnap()
	switch {
	case s.title != skipFromTitle:
		finishSkip(s) // without cover art; it fades in when it arrives
	case skipPending == actPrev:
		skipPending = 0
		showSkip(actPrev, "RESTARTED", s) // e.g. Spotify restarts the song when you're past the start
	default:
		skipPending = 0
		ppMedia = true
		showMedia(s.status == statusPaused, s)
	}
}

func finishSkip(s mediaSnap) {
	dir := transNext
	if skipPending == actPrev {
		dir = transPrev
	}
	skipPending = 0
	pKillTimer.Call(hwnd, skipTimer)
	ppMedia = true // lets late cover art fade in
	pendingTrans = dir
	showMedia(s.status == statusPaused, s)
}

// ---- media watcher thread ----

func comBool(obj uintptr, idx int) bool {
	var b uint32
	if comCall(obj, idx, uintptr(unsafe.Pointer(&b))) != 0 {
		return false
	}
	return b&0xFF != 0
}

func comInt64(obj uintptr, idx int) int64 {
	var v int64
	comCall(obj, idx, uintptr(unsafe.Pointer(&v)))
	return v
}

func nowFileTime() int64 {
	var ft int64
	pGetSystemTimeAsFileTime.Call(uintptr(unsafe.Pointer(&ft)))
	return ft
}

func youtubeInFront() bool {
	h, _, _ := pGetForegroundWindow.Call()
	if h == 0 {
		return false
	}
	t := make([]uint16, 512)
	n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&t[0])), 512)
	if n == 0 || !strings.Contains(syscallUTF16(t[:n]), "YouTube") {
		return false
	}
	var pid uint32
	pGetWindowThreadPid.Call(h, uintptr(unsafe.Pointer(&pid)))
	switch processName(pid) {
	case "chrome.exe", "msedge.exe", "firefox.exe", "brave.exe", "opera.exe", "vivaldi.exe":
		return true
	}
	return false
}

func sendShiftN() {
	pKeybdEvent.Call(0x10, 0, 0, 0) // Shift down
	pKeybdEvent.Call(0x4E, 0, 0, 0) // N
	pKeybdEvent.Call(0x4E, 0, KEYEVENTF_KEYUP, 0)
	pKeybdEvent.Call(0x10, 0, KEYEVENTF_KEYUP, 0)
}

func handleSkip(w *watcher, next bool) {
	defer guard("handleSkip")
	result := func(act, secs int) { pPostMessageW.Call(hwnd, WM_SKIPRESULT, uintptr(act), uintptr(secs)) }
	mediaKey := func() {
		if next {
			send(VK_MEDIA_NEXT)
		} else {
			send(VK_MEDIA_PREV)
		}
		result(actKey, 0)
	}

	sess := w.pick()
	if sess == 0 {
		mediaKey()
		return
	}
	defer release(sess)

	var nextOK, prevOK, seekOK bool
	if pi := comOut(sess, 9); pi != 0 { // GetPlaybackInfo
		if c := comOut(pi, 6); c != 0 { // Controls
			nextOK, prevOK, seekOK = comBool(c, 12), comBool(c, 13), comBool(c, 20)
			release(c)
		}
		release(pi)
	}

	if next && nextOK {
		op := comOut(sess, 16) // TrySkipNextAsync
		await(op, time.Second)
		release(op)
		result(actNext, 0)
		return
	}
	if !next && prevOK {
		op := comOut(sess, 17) // TrySkipPreviousAsync
		await(op, time.Second)
		release(op)
		result(actPrev, 0)
		return
	}
	if next && youtubeInFront() {
		sendShiftN()
		result(actYouTube, 0)
		return
	}
	if seekOK {
		// no next/previous item: skip the whole video (jump to its end), or restart it
		if tl := comOut(sess, 8); tl != 0 { // GetTimelineProperties
			end := comInt64(tl, 7)
			release(tl)
			np := int64(0)
			act := actRestart
			if next {
				if end <= 0 {
					mediaKey()
					return
				}
				np, act = end, actEnd
			}
			op := comOut1(sess, 24, uintptr(np)) // TryChangePlaybackPositionAsync
			await(op, time.Second)
			release(op)
			result(act, 0)
			return
		}
	}
	mediaKey()
}

// like comOut, for methods that take one argument before the out pointer
func comOut1(obj uintptr, idx int, arg uintptr) uintptr {
	var r uintptr
	if comCall(obj, idx, arg, uintptr(unsafe.Pointer(&r))) != 0 {
		return 0
	}
	return r
}
