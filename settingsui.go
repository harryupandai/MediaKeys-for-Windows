package main

// The welcome / Controls & Settings window. Drawn with the same software
// renderer as the on-screen cards, so it shares their look.

import (
	"math"
	"syscall"
	"unsafe"
)

var (
	dwmapi                 = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	pAdjustWindowRectEx    = user32.NewProc("AdjustWindowRectEx")
	pBeginPaintW           = user32.NewProc("BeginPaint")
	pEndPaintW             = user32.NewProc("EndPaint")
	pBitBlt                = gdi32.NewProc("BitBlt")
	pInvalidateRect        = user32.NewProc("InvalidateRect")
	pSetCursor             = user32.NewProc("SetCursor")
	pLoadCursorW           = user32.NewProc("LoadCursorW")
	pDestroyWindow         = user32.NewProc("DestroyWindow")
	pSetWindowPos          = user32.NewProc("SetWindowPos")
	pSendMessageW          = user32.NewProc("SendMessageW")
	pIsIconic              = user32.NewProc("IsIconic")
	pTrackMouseEvent       = user32.NewProc("TrackMouseEvent")
	pSetWindowTextW        = user32.NewProc("SetWindowTextW")
	pBringWindowToTop      = user32.NewProc("BringWindowToTop")
	pAllowSetForegroundWnd = user32.NewProc("AllowSetForegroundWindow")
)

const (
	setCW, setCH = 560.0, 628.0 // client size, logical px
)

var (
	colSetBg    = rgb{17, 17, 21}
	colPanel    = rgb{26, 26, 32}
	colMutedTxt = rgb{150, 150, 162}
	colLabel    = rgb{130, 130, 142}
	colAccent   = rgb{108, 99, 255}
	colKeyTop   = rgb{52, 52, 61}
	colKeyEdge  = rgb{36, 36, 42}
	colOff      = rgb{60, 60, 70}
)

type paintstruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type toggleRow struct {
	title, desc string
	get         func() bool
	set         func(bool)
	pos         float64 // knob position 0..1, animated
	hit         rect
}

var (
	setWnd              uintptr
	setW, setH          int
	setDC, setBits      uintptr
	setPx               []uint32
	setMaskDC, setMaskB uintptr
	setMask             []uint32
	setWelcome          bool
	setHover            = -1 // toggle index, or 100 = button
	setBtn              rect
	setAnimOn           bool
	setClassReady       bool
	toggles             []*toggleRow

	fSetTitle, fSetSub, fSetLabel, fSetRow, fSetRowB, fSetSmall, fSetKey, fSetBtn uintptr
)

func initToggles() {
	toggles = []*toggleRow{
		{title: "Arrow keys control media", desc: "Turn off to use the arrows normally. Scroll Lock switches it too.",
			get: func() bool { return arrowsOn }, set: func(v bool) { setArrows(v, false) }},
		{title: "Start with Windows", desc: "Open MediaKeys automatically when you sign in.",
			get: startupEnabled, set: func(v bool) { setStartup(v); refreshSettingsWindow() }},
		{title: "Show on-screen cards", desc: "The popup with the song, cover art and volume.",
			get: func() bool { return settings.ShowCards }, set: setShowCards},
		{title: "Typing sounds", desc: "A soft click for each letter you type in the search bar.",
			get: func() bool { return settings.TypingSounds }, set: setTypingSounds},
	}
	for _, t := range toggles {
		if t.get() {
			t.pos = 1
		}
	}
}

// run f with the shared drawing helpers pointed at the settings canvas
func withSettingsCanvas(f func()) {
	sw, sh, smdc, sm := winW, winH, maskDC, mask
	winW, winH, maskDC, mask = setW, setH, setMaskDC, setMask
	defer func() { winW, winH, maskDC, mask = sw, sh, smdc, sm }()
	f()
}

func S(v float64) int32 { return int32(math.Round(v * scale)) }

func measureText(s string, font uintptr) int32 {
	pSelectObject.Call(setMaskDC, font)
	p, _ := syscall.UTF16FromString(s)
	r := rect{0, 0, 2000, 200}
	pDrawTextW.Call(setMaskDC, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), 0x400|dtSingle|dtNoPrefix) // DT_CALCRECT
	return r.R - r.L
}

func fillRect(b []uint32, x0, y0, x1, y1 float64, c rgb, a float64) {
	for y := int(y0); y < int(math.Ceil(y1)) && y < winH; y++ {
		for x := int(x0); x < int(math.Ceil(x1)) && x < winW; x++ {
			if x >= 0 && y >= 0 {
				blendPx(&b[y*winW+x], c, a)
			}
		}
	}
}

// app logo: indigo tile with a white keycap showing play/pause (matches app.ico)
func drawAppIcon(b []uint32, x0, y0, sz float64) {
	k := sz / 1024
	X := func(v float64) float64 { return x0 + v*k }
	Y := func(v float64) float64 { return y0 + v*k }
	top, bot := rgb{94, 92, 255}, rgb{58, 44, 190}
	for y := int(Y(40)) - 1; y <= int(Y(984))+1; y++ {
		t := clamp01((float64(y) - Y(40)) / (944 * k))
		c := rgb{top.R + (bot.R-top.R)*t, top.G + (bot.G-top.G)*t, top.B + (bot.B-top.B)*t}
		for x := int(X(40)) - 1; x <= int(X(984))+1; x++ {
			if x < 0 || y < 0 || x >= winW || y >= winH {
				continue
			}
			cov := clamp01(0.5 - rrSDF(float64(x)+0.5, float64(y)+0.5, X(40), Y(40), X(984), Y(984), 230*k))
			blendPx(&b[y*winW+x], c, cov)
		}
	}
	fillRRect(b, X(150), Y(170), X(874), Y(904), 120*k, rgb{176, 176, 196}, 1) // keycap lip
	fillRRect(b, X(150), Y(150), X(874), Y(854), 120*k, rgb{246, 246, 250}, 1) // keycap face
	cx, cy := X(474), Y(500)
	g := rgb{70, 60, 220}
	tri(b, cx-150*k, cy-150*k, cx+60*k, cy, cx-150*k, cy+150*k, g)
	fillRRect(b, cx+85*k, cy-150*k, cx+135*k, cy+150*k, 18*k, g, 1)
	fillRRect(b, cx+175*k, cy-150*k, cx+225*k, cy+150*k, 18*k, g, 1)
}

// keycap; returns its right edge
func drawKey(b []uint32, x, y float64, label string) float64 {
	tw := float64(measureText(label, fSetKey))
	w := math.Max(L(26), tw+L(14))
	h := L(22)
	fillRRect(b, x, y, x+w, y+h, L(5), colKeyEdge, 1)
	fillRRect(b, x, y, x+w, y+h-L(2), L(5), colKeyTop, 1)
	drawText(b, label, fSetKey, colWhite, 1, rect{int32(x), int32(y), int32(x + w), int32(y + h - L(2))}, 1|dtVC|dtSingle|dtNoPrefix, 0)
	return x + w
}

func renderSettings() {
	if setWnd == 0 {
		return
	}
	withSettingsCanvas(func() {
		b := setPx
		for i := range b {
			b[i] = 0xFF000000 | uint32(colSetBg.R)<<16 | uint32(colSetBg.G)<<8 | uint32(colSetBg.B)
		}
		ls := int(math.Round(L(2)))

		// header
		drawAppIcon(b, L(24), L(22), L(44))
		title, sub := "Welcome to MediaKeys", "Control music and videos from anywhere with your keyboard."
		if !setWelcome {
			title, sub = "MediaKeys", "Version "+appVersion+"  ·  running in your tray by the clock"
		}
		drawText(b, title, fSetTitle, colWhite, 1, rect{S(80), S(17), S(540), S(47)}, dtLine, 0)
		drawText(b, sub, fSetSub, colMutedTxt, 1, rect{S(80), S(47), S(540), S(67)}, dtLine, 0)

		// controls
		drawText(b, "CONTROLS", fSetLabel, colLabel, 1, rect{S(28), S(86), S(300), S(104)}, dtLine, ls)
		rows := []struct {
			keys      []string
			text, sub string
		}{
			{[]string{"Pause"}, "Play / pause", "mutes Discord and games"},
			{[]string{"←", "→"}, "Previous / next", "song or video"},
			{[]string{"↑", "↓"}, "Computer volume", ""},
			{[]string{"Shift", "+", "↑", "↓"}, "Volume of the app you're controlling", ""},
			{[]string{"Shift", "+", "←", "→"}, "Switch app", "Spotify, browser, Discord…"},
			{[]string{"Ctrl", "+", "Alt", "+", "X"}, "Search Spotify", "songs and your playlists"},
			{[]string{"Scroll Lock"}, "Turn the arrow keys on or off", ""},
		}
		const rowH, togH = 32.0, 46.0 // compact, so the window fits on 1080p screens at 150% scaling
		py := 108.0
		fillRRect(b, L(24), L(py), L(536), L(py+float64(len(rows))*rowH), L(12), colPanel, 1)
		for i, r := range rows {
			y := py + float64(i)*rowH
			if i > 0 {
				fillRect(b, L(40), L(y), L(520), L(y)+math.Max(1, L(1)), colWhite, 0.055)
			}
			x := L(40)
			for _, k := range r.keys {
				if k == "+" {
					drawText(b, "+", fSetRow, colMutedTxt, 1, rect{int32(x), S(y), int32(x + L(16)), S(y + rowH)}, 1|dtVC|dtSingle, 0)
					x += L(16)
					continue
				}
				x = drawKey(b, x, L(y+5), k) + L(5)
			}
			tx := S(214)
			drawText(b, r.text, fSetRow, colWhite, 1, rect{tx, S(y), S(530), S(y + rowH)}, dtLine, 0)
			if r.sub != "" {
				w := measureText(r.text, fSetRow)
				drawText(b, r.sub, fSetSmall, colMutedTxt, 1, rect{tx + w + S(8), S(y + 1), S(530), S(y + rowH)}, dtLine, 0)
			}
		}

		// settings
		ex := float64(len(rows))*rowH - 6*38 // shift from the original layout (six 38-px rows)
		drawText(b, "SETTINGS", fSetLabel, colLabel, 1, rect{S(28), S(352 + ex), S(300), S(370 + ex)}, dtLine, ls)
		sy := 374.0 + ex
		fillRRect(b, L(24), L(sy), L(536), L(sy+float64(len(toggles))*togH), L(12), colPanel, 1)
		for i, t := range toggles {
			y := sy + float64(i)*togH
			if i > 0 {
				fillRect(b, L(40), L(y), L(520), L(y)+math.Max(1, L(1)), colWhite, 0.055)
			}
			drawText(b, t.title, fSetRowB, colWhite, 1, rect{S(40), S(y + 4), S(460), S(y + 24)}, dtLine, 0)
			drawText(b, t.desc, fSetSmall, colMutedTxt, 1, rect{S(40), S(y + 24), S(460), S(y + 41)}, dtLine, 0)
			// switch
			tx, ty := L(476), L(y+11)
			off := colOff
			if setHover == i {
				off = rgb{72, 72, 84}
			}
			track := rgb{off.R + (colAccent.R-off.R)*t.pos, off.G + (colAccent.G-off.G)*t.pos, off.B + (colAccent.B-off.B)*t.pos}
			fillRRect(b, tx, ty, tx+L(42), ty+L(24), L(12), track, 1)
			kx := tx + L(2) + L(18)*t.pos
			fillCircle(b, kx+L(10), ty+L(12), L(10), colWhite, 1)
			t.hit = rect{S(24), S(y), S(536), S(y + togH)}
		}

		// footer (below the settings panel, however many switches it has)
		fx := ex + float64(len(toggles))*46 - 3*52
		drawText(b, "MediaKeys runs in the tray by the clock.", fSetSmall, colMutedTxt, 1, rect{S(28), S(549 + fx), S(390), S(567 + fx)}, dtLine, 0)
		drawText(b, "Click its icon to come back here.", fSetSmall, colMutedTxt, 1, rect{S(28), S(567 + fx), S(390), S(585 + fx)}, dtLine, 0)
		label := "Done"
		if setWelcome {
			label = "Get started"
		}
		btn := colAccent
		if setHover == 100 {
			btn = rgb{128, 120, 255}
		}
		fillRRect(b, L(404), L(548+fx), L(536), L(588+fx), L(10), btn, 1)
		drawText(b, label, fSetBtn, colWhite, 1, rect{S(404), S(548 + fx), S(536), S(588 + fx)}, 1|dtVC|dtSingle, 0)
		setBtn = rect{S(404), S(548 + fx), S(536), S(588 + fx)}
	})
	pInvalidateRect.Call(setWnd, 0, 0)
}

func refreshSettingsWindow() {
	if setWnd == 0 {
		return
	}
	for _, t := range toggles {
		target := 0.0
		if t.get() {
			target = 1
		}
		if t.pos != target && !setAnimOn {
			setAnimOn = true
			pSetTimer.Call(setWnd, 1, 15, 0)
		}
	}
	renderSettings()
}

func animateToggles() {
	moving := false
	for _, t := range toggles {
		target := 0.0
		if t.get() {
			target = 1
		}
		if d := target - t.pos; math.Abs(d) > 0.01 {
			t.pos += d * 0.32
			moving = true
		} else {
			t.pos = target
		}
	}
	if !moving {
		pKillTimer.Call(setWnd, 1)
		setAnimOn = false
	}
	renderSettings()
}

func inRect(r rect, x, y int32) bool { return x >= r.L && x < r.R && y >= r.T && y < r.B }

func hitTest(x, y int32) int {
	if inRect(setBtn, x, y) {
		return 100
	}
	for i, t := range toggles {
		if inRect(t.hit, x, y) {
			return i
		}
	}
	return -1
}

func closeSettings() {
	if setWelcome {
		settings.Welcomed = true
		saveSettings()
	}
	if setWnd != 0 {
		pDestroyWindow.Call(setWnd)
	}
}

func settingsProc(h uintptr, m uint32, wParam, lParam uintptr) uintptr {
	defer guard("settingsProc")
	switch m {
	case 0x0F: // WM_PAINT
		var ps paintstruct
		hdc, _, _ := pBeginPaintW.Call(h, uintptr(unsafe.Pointer(&ps)))
		pBitBlt.Call(hdc, 0, 0, uintptr(setW), uintptr(setH), setDC, 0, 0, 0x00CC0020)
		pEndPaintW.Call(h, uintptr(unsafe.Pointer(&ps)))
		return 0
	case 0x14: // WM_ERASEBKGND
		return 1
	case 0x200: // WM_MOUSEMOVE
		x, y := int32(int16(lParam&0xFFFF)), int32(int16(lParam>>16&0xFFFF))
		if hv := hitTest(x, y); hv != setHover {
			setHover = hv
			renderSettings()
		}
		tme := struct {
			Size, Flags uint32
			Hwnd        uintptr
			Hover       uint32
		}{Flags: 0x2, Hwnd: h} // TME_LEAVE
		tme.Size = uint32(unsafe.Sizeof(tme))
		pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
		return 0
	case 0x2A3: // WM_MOUSELEAVE
		if setHover != -1 {
			setHover = -1
			renderSettings()
		}
		return 0
	case 0x20: // WM_SETCURSOR
		if lParam&0xFFFF == 1 && setHover != -1 { // HTCLIENT over something clickable
			c, _, _ := pLoadCursorW.Call(0, 32649) // IDC_HAND
			pSetCursor.Call(c)
			return 1
		}
	case 0x202: // WM_LBUTTONUP
		x, y := int32(int16(lParam&0xFFFF)), int32(int16(lParam>>16&0xFFFF))
		switch hv := hitTest(x, y); {
		case hv == 100:
			closeSettings()
		case hv >= 0:
			t := toggles[hv]
			t.set(!t.get())
			refreshSettingsWindow()
		}
		return 0
	case 0x100: // WM_KEYDOWN
		if wParam == 0x1B || wParam == 0x0D { // Esc / Enter
			closeSettings()
			return 0
		}
	case 0x113: // WM_TIMER
		animateToggles()
		return 0
	case 0x10: // WM_CLOSE
		closeSettings()
		return 0
	case 0x2: // WM_DESTROY
		setWnd = 0
		setHover = -1
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(m), wParam, lParam)
	return r
}

func setupSettingsCanvas() {
	setW, setH = int(math.Round(L(setCW))), int(math.Round(L(setCH)))
	sw, sh := winW, winH
	winW, winH = setW, setH // newDIB sizes by these
	setDC, setBits, setPx = newDIB()
	setMaskDC, setMaskB, setMask = newDIB()
	winW, winH = sw, sh

	fSetTitle = mkFont(21, 600)
	fSetSub = mkFont(13, 400)
	fSetLabel = mkFont(10.5, 700)
	fSetRow = mkFont(13.5, 400)
	fSetRowB = mkFont(13.5, 600)
	fSetSmall = mkFont(12, 400)
	fSetKey = mkFont(11.5, 600)
	fSetBtn = mkFont(13.5, 600)
	initToggles()
}

// openSettings shows the window (welcome = first-run wording), or brings it to the front.
func openSettings(welcome bool) {
	if setWnd != 0 {
		if r, _, _ := pIsIconic.Call(setWnd); r != 0 {
			pShowWindow.Call(setWnd, 9) // SW_RESTORE
		}
		pSetForegroundWnd.Call(setWnd)
		return
	}
	inst, _, _ := pGetModuleHandleW.Call(0)
	if !setClassReady {
		setupSettingsCanvas()
		big, _, _ := user32.NewProc("LoadImageW").Call(inst, 1, 1, uintptr(S(32)), uintptr(S(32)), 0)
		cls := u16("MediaKeysSettings")
		wc := wndclassex{WndProc: syscall.NewCallback(settingsProc), Instance: inst, ClassName: cls, Icon: big}
		wc.Cursor, _, _ = pLoadCursorW.Call(0, 32512) // IDC_ARROW
		wc.Size = uint32(unsafe.Sizeof(wc))
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		setClassReady = true
	}
	setWelcome = welcome
	for _, t := range toggles {
		t.pos = 0
		if t.get() {
			t.pos = 1
		}
	}

	const style = 0x00C00000 | 0x00080000 | 0x00020000 // caption, system menu, minimize
	r := rect{0, 0, int32(setW), int32(setH)}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, 0)
	ww, wh := r.R-r.L, r.B-r.T
	wa := activeWorkArea()
	x := wa.L + (wa.R-wa.L-ww)/2
	y := wa.T + (wa.B-wa.T-wh)/2
	if y < wa.T {
		y = wa.T
	}
	title := "MediaKeys"
	setWnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("MediaKeysSettings"))), uintptr(unsafe.Pointer(u16(title))),
		style, uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), 0, 0, inst, 0)
	if setWnd == 0 {
		return
	}
	// dark title bar that blends into the window (Windows 11; harmless elsewhere)
	on := int32(1)
	pDwmSetWindowAttribute.Call(setWnd, 20, uintptr(unsafe.Pointer(&on)), 4)
	capColor := uint32(colSetBg.B)<<16 | uint32(colSetBg.G)<<8 | uint32(colSetBg.R)
	pDwmSetWindowAttribute.Call(setWnd, 35, uintptr(unsafe.Pointer(&capColor)), 4)
	txt := uint32(0x00F0F0F0)
	pDwmSetWindowAttribute.Call(setWnd, 36, uintptr(unsafe.Pointer(&txt)), 4)

	renderSettings()
	pShowWindow.Call(setWnd, 5) // SW_SHOW
	pSetForegroundWnd.Call(setWnd)
	pBringWindowToTop.Call(setWnd)
}
