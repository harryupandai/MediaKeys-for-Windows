package main

// Shift + Up/Down: change the volume of whichever app is playing media
// (the same app the Pause key controls), via its Volume Mixer session.

import (
	"math"
	"path/filepath"
	"strings"
	"unsafe"
)

var pGetAppUserModelId = kernel32.NewProc("GetApplicationUserModelId")

type audioSess struct {
	ctl   uintptr // IAudioSessionControl (released by caller)
	pid   uint32
	exe   string
	aumid string
	peak  float32
}

func processAUMID(pid uint32) string {
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	if r, _, _ := pGetAppUserModelId.Call(h, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return ""
	}
	return strings.ToLower(syscallUTF16(buf))
}

func syscallUTF16(b []uint16) string {
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	r := make([]rune, 0, len(b))
	for _, c := range b {
		r = append(r, rune(c))
	}
	return string(r)
}

// exeHint guesses the process name behind a media app ID.
func exeHint(aumid string) string {
	a := strings.ToLower(aumid)
	switch {
	case a == "":
		return ""
	case strings.HasSuffix(a, ".exe"):
		return filepath.Base(strings.ReplaceAll(a, `\`, "/"))
	case strings.Contains(a, "spotify"):
		return "spotify.exe"
	case strings.Contains(a, "msedge") || a == "microsoftedge":
		return "msedge.exe"
	case strings.Contains(a, "chrome"):
		return "chrome.exe"
	case strings.Contains(a, "firefox") || a == "308046b0af4a39cb":
		return "firefox.exe"
	case strings.Contains(a, "brave"):
		return "brave.exe"
	case strings.Contains(a, "opera"):
		return "opera.exe"
	case strings.Contains(a, "vivaldi"):
		return "vivaldi.exe"
	case !strings.ContainsAny(a, `.!\/ `):
		return a + ".exe"
	}
	return ""
}

func friendlyName(exe string) string {
	names := map[string]string{
		"spotify.exe": "Spotify", "chrome.exe": "Chrome", "msedge.exe": "Edge", "firefox.exe": "Firefox",
		"brave.exe": "Brave", "opera.exe": "Opera", "vivaldi.exe": "Vivaldi", "vlc.exe": "VLC",
		"discord.exe": "Discord", "music.ui.exe": "Media Player", "microsoft.media.player.exe": "Media Player",
	}
	if n, ok := names[exe]; ok {
		return n
	}
	n := strings.TrimSuffix(exe, ".exe")
	if n == "" {
		return "App"
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

// listSessions returns every app audio session on the default speakers.
func listSessions() (sessions []audioSess, cleanup func()) {
	var objs []uintptr
	cleanup = func() {
		for i := len(objs) - 1; i >= 0; i-- {
			release(objs[i])
		}
	}
	var enum uintptr
	if r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); r != 0 || enum == 0 {
		return
	}
	objs = append(objs, enum)
	var device, mgr, list uintptr
	if comCall(enum, 4, 0, 1, uintptr(unsafe.Pointer(&device))) != 0 || device == 0 {
		return
	}
	objs = append(objs, device)
	if comCall(device, 3, uintptr(unsafe.Pointer(&iidIAudioSessionManager2)), clsctxAll, 0, uintptr(unsafe.Pointer(&mgr))) != 0 || mgr == 0 {
		return
	}
	objs = append(objs, mgr)
	if comCall(mgr, 5, uintptr(unsafe.Pointer(&list))) != 0 || list == 0 {
		return
	}
	objs = append(objs, list)
	var count int32
	comCall(list, 3, uintptr(unsafe.Pointer(&count)))
	for i := int32(0); i < count; i++ {
		var ctl uintptr
		if comCall(list, 4, uintptr(i), uintptr(unsafe.Pointer(&ctl))) != 0 || ctl == 0 {
			continue
		}
		objs = append(objs, ctl)
		ctl2 := qi(ctl, &iidIAudioSessionControl2)
		if ctl2 == 0 {
			continue
		}
		var pid uint32
		comCall(ctl2, 14, uintptr(unsafe.Pointer(&pid)))
		system := comCall(ctl2, 15) == 0 // IsSystemSoundsSession: S_OK means yes
		release(ctl2)
		if pid == 0 || system {
			continue
		}
		s := audioSess{ctl: ctl, pid: pid, exe: processName(pid)}
		if m := qi(ctl, &iidIAudioMeterInformation); m != 0 {
			comCall(m, 3, uintptr(unsafe.Pointer(&s.peak)))
			release(m)
		}
		sessions = append(sessions, s)
	}
	return
}

// changeMediaVolume nudges the playing app's volume by delta (0..1 scale).
// Returns the new level 0-100 and the app's display name, or -1 if nothing fits.
func changeMediaVolume(delta float32) (pct int, name string, fromMedia bool) {
	sessions, cleanup := listSessions()
	defer cleanup()

	var targets []audioSess
	if s := getSnap(); s.valid && s.audioOnly { // a Volume Mixer app picked with Shift+Left/Right
		for _, a := range sessions {
			if a.exe == s.exe {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			return -1, "", false
		}
		pct, _, _ = setLevel(targets, delta)
		return pct, s.title, pct >= 0
	} else if s.valid && s.app != "" {
		app := strings.ToLower(s.app)
		if strings.Contains(app, "!") { // Store app: match its exact app ID
			for _, a := range sessions {
				if processAUMID(a.pid) == app {
					targets = append(targets, a)
				}
			}
		}
		if len(targets) == 0 {
			if hint := exeHint(app); hint != "" {
				for _, a := range sessions {
					if a.exe == hint {
						targets = append(targets, a)
					}
				}
			}
		}
		fromMedia = len(targets) > 0
	}
	if len(targets) == 0 { // fall back to whatever is making sound right now
		var best *audioSess
		for i := range sessions {
			if sessions[i].peak > 0.0005 && (best == nil || sessions[i].peak > best.peak) {
				best = &sessions[i]
			}
		}
		if best != nil {
			for _, a := range sessions {
				if a.exe == best.exe {
					targets = append(targets, a)
				}
			}
		}
	}
	if len(targets) == 0 {
		return -1, "", false
	}

	pct, _, _ = setLevel(targets, delta)
	if pct < 0 {
		return -1, "", false
	}
	return pct, friendlyName(targets[0].exe), fromMedia
}

// setLevel gives all of an app's sessions one new level, based on the loudest one's setting
func setLevel(targets []audioSess, delta float32) (int, string, bool) {
	var cur float32 = -1
	vols := make([]uintptr, 0, len(targets))
	for _, t := range targets {
		if v := qi(t.ctl, &iidISimpleAudioVolume); v != 0 {
			vols = append(vols, v)
			var lvl float32
			comCall(v, 4, uintptr(unsafe.Pointer(&lvl)))
			if lvl > cur {
				cur = lvl
			}
		}
	}
	defer func() {
		for _, v := range vols {
			release(v)
		}
	}()
	if len(vols) == 0 {
		return -1, "", false
	}
	next := float32(math.Round(float64(cur+delta)*100) / 100)
	if next < 0 {
		next = 0
	} else if next > 1 {
		next = 1
	}
	for _, v := range vols {
		comCall(v, 3, uintptr(math.Float32bits(next)), 0) // SetMasterVolume
		if delta > 0 {
			comCall(v, 5, 0, 0) // unmute when turning up
		}
	}
	return int(math.Round(float64(next) * 100)), "", true
}
