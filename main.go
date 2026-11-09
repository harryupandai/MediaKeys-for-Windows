// MediaKeys: system-wide media keys for Windows.
//
//	Pause        -> play / pause
//	Left / Right -> previous / next track
//	Up / Down    -> volume up / down
//
// Scroll Lock (or the tray menu) turns the arrow keys off/on so they can be used normally.
package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pSetWindowsHookExW = user32.NewProc("SetWindowsHookExW")
	pCallNextHookEx    = user32.NewProc("CallNextHookEx")
	pUnhookWindowsHook = user32.NewProc("UnhookWindowsHookEx")
	pGetMessageW       = user32.NewProc("GetMessageW")
	pTranslateMessage  = user32.NewProc("TranslateMessage")
	pDispatchMessageW  = user32.NewProc("DispatchMessageW")
	pKeybdEvent        = user32.NewProc("keybd_event")
	pRegisterClassExW  = user32.NewProc("RegisterClassExW")
	pCreateWindowExW   = user32.NewProc("CreateWindowExW")
	pDefWindowProcW    = user32.NewProc("DefWindowProcW")
	pPostMessageW      = user32.NewProc("PostMessageW")
	pPostQuitMessage   = user32.NewProc("PostQuitMessage")
	pLoadIconW         = user32.NewProc("LoadIconW")
	pCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	pAppendMenuW       = user32.NewProc("AppendMenuW")
	pTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	pDestroyMenu       = user32.NewProc("DestroyMenu")
	pGetCursorPos      = user32.NewProc("GetCursorPos")
	pSetForegroundWnd  = user32.NewProc("SetForegroundWindow")
	pMessageBoxW       = user32.NewProc("MessageBoxW")
	pGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW      = kernel32.NewProc("CreateMutexW")
	pShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	pRegOpenKeyExW     = advapi32.NewProc("RegOpenKeyExW")
	pRegSetValueExW    = advapi32.NewProc("RegSetValueExW")
	pRegQueryValueExW  = advapi32.NewProc("RegQueryValueExW")
	pRegDeleteValueW   = advapi32.NewProc("RegDeleteValueW")
	pRegCloseKey       = advapi32.NewProc("RegCloseKey")
)

const (
	WH_KEYBOARD_LL = 13
	WM_KEYDOWN     = 0x100
	WM_KEYUP       = 0x101
	WM_SYSKEYDOWN  = 0x104
	WM_SYSKEYUP    = 0x105
	WM_DESTROY     = 0x2
	WM_COMMAND     = 0x111
	WM_LBUTTONUP   = 0x202
	WM_RBUTTONUP   = 0x205
	WM_APP         = 0x8000
	WM_TRAY        = WM_APP + 1
	WM_REFRESH     = WM_APP + 2
	WM_SPOTIFY     = WM_APP + 3
	WM_PLAYPAUSE   = WM_APP + 4
	WM_SWITCHAPP   = WM_APP + 12 // wParam 1 = next app, 0 = previous
	WM_TIMER       = 0x113
	spotifyStep    = 0.05

	LLKHF_INJECTED        = 0x10
	KEYEVENTF_EXTENDEDKEY = 0x1
	KEYEVENTF_KEYUP       = 0x2

	VK_PAUSE  = 0x13
	VK_LEFT   = 0x25
	VK_UP     = 0x26
	VK_RIGHT  = 0x27
	VK_DOWN   = 0x28
	VK_SCROLL = 0x91

	VK_VOLUME_DOWN      = 0xAE
	VK_VOLUME_UP        = 0xAF
	VK_MEDIA_NEXT       = 0xB0
	VK_MEDIA_PREV       = 0xB1
	VK_MEDIA_PLAY_PAUSE = 0xB3

	NIM_ADD     = 0
	NIM_MODIFY  = 1
	NIM_DELETE  = 2
	NIF_MESSAGE = 0x1
	NIF_ICON    = 0x2
	NIF_TIP     = 0x4
	NIF_INFO    = 0x10

	MF_STRING       = 0x0
	MF_CHECKED      = 0x8
	MF_SEPARATOR    = 0x800
	MF_GRAYED       = 0x1
	TPM_RETURNCMD   = 0x100
	TPM_RIGHTBUTTON = 0x2

	ID_ARROWS  = 1
	ID_STARTUP = 2
	ID_QUIT    = 3

	appVersion  = "1.0.0"
	appBuild    = 33
	WM_HELLO    = WM_APP + 20 // another copy was opened: wParam = its build
	ID_SETTINGS = 5
	ID_CARDS    = 6
	ID_TYPING   = 7

	HKEY_CURRENT_USER = 0x80000001
	KEY_ALL_ACCESS    = 0xF003F
	REG_SZ            = 1
	runKey            = `Software\Microsoft\Windows\CurrentVersion\Run`
	appName           = "MediaKeys"
)

type kbdllhookstruct struct {
	VkCode, ScanCode, Flags, Time uint32
	ExtraInfo                     uintptr
}

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndclassex struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type notifyicondata struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        [16]byte
	BalloonIcon     uintptr
}

var (
	hook     uintptr
	hwnd     uintptr
	arrowsOn = true
	held     = map[uint32]bool{}
	nid      notifyicondata
)

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func copyU16(dst []uint16, s string) {
	src, _ := syscall.UTF16FromString(s)
	if len(src) > len(dst) {
		src = src[:len(dst)]
		src[len(src)-1] = 0
	}
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, src)
}

// Shift is tracked here from real key events. Many keyboards (NumLock on) send a
// fake "Shift up" right before an arrow key when Shift is held, which fools
// GetAsyncKeyState. Those fake events carry the extended-key flag, so we skip them.
var lShift, rShift bool

func shiftHeld() bool { return lShift || rShift }

func trackShift(k *kbdllhookstruct, down, up bool) {
	if k.Flags&0x01 != 0 && k.VkCode == 0xA0 { // LLKHF_EXTENDED on left shift = fake shift
		return
	}
	switch k.VkCode {
	case 0xA0, 0x10: // VK_LSHIFT (or generic shift)
		if down {
			lShift = true
		} else if up {
			lShift = false
		}
	case 0xA1: // VK_RSHIFT
		if down {
			rShift = true
		} else if up {
			rShift = false
		}
	}
}

// Ctrl and Alt, tracked so Ctrl+Alt+X works but AltGr+X (used to type characters
// on many European keyboards) doesn't: AltGr sends Right Alt plus a fake Left Ctrl.
var lCtrl, rCtrl, lAlt bool

func trackCtrlAlt(k *kbdllhookstruct, down, up bool) {
	set := func(v *bool) {
		if down {
			*v = true
		} else if up {
			*v = false
		}
	}
	switch k.VkCode {
	case 0xA2: // VK_LCONTROL
		if k.ScanCode&0x200 == 0 { // 0x21D = the fake Ctrl that comes with AltGr
			set(&lCtrl)
		} else if up {
			lCtrl = false
		}
	case 0xA3: // VK_RCONTROL
		set(&rCtrl)
	case 0xA4: // VK_LMENU (left Alt only; right Alt may be AltGr)
		set(&lAlt)
	}
}

func searchHotkeyHeld() bool { return (lCtrl || rCtrl) && lAlt && !shiftHeld() }

var pGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")

func send(vk uintptr) {
	pKeybdEvent.Call(vk, 0, KEYEVENTF_EXTENDEDKEY, 0)
	pKeybdEvent.Call(vk, 0, KEYEVENTF_EXTENDEDKEY|KEYEVENTF_KEYUP, 0)
}

func mapKey(vk uint32) uintptr {
	if !arrowsOn {
		return 0
	}
	switch vk {
	case VK_LEFT:
		return VK_MEDIA_PREV
	case VK_RIGHT:
		return VK_MEDIA_NEXT
	case VK_UP:
		return VK_VOLUME_UP
	case VK_DOWN:
		return VK_VOLUME_DOWN
	}
	return 0
}

func hookProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 {
		k := (*kbdllhookstruct)(unsafe.Pointer(lParam))
		if k.Flags&LLKHF_INJECTED == 0 {
			down := wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN
			up := wParam == WM_KEYUP || wParam == WM_SYSKEYUP
			trackShift(k, down, up)
			trackCtrlAlt(k, down, up)
			if searchActive && k.VkCode >= VK_LEFT && k.VkCode <= VK_DOWN {
				// the search bar is open: arrow keys move through its list
				r, _, _ := pCallNextHookEx.Call(hook, uintptr(nCode), wParam, lParam)
				return r
			}
			if k.VkCode == 0x58 && (down || up) && (held[0x58] || (down && searchHotkeyHeld())) { // Ctrl+Alt+X
				if down && !held[0x58] {
					held[0x58] = true
					pKeybdEvent.Call(0xE8, 0, 0, 0) // harmless key, so releasing Alt doesn't open the app's menu bar
					pKeybdEvent.Call(0xE8, 0, KEYEVENTF_KEYUP, 0)
					pPostMessageW.Call(hwnd, WM_SEARCH, 0, 0)
				} else if up {
					delete(held, 0x58)
				}
				return 1
			}
			if k.VkCode == VK_SCROLL {
				if down && !held[VK_SCROLL] {
					held[VK_SCROLL] = true
					arrowsOn = !arrowsOn
					pPostMessageW.Call(hwnd, WM_REFRESH, 1, 0)
				} else if up {
					delete(held, VK_SCROLL)
				}
			} else if k.VkCode == VK_PAUSE {
				if down {
					if !held[VK_PAUSE] {
						held[VK_PAUSE] = true
						pPostMessageW.Call(hwnd, WM_PLAYPAUSE, 0, 0)
					}
					return 1
				}
				if up && held[VK_PAUSE] {
					delete(held, VK_PAUSE)
					return 1
				}
			} else if down && arrowsOn && (k.VkCode == VK_UP || k.VkCode == VK_DOWN) && shiftHeld() {
				// Shift + Up/Down: Spotify-only volume (done on the main thread, not inside the hook)
				held[k.VkCode] = true
				dir := uintptr(1)
				if k.VkCode == VK_DOWN {
					dir = 0
				}
				pPostMessageW.Call(hwnd, WM_SPOTIFY, dir, 0)
				return 1
			} else if down && arrowsOn && (k.VkCode == VK_LEFT || k.VkCode == VK_RIGHT) && shiftHeld() {
				// Shift + Left/Right: switch which app the keys control
				if !held[k.VkCode] {
					dir := uintptr(1)
					if k.VkCode == VK_LEFT {
						dir = 0
					}
					pPostMessageW.Call(hwnd, WM_SWITCHAPP, dir, 0)
				}
				held[k.VkCode] = true
				return 1
			} else if down && arrowsOn && (k.VkCode == VK_LEFT || k.VkCode == VK_RIGHT) {
				// Left/Right: smart skip, worked out on the main thread
				if !held[k.VkCode] {
					dir := uintptr(1)
					if k.VkCode == VK_LEFT {
						dir = 0
					}
					pPostMessageW.Call(hwnd, WM_SKIP, dir, 0)
				}
				held[k.VkCode] = true
				return 1
			} else if down {
				if m := mapKey(k.VkCode); m != 0 {
					repeat := held[k.VkCode]
					held[k.VkCode] = true
					// holding up/down keeps changing volume; other keys fire once per press
					if !repeat || m == VK_VOLUME_UP || m == VK_VOLUME_DOWN {
						send(m)
					}
					return 1
				}
			} else if up && held[k.VkCode] {
				delete(held, k.VkCode)
				return 1
			}
		}
	}
	r, _, _ := pCallNextHookEx.Call(hook, uintptr(nCode), wParam, lParam)
	return r
}

// ---- start with Windows (HKCU Run key) ----

func startupEnabled() bool {
	var key uintptr
	if r, _, _ := pRegOpenKeyExW.Call(HKEY_CURRENT_USER, uintptr(unsafe.Pointer(u16(runKey))), 0, KEY_ALL_ACCESS, uintptr(unsafe.Pointer(&key))); r != 0 {
		return false
	}
	defer pRegCloseKey.Call(key)
	r, _, _ := pRegQueryValueExW.Call(key, uintptr(unsafe.Pointer(u16(appName))), 0, 0, 0, 0)
	return r == 0
}

func setStartup(on bool) {
	var key uintptr
	if r, _, _ := pRegOpenKeyExW.Call(HKEY_CURRENT_USER, uintptr(unsafe.Pointer(u16(runKey))), 0, KEY_ALL_ACCESS, uintptr(unsafe.Pointer(&key))); r != 0 {
		return
	}
	defer pRegCloseKey.Call(key)
	if on {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		val, _ := syscall.UTF16FromString(`"` + exe + `" --startup`)
		pRegSetValueExW.Call(key, uintptr(unsafe.Pointer(u16(appName))), 0, REG_SZ, uintptr(unsafe.Pointer(&val[0])), uintptr(len(val)*2))
	} else {
		pRegDeleteValueW.Call(key, uintptr(unsafe.Pointer(u16(appName))))
	}
}

// ---- tray icon ----

func tipText() string {
	if arrowsOn {
		return "MediaKeys - arrow keys on"
	}
	return "MediaKeys - arrow keys off (Scroll Lock to turn on)"
}

func refreshTray(balloon bool) {
	nid.Flags = NIF_TIP
	copyU16(nid.Tip[:], tipText())
	if balloon {
		nid.Flags |= NIF_INFO
		copyU16(nid.InfoTitle[:], "MediaKeys")
		if arrowsOn {
			copyU16(nid.Info[:], "Arrow keys now control your music and video.")
		} else {
			copyU16(nid.Info[:], "Arrow keys are back to normal.")
		}
	}
	pShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

func showMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	check := func(on bool) uintptr {
		if on {
			return MF_STRING | MF_CHECKED
		}
		return MF_STRING
	}
	pAppendMenuW.Call(menu, MF_STRING, ID_SETTINGS, uintptr(unsafe.Pointer(u16("Controls && settings…"))))
	user32.NewProc("SetMenuDefaultItem").Call(menu, ID_SETTINGS, 0)
	pAppendMenuW.Call(menu, MF_SEPARATOR, 0, 0)
	pAppendMenuW.Call(menu, check(arrowsOn), ID_ARROWS, uintptr(unsafe.Pointer(u16("Arrow keys control media\tScroll Lock"))))
	pAppendMenuW.Call(menu, check(startupEnabled()), ID_STARTUP, uintptr(unsafe.Pointer(u16("Start with Windows"))))
	pAppendMenuW.Call(menu, check(settings.ShowCards), ID_CARDS, uintptr(unsafe.Pointer(u16("Show on-screen cards"))))
	pAppendMenuW.Call(menu, check(settings.TypingSounds), ID_TYPING, uintptr(unsafe.Pointer(u16("Typing sounds"))))
	pAppendMenuW.Call(menu, MF_SEPARATOR, 0, 0)
	pAppendMenuW.Call(menu, MF_STRING, ID_QUIT, uintptr(unsafe.Pointer(u16("Quit MediaKeys"))))

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWnd.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, TPM_RETURNCMD|TPM_RIGHTBUTTON, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	pPostMessageW.Call(hwnd, 0, 0, 0)
	pDestroyMenu.Call(menu)

	switch cmd {
	case ID_SETTINGS:
		openSettings(false)
	case ID_ARROWS:
		setArrows(!arrowsOn, true)
	case ID_STARTUP:
		setStartup(!startupEnabled())
		refreshSettingsWindow()
	case ID_CARDS:
		setShowCards(!settings.ShowCards)
	case ID_TYPING:
		setTypingSounds(!settings.TypingSounds)
	case ID_QUIT:
		quit()
	}
}

func quit() {
	if setWnd != 0 {
		pDestroyWindow.Call(setWnd)
	}
	pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
	pPostQuitMessage.Call(0)
}

func wndProc(h uintptr, m uint32, wParam, lParam uintptr) uintptr {
	defer guard("wndProc")
	switch m {
	case WM_TRAY:
		switch lParam & 0xFFFF {
		case WM_LBUTTONUP:
			openSettings(false)
		case WM_RBUTTONUP:
			showMenu()
		}
		return 0
	case WM_REFRESH: // Scroll Lock flipped the arrow keys
		setArrows(arrowsOn, wParam == 1)
		return 0
	case WM_HELLO:
		if int(wParam) > appBuild {
			pPostMessageW.Call(hwnd, 0x10, 0, 0) // a newer version was opened: make way for it
			return 1
		}
		openSettings(false) // same version opened again: just show the window
		return 2
	case WM_SEARCH:
		openSearch()
		return 0
	case WM_SEARCHRESULTS:
		onSearchResults(int(wParam))
		return 0
	case WM_PLAYED:
		onPlayed(wParam == 1)
		return 0
	case WM_PLAYLISTS:
		onPlaylistsScanned()
		return 0
	case WM_PLCOVER:
		onPlaylistCover()
		return 0
	case WM_PLAYLISTSCAN:
		startPlaylistScan(true)
		return 0
	case WM_SKIP:
		onSkip(wParam == 1)
		return 0
	case WM_SWITCHAPP:
		if getSnap().valid {
			if wParam == 1 {
				queueCmd(cmdSwitchNext)
			} else {
				queueCmd(cmdSwitchPrev)
			}
		} else {
			showMessage("No apps to switch between", "Start a song or video first")
		}
		return 0
	case WM_SWITCHED:
		if wParam == 0 {
			showMessage("No apps to switch between", "Start a song or video first")
		} else {
			showSwitched(getSnap(), int(wParam))
		}
		return 0
	case WM_SKIPRESULT:
		onSkipResult(int(wParam), int(lParam))
		return 0
	case WM_MEDIAUPDATE:
		onMediaUpdate()
		return 0
	case WM_SPOTIFY:
		d := float32(spotifyStep)
		if wParam == 0 {
			d = -d
		}
		if pct, name, fromMedia := changeMediaVolume(d); pct >= 0 {
			showVolume(name, pct, fromMedia)
		} else {
			showMessage("Nothing is playing", "Start a song or video first")
		}
		return 0
	case WM_PLAYPAUSE:
		onPlayPause()
		return 0
	case WM_TIMER:
		if wParam == ppTimer {
			resolvePlayPause()
		} else if wParam == skipTimer {
			onSkipTimer()
		} else if wParam == 9 {
			pKillTimer.Call(hwnd, 9)
			startPlaylistScan(true)
			fetchPlaylistCovers() // any covers still missing
		}
		return 0
	case WM_DESTROY:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(m), wParam, lParam)
	return r
}

func msgBox(text string) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16("MediaKeys"))), 0x40)
}

func main() {
	runtime.LockOSThread()
	initLog()
	initCOM()
	// crisp rendering on high-DPI screens
	if r, _, _ := user32.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3)); r == 0 {
		user32.NewProc("SetProcessDPIAware").Call()
	}
	if p := user32.NewProc("GetDpiForSystem"); p.Find() == nil {
		if d, _, _ := p.Call(); d > 0 {
			scale = float64(d) / 96
		}
	}

	loadSettings()
	loadHistory()
	loadPlaylists()
	arrowsOn = settings.ArrowsOn
	atStartup := len(os.Args) > 1 && os.Args[1] == "--startup"

	// Already running? Same version: show its window. Older version: replace it.
	pFindWindowW := user32.NewProc("FindWindowW")
	pSleep := kernel32.NewProc("Sleep")
	pSendMessageTimeoutW := user32.NewProc("SendMessageTimeoutW")
	took := false
	asked := false
	for try := 0; try < 25; try++ {
		mtx, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16("MediaKeys_SingleInstance"))))
		if err != syscall.Errno(183) {
			took = true
			break
		}
		pCloseHandle.Call(mtx)
		old, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16("MediaKeysWnd"))), 0)
		if old != 0 && !asked {
			asked = true
			var res uintptr
			pAllowSetForegroundWnd.Call(^uintptr(0)) // let it bring its window to the front
			pSendMessageTimeoutW.Call(old, WM_HELLO, appBuild, 0, 0x2 /*SMTO_ABORTIFHUNG*/, 2000, uintptr(unsafe.Pointer(&res)))
			if res == 2 {
				return // it's showing its window; nothing more to do
			}
			if res == 0 { // an early test version that doesn't answer: close it
				pPostMessageW.Call(old, 0x10, 0, 0)
			}
		}
		pSleep.Call(150)
	}
	if !took {
		msgBox("Another copy of MediaKeys is running and wouldn't close.\n\nRight-click its icon by the clock, choose Quit, then open this again.")
		return
	}

	inst, _, _ := pGetModuleHandleW.Call(0)
	cx, _, _ := user32.NewProc("GetSystemMetrics").Call(49) // SM_CXSMICON: crisp tray-sized icon
	icon, _, _ := user32.NewProc("LoadImageW").Call(inst, 1, 1, cx, cx, 0)
	if icon == 0 {
		icon, _, _ = pLoadIconW.Call(0, 32512)
	}

	cls := u16("MediaKeysWnd")
	wc := wndclassex{WndProc: syscall.NewCallback(wndProc), Instance: inst, ClassName: cls, Icon: icon}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("MediaKeys"))), 0, 0, 0, 0, 0, 0, 0, inst, 0)

	createOSD(inst)

	nid.Size = uint32(unsafe.Sizeof(nid))
	nid.Wnd = hwnd
	nid.ID = 1
	nid.Flags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.CallbackMessage = WM_TRAY
	nid.Icon = icon
	copyU16(nid.Tip[:], tipText())
	pShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))

	startMediaWatcher()
	pSetTimer.Call(hwnd, 9, 5000, 0) // read your playlists from Spotify shortly after starting

	hook, _, _ = pSetWindowsHookExW.Call(WH_KEYBOARD_LL, syscall.NewCallback(hookProc), inst, 0)
	if hook == 0 {
		pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
		msgBox("MediaKeys couldn't start its keyboard listener.")
		return
	}

	switch {
	case !settings.Welcomed:
		openSettings(true) // first run: welcome + how it works
	case !atStartup:
		showMessage("MediaKeys is running", "Click the tray icon for settings")
	}

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	pUnhookWindowsHook.Call(hook)
	pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
}
