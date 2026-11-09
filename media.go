package main

// Working out whether a play/pause press paused or resumed, and what song.

import (
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	pEnumWindows        = user32.NewProc("EnumWindows")
	pGetClassNameW      = user32.NewProc("GetClassNameW")
	pGetWindowTextW     = user32.NewProc("GetWindowTextW")
	pGetWindowThreadPid = user32.NewProc("GetWindowThreadProcessId")

	iidIAudioMeterInformation = guid{0xC02216F6, 0x8C67, 0x4B5B, [8]byte{0x9D, 0x00, 0xD0, 0x08, 0xE7, 0x3E, 0x00, 0x64}}

	pIsWindow = user32.NewProc("IsWindow")

	spotifyWnd uintptr // remembered so later checks are instant
	enumResult string
	enumFound  bool
	enumCB     = syscall.NewCallback(func(h, _ uintptr) uintptr {
		cls := make([]uint16, 64)
		pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&cls[0])), 64)
		if !strings.HasPrefix(syscall.UTF16ToString(cls), "Chrome_WidgetWin") {
			return 1
		}
		t := make([]uint16, 512)
		n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&t[0])), 512)
		if n == 0 {
			return 1
		}
		var pid uint32
		pGetWindowThreadPid.Call(h, uintptr(unsafe.Pointer(&pid)))
		if processName(pid) != "spotify.exe" {
			return 1
		}
		enumResult, enumFound = syscall.UTF16ToString(t[:n]), true
		spotifyWnd = h
		return 0 // stop
	})
)

// spotifyTitle returns Spotify's window title: "Artist - Song" while playing,
// "Spotify" / "Spotify Premium" / "Spotify Free" while paused.
func spotifyTitle() (string, bool) {
	if spotifyWnd != 0 {
		if ok, _, _ := pIsWindow.Call(spotifyWnd); ok != 0 {
			t := make([]uint16, 512)
			if n, _, _ := pGetWindowTextW.Call(spotifyWnd, uintptr(unsafe.Pointer(&t[0])), 512); n > 0 {
				return syscall.UTF16ToString(t[:n]), true
			}
		}
		spotifyWnd = 0
	}
	enumResult, enumFound = "", false
	pEnumWindows.Call(enumCB, 0)
	return enumResult, enumFound
}

func spotifyPausedTitle(t string) bool {
	return t == "Spotify" || strings.HasPrefix(t, "Spotify ")
}

// devicePeak is the current output level of the speakers (0 = silent).
func devicePeak() float32 {
	var enum uintptr
	if r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); r != 0 || enum == 0 {
		return 0
	}
	defer release(enum)
	var device uintptr
	if comCall(enum, 4, 0, 1, uintptr(unsafe.Pointer(&device))) != 0 || device == 0 {
		return 0
	}
	defer release(device)
	var meter uintptr
	if comCall(device, 3, uintptr(unsafe.Pointer(&iidIAudioMeterInformation)), clsctxAll, 0, uintptr(unsafe.Pointer(&meter))) != 0 || meter == 0 {
		return 0
	}
	defer release(meter)
	var peak float32
	comCall(meter, 3, uintptr(unsafe.Pointer(&peak)))
	return peak
}

// ---- play/pause flow ----
// The popup appears instantly using a prediction (Spotify playing -> we're pausing),
// then a quick follow-up check corrects it if the press went somewhere else.

var (
	ppBefore    string
	ppFound     bool
	ppSpotify   bool // prediction was based on Spotify
	ppPredPause bool
	ppChecks    int
	lastSong    string
)

const (
	ppTimer    = 2
	ppInterval = 60 // ms between follow-up checks
	ppMaxCheck = 16 // ~1 second
)

// media-session path: Windows tells us exactly what's playing
var ppMedia bool

func onPlayPause() {
	defer guard("onPlayPause")
	// Volume Mixer app (Discord, a game...): Pause mutes / unmutes it
	if s := getSnap(); s.valid && s.audioOnly {
		ppMedia = false
		if muted, ok := appMute(s.exe, true); ok {
			s.muted = muted
			if muted {
				showAudioCard(s, "MUTED", s.title, "Press Pause again to unmute")
			} else {
				showAudioCard(s, "UNMUTED", s.title, "")
			}
		}
		pollFast(800 * time.Millisecond)
		return
	}
	// draw the popup first, then play/pause
	if s := getSnap(); s.valid && (s.status == statusPlaying || s.status == statusPaused) {
		ppMedia = true
		pKillTimer.Call(hwnd, ppTimer)
		showMedia(s.status == statusPlaying, s) // it was playing, so this press pauses it
		queueCmd(cmdToggle)                     // acts on the app picked with Shift+Left/Right
		pollFast(1500 * time.Millisecond)
		return
	}
	ppMedia = false
	defer send(VK_MEDIA_PLAY_PAUSE)

	ppBefore, ppFound = spotifyTitle()
	if ppFound {
		ppSpotify = true
		if spotifyPausedTitle(ppBefore) {
			ppPredPause = false
			showPlayback(false, lastSong, true)
		} else {
			lastSong = ppBefore
			ppPredPause = true
			showPlayback(true, ppBefore, true)
		}
	} else {
		ppSpotify = false
		ppPredPause = devicePeak() >= 0.0008 // sound was playing, so this press pauses it
		showPlayback(ppPredPause, "", false)
	}
	ppChecks = 0
	pSetTimer.Call(hwnd, ppTimer, ppInterval, 0)
}

// the media watcher saw a change: correct / fill in the popup if it's showing
func onMediaUpdate() {
	defer guard("onMediaUpdate")
	if checkSkipUpdate() {
		return
	}
	if osdAction != actNone && osdAction != actAudio && animOn {
		// a skip card is showing: just fill in late cover art
		if s := getSnap(); s.valid && s.title == osdMediaTitle && s.artVersion != osdArtVersion && s.art != nil {
			osdArt, osdArtN, osdArtVersion = s.art, s.artN, s.artVersion
			baseDirty = true
		}
		return
	}
	if !ppMedia || !animOn || osdMode != modePlayback || !osdFromMedia {
		return
	}
	s := getSnap()
	if !s.valid || (s.status != statusPlaying && s.status != statusPaused) {
		return
	}
	paused := s.status == statusPaused
	if paused != osdPaused || s.title != osdMediaTitle || s.artVersion != osdArtVersion || (s.art == nil) != (osdArt == nil) {
		showMedia(paused, s)
	}
}

func resolvePlayPause() {
	defer guard("resolvePlayPause")
	ppChecks++
	if ppSpotify {
		after, found := spotifyTitle()
		if found && spotifyPausedTitle(after) != spotifyPausedTitle(ppBefore) {
			pKillTimer.Call(hwnd, ppTimer)
			if !spotifyPausedTitle(after) {
				lastSong = after
				if osdSub != after || osdPaused { // fill in / correct the song name
					showPlayback(false, after, true)
				}
			}
			return
		}
		if ppChecks < ppMaxCheck {
			return // keep waiting for Spotify to update
		}
		// Spotify never changed: the press went to another app
		pKillTimer.Call(hwnd, ppTimer)
		showPlayback(devicePeak() < 0.0008, "", false)
		return
	}
	// non-Spotify: one check after ~350 ms to confirm
	if ppChecks*ppInterval < 350 {
		return
	}
	pKillTimer.Call(hwnd, ppTimer)
	if actual := devicePeak() < 0.0008; actual != ppPredPause {
		showPlayback(actual, "", false)
	}
}
