package main

// Ctrl+Alt+X: search Spotify. Sits where the on-screen cards appear (bottom
// centre) and grows upward: the search box is at the bottom, results stack
// above it with the most likely match at the top. Before you type it shows
// recently played songs; while you type it shows live results with covers.

import (
	"math"
	"net/url"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	WM_SEARCH = WM_APP + 21

	srCardW   = 600.0
	srMargin  = 26.0
	srFieldH  = 60.0
	srRowH    = 54.0
	srMaxRows = 7 // 6 results + "see all in Spotify"
	srFootH   = 32.0
	srLabelH  = 32.0
	srMsgH    = 46.0

	itemTrack    = 0 // live result
	itemRecent   = 1 // recently played
	itemSearch   = 2 // "See all results for …"
	itemPlaylist = 3 // one of your playlists

	debounceTimer = 3
)

var (
	pShellExecuteW      = shell32.NewProc("ShellExecuteW")
	pOpenClipboard      = user32.NewProc("OpenClipboard")
	pCloseClipboard     = user32.NewProc("CloseClipboard")
	pGetClipboardData   = user32.NewProc("GetClipboardData")
	pGlobalLock         = kernel32.NewProc("GlobalLock")
	pGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	pAttachThreadInput  = user32.NewProc("AttachThreadInput")
	pGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
	pSetFocus           = user32.NewProc("SetFocus")
	pSetActiveWindow    = user32.NewProc("SetActiveWindow")

	srchWnd            uintptr
	srchW, srchH       int
	srchDC, srchBits   uintptr
	srchPx             []uint32
	srchMaskDC, srchMB uintptr
	srchMask           []uint32
	srchReady          bool
	searchActive       bool // the search bar has keyboard focus (arrow keys go to it)

	srchQuery     []rune
	srchAll       bool // Ctrl+A: next typing replaces everything
	srchSel       = 0  // item index; 0 = the top row (most likely match)
	srchHover     = -1
	srchItems     []searchItem
	srchLabel     string
	srchCaret     = true
	srchAlpha     float64
	srchRowRects  []rect // by item index
	srchPressed   = -1   // row the mouse went down on
	srchWork      rect   // work area of the screen the search bar is on
	srchCloseR    rect   // the ✕ button
	srchCloseHov  bool
	srchEnterWait bool // Enter pressed while results were loading: play the top one when they arrive

	fSrQuery, fSrRowT, fSrRowS, fSrLabel, fSrFoot, fSrRowTB, fSrRowTag uintptr
)

type searchItem struct {
	kind  int
	title string
	sub   string
	art   []uint32
	query string // what to search in Spotify
	song  string // song title and artist, for pressing Play
	by    string
	pl    *playlistEntry
}

// ---- items ----

func buildItems() {
	q := strings.TrimSpace(string(srchQuery))
	srchItems = srchItems[:0]
	if q == "" {
		srchLabel = "RECENTLY SEARCHED & PLAYED"
		for _, h := range history {
			if len(srchItems) >= 6 {
				break
			}
			if h.Kind == "playlist" {
				p := &playlistEntry{Title: h.Title, URI: h.URI}
				srchItems = append(srchItems, searchItem{kind: itemPlaylist, title: h.Title, sub: "PLAYLIST  ·  " + timeAgo(h.At), pl: p, art: playlistArt(h.URI, h.Title)})
				continue
			}
			sub := h.Artist
			if sub != "" {
				sub += "  ·  "
			}
			sub += timeAgo(h.At)
			srchItems = append(srchItems, searchItem{kind: itemRecent, title: h.Title, sub: sub, art: h.px,
				query: strings.TrimSpace(h.Title + " " + h.Artist), song: h.Title, by: h.Artist})
		}
	} else {
		catMu.Lock()
		fresh := catQuery == q && !catLoading
		results := append([]catalogTrack(nil), catResults...)
		loading, failed := catLoading, catErr
		catMu.Unlock()
		switch {
		case fresh && !failed:
			srchLabel = "RESULTS"
			for _, p := range matchPlaylists(q) {
				p := p
				srchItems = append(srchItems, searchItem{kind: itemPlaylist, title: p.Title, sub: "PLAYLIST", pl: &p, art: p.px})
			}
			for _, t := range results {
				if len(srchItems) >= 6 {
					break
				}
				sub := t.Artist
				if t.Album != "" && t.Album != t.Title {
					sub += "  ·  " + t.Album
				}
				srchItems = append(srchItems, searchItem{kind: itemTrack, title: t.Title, sub: sub, art: t.art,
					query: strings.TrimSpace(t.Title + " " + t.Artist), song: t.Title, by: t.Artist})
			}
			if len(srchItems) == 0 {
				srchLabel = "NO MATCHES"
			}
		case failed && !loading:
			srchLabel = "COULDN'T LOAD RESULTS  ·  PRESS ENTER TO SEARCH IN SPOTIFY"
		default:
			// keep the previous results on screen until the new ones arrive (all at once)
			srchLabel = "SEARCHING…"
			for _, t := range results {
				sub := t.Artist
				if t.Album != "" && t.Album != t.Title {
					sub += "  ·  " + t.Album
				}
				srchItems = append(srchItems, searchItem{kind: itemTrack, title: t.Title, sub: sub, art: t.art,
					query: strings.TrimSpace(t.Title + " " + t.Artist), song: t.Title, by: t.Artist})
			}
		}
		srchItems = append(srchItems, searchItem{kind: itemSearch, title: "See all results for “" + q + "”", sub: "Opens Spotify's search", query: q})
	}
	if srchSel >= len(srchItems) {
		srchSel = len(srchItems) - 1
	}
	if srchSel < 0 {
		srchSel = 0
	}
}

// ---- actions ----

func shellOpen(target string) bool {
	r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(target))), 0, 0, 1)
	return r > 32
}

func searchSpotify(q string) {
	if !shellOpen("spotify:search:" + url.PathEscape(q)) {
		shellOpen("https://open.spotify.com/search/" + url.PathEscape(q)) // Spotify app not installed
	}
}

func activateItem(i int) {
	if playBusy {
		return
	}
	q := strings.TrimSpace(string(srchQuery))
	if i >= 0 && i < len(srchItems) {
		if it := srchItems[i]; it.kind == itemPlaylist && it.pl != nil {
			hideSearch()
			playPlaylist(*it.pl)
			return
		}
		if it := srchItems[i]; it.song != "" {
			hideSearch()
			playInSpotify(it.song, it.by, it.art) // opens it in Spotify and presses Play
			return
		}
		q = srchItems[i].query
	}
	if q == "" {
		return
	}
	hideSearch()
	searchSpotify(q)
}

// ---- drawing ----

func cardHeight() float64 {
	h := srLabelH + srFieldH + srFootH + 6
	if len(srchItems) > 0 {
		h += float64(len(srchItems)) * srRowH
	} else {
		h += srMsgH
	}
	return h
}

func glyphSearch(b []uint32, cx, cy, u float64, c rgb) {
	r := 6.5 * u
	ox, oy := cx-2*u, cy-2*u
	fillShape(b, ox-r-2*u, oy-r-2*u, cx+9*u, cy+9*u, func(x, y float64) bool {
		if math.Abs(math.Hypot(x-ox, y-oy)-r) <= 1.15*u {
			return true
		}
		ax, ay := ox+r*0.72, oy+r*0.72
		bx, by := cx+7*u, cy+7*u
		vx, vy := bx-ax, by-ay
		t := clamp01(((x-ax)*vx + (y-ay)*vy) / (vx*vx + vy*vy))
		return math.Hypot(x-(ax+vx*t), y-(ay+vy*t)) <= 1.3*u
	}, c, 1)
}

func measureOn(dc uintptr, s string, font uintptr) int32 {
	if s == "" {
		return 0
	}
	pSelectObject.Call(dc, font)
	p, _ := syscall.UTF16FromString(s)
	r := rect{0, 0, 4000, 200}
	pDrawTextW.Call(dc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), 0x400|dtSingle|dtNoPrefix)
	return r.R - r.L
}

func drawSmallArt(b []uint32, art []uint32, ax, ay float64) {
	n := int(math.Sqrt(float64(len(art))))
	ix, iy := int(math.Round(ax)), int(math.Round(ay))
	for j := 0; j < n; j++ {
		for k := 0; k < n; k++ {
			wx, wy := ix+k, iy+j
			if wx < 0 || wy < 0 || wx >= winW || wy >= winH {
				continue
			}
			cov := clamp01(0.5 - rrSDF(float64(wx)+0.5, float64(wy)+0.5, float64(ix), float64(iy), float64(ix+n), float64(iy+n), L(8)))
			if cov > 0 {
				p := art[j*n+k]
				blendPx(&b[wy*winW+wx], rgb{float64(p >> 16 & 0xFF), float64(p >> 8 & 0xFF), float64(p & 0xFF)}, cov)
			}
		}
	}
}

func renderSearch() {
	if srchWnd == 0 {
		return
	}
	buildItems()
	sw, sh, smdc, sm := winW, winH, maskDC, mask
	winW, winH, maskDC, mask = srchW, srchH, srchMaskDC, srchMask
	defer func() { winW, winH, maskDC, mask = sw, sh, smdc, sm }()

	b := srchPx
	for i := range b {
		b[i] = 0
	}
	// card anchored to the bottom of the canvas (where the on-screen cards sit), growing upward
	x0 := L(srMargin)
	x1 := x0 + L(srCardW)
	y1 := float64(srchH) - L(srMargin)
	y0 := y1 - L(cardHeight())
	rad := L(18)

	shd := L(22)
	for y := int(y0 - shd); y < winH; y++ {
		if y < 0 {
			continue
		}
		if y < 0 {
			continue
		}
		for x := 0; x < winW; x++ {
			d := rrSDF(float64(x)+0.5, float64(y)+0.5-L(5), x0, y0, x1, y1, rad)
			if d > -L(4) && d < shd {
				t := 1 - clamp01((d+L(4))/(shd+L(4)))
				blendPx(&b[y*winW+x], colBlack, 0.40*t*t)
			}
		}
	}
	fillRRect(b, x0, y0, x1, y1, rad, rgb{22, 22, 27}, 1)
	fillShape(b, x0-1, y0-1, x1+1, y1+1, func(x, y float64) bool {
		d := rrSDF(x, y, x0, y0, x1, y1, rad)
		return d <= 0 && d > -L(1)
	}, colWhite, 0.10)

	// label
	ls := int(math.Round(L(1.6)))
	ly := y0
	drawText(b, srchLabel, fSrLabel, colWhite, 0.45, rect{int32(x0 + L(22)), int32(ly + L(10)), int32(x1 - L(54)), int32(ly + L(30))}, dtLine, ls)
	// ✕ close button, top right
	ccx, ccy, cr := x1-L(24), y0+L(20), L(12)
	xa := 0.55
	if srchCloseHov {
		fillCircle(b, ccx, ccy, cr, colWhite, 0.12)
		xa = 0.95
	}
	fillShape(b, ccx-cr, ccy-cr, ccx+cr, ccy+cr, func(x, y float64) bool {
		dx, dy := x-ccx, y-ccy
		if math.Abs(dx) > L(4.6) || math.Abs(dy) > L(4.6) {
			return false
		}
		return math.Abs(dx-dy) < L(1.05) || math.Abs(dx+dy) < L(1.05)
	}, colWhite, xa)
	srchCloseR = rect{int32(ccx - cr - L(4)), int32(ccy - cr - L(4)), int32(ccx + cr + L(4)), int32(ccy + cr + L(4))}

	// rows: best match at the top
	srchRowRects = make([]rect, len(srchItems))
	listTop := ly + L(srLabelH)
	n := len(srchItems)
	if n == 0 {
		drawText(b, "Songs you search for and play will show up here.", fSrRowS, colWhite, 0.5,
			rect{int32(x0 + L(22)), int32(listTop), int32(x1 - L(22)), int32(listTop + L(srMsgH))}, dtLine, 0)
	}
	dim := 1.0
	if srchLabel == "SEARCHING…" {
		dim = 0.55 // previous results, until the new ones arrive
	}
	for i := 0; i < n; i++ {
		it := srchItems[i]
		ry := listTop + float64(i)*L(srRowH)
		srchRowRects[i] = rect{int32(x0 + L(8)), int32(ry), int32(x1 - L(8)), int32(ry + L(srRowH))}
		if i == srchSel {
			fillRRect(b, x0+L(8), ry+L(2), x1-L(8), ry+L(srRowH-2), L(10), colWhite, 0.085)
		} else if i == srchHover {
			fillRRect(b, x0+L(8), ry+L(2), x1-L(8), ry+L(srRowH-2), L(10), colWhite, 0.045)
		}
		ax, ay, as := x0+L(18), ry+L(7), L(40)
		switch {
		case it.kind == itemSearch:
			fillRRect(b, ax, ay, ax+as, ay+as, L(8), colGreen, 1)
			glyphSearch(b, ax+as/2, ay+as/2, scale*0.9, rgb{10, 10, 12})
		case it.kind == itemPlaylist && it.art == nil:
			fillRRect(b, ax, ay, ax+as, ay+as, L(8), rgb{108, 99, 255}, 1)
			glyphPlaylist(b, ax+as/2, ay+as/2, scale, colWhite)
		case it.art != nil:
			drawSmallArt(b, it.art, ax, ay)
		default:
			fillRRect(b, ax, ay, ax+as, ay+as, L(8), colWhite, 0.08)
			glyphPlay(b, ax+as/2, ay+as/2, scale*0.8, rgb{150, 150, 160})
		}
		a := 1.0
		if it.kind == itemTrack {
			a = dim
		}
		ttx, trr := int32(ax+as+L(14)), int32(x1-L(50))
		if it.kind == itemPlaylist { // stands out: extra-bold name, indigo "PLAYLIST"
			drawText(b, it.title, fSrRowTB, colWhite, a, rect{ttx, int32(ry + L(7)), trr, int32(ry + L(29))}, dtLine, 0)
			drawText(b, it.sub, fSrRowTag, rgb{150, 142, 255}, a, rect{ttx, int32(ry + L(29)), trr, int32(ry + L(47))}, dtLine, int(math.Round(L(1))))
		} else {
			drawText(b, it.title, fSrRowT, colWhite, a, rect{ttx, int32(ry + L(8)), trr, int32(ry + L(29))}, dtLine, 0)
			drawText(b, it.sub, fSrRowS, colWhite, 0.55*a, rect{ttx, int32(ry + L(29)), trr, int32(ry + L(47))}, dtLine, 0)
		}
		if i == srchSel {
			drawText(b, "↵", fSrRowT, colWhite, 0.5, rect{trr, int32(ry), int32(x1 - L(22)), int32(ry + L(srRowH))}, dtRight|dtVC|dtSingle, 0)
		}
	}

	// search box (bottom)
	fy0 := y1 - L(srFootH) - L(srFieldH)
	fillRect(b, x0, fy0, x1, fy0+math.Max(1, L(1)), colWhite, 0.07)
	fcy := fy0 + L(srFieldH/2)
	glyphSearch(b, x0+L(30), fcy, scale*1.05, rgb{170, 170, 182})
	tx := x0 + L(56)
	fieldR := x1 - L(110)
	q := string(srchQuery)
	if q == "" {
		drawText(b, "Search Spotify", fSrQuery, colWhite, 0.38, rect{int32(tx), int32(fy0), int32(fieldR), int32(fy0 + L(srFieldH))}, dtLine, 0)
		if srchCaret {
			fillRect(b, tx, fcy-L(11), tx+math.Max(1.5, L(1.6)), fcy+L(11), colWhite, 0.9)
		}
	} else {
		w := float64(measureOn(srchMaskDC, q, fSrQuery))
		flags := uintptr(dtLeft | dtVC | dtSingle | dtNoPrefix)
		cx := tx + w
		if w > fieldR-tx { // long query: keep the end visible
			flags, cx = dtRight|dtVC|dtSingle|dtNoPrefix, fieldR
		}
		if srchAll {
			fillRRect(b, math.Max(tx, cx-w)-L(2), fcy-L(13), cx+L(2), fcy+L(13), L(4), rgb{108, 99, 255}, 0.55)
		}
		drawText(b, q, fSrQuery, colWhite, 1, rect{int32(tx), int32(fy0), int32(fieldR), int32(fy0 + L(srFieldH))}, flags, 0)
		if srchCaret && !srchAll {
			fillRect(b, cx+L(1), fcy-L(11), cx+L(1)+math.Max(1.5, L(1.6)), fcy+L(11), colWhite, 0.9)
		}
	}
	bx := x1 - L(92)
	fillCircle(b, bx, fcy, L(4), colGreen, 1)
	drawText(b, "Spotify", fSrFoot, colWhite, 0.75, rect{int32(bx + L(10)), int32(fcy - L(10)), int32(x1 - L(18)), int32(fcy + L(10))}, dtLine, 0)

	// hints
	drawText(b, "↵  play in Spotify        ↑ ↓  choose        Esc  close", fSrFoot, colWhite, 0.40,
		rect{int32(x0 + L(22)), int32(y1 - L(srFootH)), int32(x1 - L(22)), int32(y1 - L(6))}, dtLine, 0)

	presentSearch()
}

func presentSearch() {
	wa := srchWork
	e := 1 - math.Pow(1-srchAlpha, 3)
	// card bottom lines up with where the on-screen cards sit
	cardBottom := int(wa.B) - int(L(30)) - int(L(margin))
	x := (int(wa.L)+int(wa.R))/2 - srchW/2
	y := cardBottom - (srchH - int(L(srMargin))) + int((1-e)*L(12))
	pt := point{int32(x), int32(y)}
	sz := size{int32(srchW), int32(srchH)}
	src := point{0, 0}
	blend := uint32(uint32(math.Round(e*255))<<16 | 1<<24)
	screen, _, _ := pGetDC.Call(0)
	pUpdateLayeredWindow.Call(srchWnd, screen, uintptr(unsafe.Pointer(&pt)), uintptr(unsafe.Pointer(&sz)), srchDC,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2)
	pReleaseDC.Call(0, screen)
}

// ---- typing & window ----

func clipboardText() string {
	if r, _, _ := pOpenClipboard.Call(srchWnd); r == 0 {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(13) // CF_UNICODETEXT
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	var units []uint16
	for i := 0; i < 2000; i++ {
		c := *(*uint16)(unsafe.Pointer(p + uintptr(i*2)))
		if c == 0 {
			break
		}
		units = append(units, c)
	}
	return strings.Join(strings.Fields(string(utf16.Decode(units))), " ")
}

func ctrlDown() bool {
	r, _, _ := pGetAsyncKeyState.Call(0x11)
	return r&0x8000 != 0
}

// queryChanged: redraw now, look up results once typing pauses briefly
func queryChanged() {
	srchSel, srchCaret, srchEnterWait = 0, true, false
	q := strings.TrimSpace(string(srchQuery))
	catMu.Lock()
	catGen++
	catLoading = q != ""
	catMu.Unlock()
	pKillTimer.Call(srchWnd, debounceTimer)
	if q != "" {
		pSetTimer.Call(srchWnd, debounceTimer, 220, 0)
	}
	renderSearch()
}

func typed(s string) {
	playClick()
	if srchAll {
		srchQuery, srchAll = nil, false
	}
	srchQuery = append(srchQuery, []rune(s)...)
	if len(srchQuery) > 200 {
		srchQuery = srchQuery[:200]
	}
	queryChanged()
}

func onSearchResults(gen int) {
	catMu.Lock()
	cur := gen == catGen
	catMu.Unlock()
	if cur && searchActive {
		renderSearch()
		if srchEnterWait && srchLabel != "SEARCHING…" {
			srchEnterWait = false
			activateItem(0) // the top result
		}
	}
}

func searchProc(h uintptr, m uint32, wParam, lParam uintptr) uintptr {
	defer guard("searchProc")
	switch m {
	case 0x6: // WM_ACTIVATE
		if wParam&0xFFFF == 0 { // lost focus: close
			hideSearch()
		}
		return 0
	case 0x102: // WM_CHAR
		if c := rune(wParam); c >= 32 && c != 0x7F && (c < 0xD800 || c > 0xDFFF) {
			typed(string(c))
		}
		return 0
	case 0x100: // WM_KEYDOWN
		switch wParam {
		case 0x1B: // Esc
			hideSearch()
		case 0x0D: // Enter
			if srchLabel == "SEARCHING…" && srchSel < len(srchItems) && srchItems[srchSel].kind == itemTrack {
				srchEnterWait = true // those are the previous results: wait for the new ones
				return 0
			}
			activateItem(srchSel)
		case 0x26: // Up
			if srchSel > 0 {
				srchSel--
			}
			renderSearch()
		case 0x28: // Down
			if srchSel < len(srchItems)-1 {
				srchSel++
			}
			renderSearch()
		case 0x08: // Backspace
			switch {
			case srchAll:
				srchQuery, srchAll = nil, false
			case ctrlDown(): // whole word
				q := strings.TrimRight(string(srchQuery), " ")
				if i := strings.LastIndex(q, " "); i >= 0 {
					srchQuery = []rune(q[:i+1])
				} else {
					srchQuery = nil
				}
			case len(srchQuery) > 0:
				srchQuery = srchQuery[:len(srchQuery)-1]
			}
			queryChanged()
		case 0x41: // Ctrl+A
			if ctrlDown() && len(srchQuery) > 0 {
				srchAll = true
				renderSearch()
			}
		case 0x56: // Ctrl+V
			if ctrlDown() {
				if t := clipboardText(); t != "" {
					typed(t)
				}
			}
		}
		return 0
	case 0x200: // WM_MOUSEMOVE
		x, y := int32(int16(lParam&0xFFFF)), int32(int16(lParam>>16&0xFFFF))
		hv := -1
		for i, r := range srchRowRects {
			if inRect(r, x, y) {
				hv = i
			}
		}
		ch := inRect(srchCloseR, x, y)
		if hv != srchHover || ch != srchCloseHov {
			srchHover, srchCloseHov = hv, ch
			renderSearch()
		}
		return 0
	case 0x201: // WM_LBUTTONDOWN: remember the row, act on release
		x, y := int32(int16(lParam&0xFFFF)), int32(int16(lParam>>16&0xFFFF))
		srchPressed = -1
		for i, r := range srchRowRects {
			if inRect(r, x, y) {
				srchPressed = i
				srchSel = i
				renderSearch()
			}
		}
		return 0
	case 0x202: // WM_LBUTTONUP: the release lands here, like pressing Enter
		x, y := int32(int16(lParam&0xFFFF)), int32(int16(lParam>>16&0xFFFF))
		if inRect(srchCloseR, x, y) {
			srchPressed = -1
			hideSearch()
			return 0
		}
		p := srchPressed
		srchPressed = -1
		if p >= 0 && p < len(srchRowRects) && inRect(srchRowRects[p], x, y) {
			activateItem(p)
		}
		return 0
	case 0x20: // WM_SETCURSOR
		if srchHover >= 0 || srchCloseHov {
			c, _, _ := pLoadCursorW.Call(0, 32649)
			pSetCursor.Call(c)
			return 1
		}
	case 0x113: // WM_TIMER
		switch wParam {
		case 1: // caret blink
			srchCaret = !srchCaret
			renderSearch()
		case 2: // fade in
			srchAlpha = math.Min(1, srchAlpha+0.2)
			presentSearch()
			if srchAlpha >= 1 {
				pKillTimer.Call(h, 2)
			}
		case debounceTimer:
			pKillTimer.Call(h, debounceTimer)
			if q := strings.TrimSpace(string(srchQuery)); q != "" {
				catMu.Lock()
				gen := catGen
				catMu.Unlock()
				startCatalogSearch(gen, q)
			}
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(m), wParam, lParam)
	return r
}

func setupSearch() {
	srchW = int(math.Ceil(L(srCardW + 2*srMargin)))
	srchH = int(math.Ceil(L(srLabelH + srMaxRows*srRowH + 6 + srFieldH + srFootH + 2*srMargin + 16)))
	sw, sh := winW, winH
	winW, winH = srchW, srchH
	srchDC, srchBits, srchPx = newDIB()
	srchMaskDC, srchMB, srchMask = newDIB()
	winW, winH = sw, sh
	fSrQuery = mkFont(18, 400)
	fSrRowT = mkFont(14.5, 600)
	fSrRowTB = mkFont(15, 800)
	fSrRowTag = mkFont(10.5, 700)
	fSrRowS = mkFont(12.5, 400)
	fSrLabel = mkFont(10.5, 700)
	fSrFoot = mkFont(11.5, 400)

	inst, _, _ := pGetModuleHandleW.Call(0)
	cls := u16("MediaKeysSearch")
	wc := wndclassex{WndProc: syscall.NewCallback(searchProc), Instance: inst, ClassName: cls}
	wc.Cursor, _, _ = pLoadCursorW.Call(0, 32512)
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const exStyle = 0x8 | 0x80 | 0x80000 // topmost, toolwindow, layered
	srchWnd, _, _ = pCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("MediaKeys search"))),
		0x80000000, 0, 0, uintptr(srchW), uintptr(srchH), 0, 0, inst, 0)
	srchReady = true
}

// force our window to the front even though another app has focus
func forceForeground(h uintptr) {
	fg, _, _ := pGetForegroundWindow.Call()
	fgThread, _, _ := pGetWindowThreadPid.Call(fg, 0)
	me, _, _ := pGetCurrentThreadId.Call()
	if fgThread != 0 && fgThread != me {
		pAttachThreadInput.Call(fgThread, me, 1)
		defer pAttachThreadInput.Call(fgThread, me, 0)
	}
	pSetForegroundWnd.Call(h)
	pBringWindowToTop.Call(h)
	pSetActiveWindow.Call(h)
	pSetFocus.Call(h)
	if now, _, _ := pGetForegroundWindow.Call(); now != h {
		logf("search: couldn't take focus on first try")
		pSetForegroundWnd.Call(h)
	}
}

func openSearch() {
	defer guard("openSearch")
	if !srchReady {
		setupSearch()
	}
	if searchActive {
		hideSearch() // pressing the shortcut again closes it
		return
	}
	holdUntil = 0            // let any on-screen card fade out; the search bar takes its place
	startPlaylistScan(false) // refresh your playlists in the background
	srchQuery, srchAll, srchSel, srchHover, srchCaret, srchCloseHov = nil, false, 0, -1, true, false
	catMu.Lock()
	catGen++
	catLoading, catErr, catQuery = false, false, ""
	catResults = nil
	catMu.Unlock()
	srchAlpha = 0.5
	srchWork = activeWorkArea() // the screen you're on
	if fg, _, _ := pGetForegroundWindow.Call(); fg != srchWnd && fg != setWnd {
		srchPrevFg = fg // to return here after a song starts
	}
	renderSearch()
	pShowWindow.Call(srchWnd, 5)
	forceForeground(srchWnd)
	searchActive = true
	pSetTimer.Call(srchWnd, 1, 530, 0)
	pSetTimer.Call(srchWnd, 2, 15, 0)
}

func hideSearch() {
	if srchWnd == 0 || !searchActive {
		return
	}
	searchActive = false
	for _, t := range []uintptr{1, 2, debounceTimer} {
		pKillTimer.Call(srchWnd, t)
	}
	pShowWindow.Call(srchWnd, 0)
}

// three lines and a small play mark: a playlist
func glyphPlaylist(b []uint32, cx, cy, u float64, c rgb) {
	fillRRect(b, cx-9*u, cy-7*u, cx+5*u, cy-5*u, 1*u, c, 1)
	fillRRect(b, cx-9*u, cy-1*u, cx+5*u, cy+1*u, 1*u, c, 1)
	fillRRect(b, cx-9*u, cy+5*u, cx-1*u, cy+7*u, 1*u, c, 1)
	tri(b, cx+2*u, cy+2.5*u, cx+9*u, cy+6*u, cx+2*u, cy+9.5*u, c)
}
