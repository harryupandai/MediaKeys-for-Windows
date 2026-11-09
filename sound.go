package main

// Plays the chosen typing sound (see clicksynth.go) for each character typed
// in the search bar, if "Typing sounds" is on.

import (
	"syscall"
	"unsafe"
)

// the typing sound, designed in the Click Sound Editor
var typingClick = ClickParams{Pitch: 10, Length: 0.5, Brightness: 0, Body: 0.9, Attack: 0.3, Ring: 0.4,
	Release: true, RelLevel: 0.58, RelDelay: 86, Volume: 0.14, Variation: 0.02}

var (
	winmm       = syscall.NewLazyDLL("winmm.dll")
	pPlaySoundW = winmm.NewProc("PlaySoundW")

	clicks    [][]byte // WAV files in memory (kept alive while playing)
	clickNext int
)

func initClicks() {
	for v := 0; v < 5; v++ { // five slightly different presses, rotated
		clicks = append(clicks, toWAV(renderCustomClick(typingClick, v)))
	}
}

func playClick() {
	if !settings.TypingSounds {
		return
	}
	if clicks == nil {
		initClicks()
	}
	c := clicks[clickNext%len(clicks)]
	clickNext++
	const sndAsync, sndNoDefault, sndMemory = 0x1, 0x2, 0x4
	pPlaySoundW.Call(uintptr(unsafe.Pointer(&c[0])), 0, sndAsync|sndNoDefault|sndMemory)
}
