package main

// On-screen card: a per-pixel-alpha layered window rendered in software, so
// edges are anti-aliased, it can fade/slide, and parts can animate.
//
// Layout (logical px, scaled by DPI):
//   ┌────────────────────────────────────────────────┐
//   │ [art 60]  EYEBROW                         ( ◉ ) │  card 360×84, radius 22
//   │           Title                                 │  background tinted from art
//   │           Subtitle                              │
//   └────────────────────────────────────────────────┘

import (
	"math"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var (
	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	pCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	pDeleteObject          = gdi32.NewProc("DeleteObject")
	pSelectObject          = gdi32.NewProc("SelectObject")
	pSetBkMode             = gdi32.NewProc("SetBkMode")
	pSetTextColor          = gdi32.NewProc("SetTextColor")
	pSetTextCharacterExtra = gdi32.NewProc("SetTextCharacterExtra")
	pCreateFontW           = gdi32.NewProc("CreateFontW")
	pDrawTextW             = user32.NewProc("DrawTextW")
	pShowWindow            = user32.NewProc("ShowWindow")
	pSetTimer              = user32.NewProc("SetTimer")
	pKillTimer             = user32.NewProc("KillTimer")
	pGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	pUpdateLayeredWindow   = user32.NewProc("UpdateLayeredWindow")
	pSystemParametersInfo  = user32.NewProc("SystemParametersInfoW")
	pGetDC                 = user32.NewProc("GetDC")
	pReleaseDC             = user32.NewProc("ReleaseDC")
	pGetTickCount64        = kernel32.NewProc("GetTickCount64")
)

type rect struct{ L, T, R, B int32 }
type size struct{ CX, CY int32 }

type bitmapinfo struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [1]uint32
}

type rgb struct{ R, G, B float64 }

var (
	colCard  = rgb{20, 20, 24}
	colTint  = rgb{14, 14, 18}
	colWhite = rgb{255, 255, 255}
	colGreen = rgb{30, 215, 96}
	colBadge = rgb{236, 236, 240}
	colGlyph = rgb{16, 16, 20}
	colBlack = rgb{0, 0, 0}
)

const (
	cardW, cardH = 360.0, 84.0
	cardR        = 22.0
	artSize      = 60.0
	artX, artY   = 12.0, 12.0
	textX        = 86.0
	btnR         = 17.0
	margin       = 22.0 // room for the shadow
)

const (
	modeVolume = iota
	modePlayback
	modeMessage
)

var (
	osdWnd           uintptr
	scale            = 1.0
	winW, winH       int
	buf, base        []uint32
	staticBg         []uint32
	maskDC, maskBits uintptr
	mask             []uint32
	outDC, outBits   uintptr
	out              []uint32
	fontEyebrow      uintptr
	fontTitle        uintptr
	fontSub          uintptr
	fontPct          uintptr

	// content
	osdMode    int
	osdEyebrow string
	osdTitle   string
	osdSub     string
	osdPaused  bool
	osdPct     int
	osdSpotify bool
	baseDirty  = true

	osdAction     int      // skip card: actNext / actPrev / actFwd / actBack, or actAudio
	osdIcon       []uint32 // app icon for Volume Mixer apps (premultiplied)
	osdIconN      int
	osdMuted      bool
	osdFromMedia  bool
	osdArt        []uint32
	osdArtN       int
	osdArtVersion int
	osdMediaTitle string

	// animation
	animOn    bool
	alpha     float64 // 0..1
	barShown  float64 // 0..100
	barTarget float64
	holdUntil uint64
	lastTick  uint64
	lastAlpha = -1.0
	lastBar   = -1.0
)

func tick() uint64 { r, _, _ := pGetTickCount64.Call(); return uint64(r) }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func L(v float64) float64 { return v * scale } // logical -> device px

// blend a colour with coverage a (0..1) into a premultiplied BGRA pixel
func blendPx(px *uint32, c rgb, a float64) {
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	d := *px
	db, dg, dr, da := float64(d&0xFF), float64(d>>8&0xFF), float64(d>>16&0xFF), float64(d>>24)
	ia := 1 - a
	*px = uint32(255*a+da*ia+0.5)<<24 | uint32(c.R*a+dr*ia+0.5)<<16 | uint32(c.G*a+dg*ia+0.5)<<8 | uint32(c.B*a+db*ia+0.5)
}

func rrSDF(px, py, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hw, hh := (x1-x0)/2, (y1-y0)/2
	qx := math.Abs(px-cx) - (hw - r)
	qy := math.Abs(py-cy) - (hh - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func fillRRect(b []uint32, x0, y0, x1, y1, r float64, c rgb, a float64) {
	for y := int(y0) - 1; y <= int(y1)+1; y++ {
		if y < 0 || y >= winH {
			continue
		}
		for x := int(x0) - 1; x <= int(x1)+1; x++ {
			if x < 0 || x >= winW {
				continue
			}
			cov := clamp01(0.5 - rrSDF(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, r))
			blendPx(&b[y*winW+x], c, cov*a)
		}
	}
}

func fillCircle(b []uint32, cx, cy, r float64, c rgb, a float64) {
	fillRRect(b, cx-r, cy-r, cx+r, cy+r, r, c, a)
}

// supersampled fill of an arbitrary shape inside a bounding box
func fillShape(b []uint32, x0, y0, x1, y1 float64, inside func(x, y float64) bool, c rgb, a float64) {
	const n = 4
	for y := int(y0) - 1; y <= int(y1)+1; y++ {
		if y < 0 || y >= winH {
			continue
		}
		for x := int(x0) - 1; x <= int(x1)+1; x++ {
			if x < 0 || x >= winW {
				continue
			}
			hits := 0
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					if inside(float64(x)+(float64(sx)+0.5)/n, float64(y)+(float64(sy)+0.5)/n) {
						hits++
					}
				}
			}
			if hits > 0 {
				blendPx(&b[y*winW+x], c, float64(hits)/(n*n)*a)
			}
		}
	}
}

func inPoly(pts [][2]float64) func(x, y float64) bool {
	return func(x, y float64) bool {
		pos, neg := false, false
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			cr := (b[0]-a[0])*(y-a[1]) - (b[1]-a[1])*(x-a[0])
			if cr > 0 {
				pos = true
			} else if cr < 0 {
				neg = true
			}
		}
		return !(pos && neg)
	}
}

// ---- glyphs (u = size unit in device px) ----

func glyphPause(b []uint32, cx, cy, u float64, c rgb) {
	fillRRect(b, cx-5.6*u, cy-7*u, cx-1.8*u, cy+7*u, 1.3*u, c, 1)
	fillRRect(b, cx+1.8*u, cy-7*u, cx+5.6*u, cy+7*u, 1.3*u, c, 1)
}

func glyphPlay(b []uint32, cx, cy, u float64, c rgb) {
	fillShape(b, cx-6*u, cy-9*u, cx+9*u, cy+9*u, inPoly([][2]float64{
		{cx - 4.5*u, cy - 7.5*u}, {cx + 7.5*u, cy}, {cx - 4.5*u, cy + 7.5*u}}), c, 1)
}

func tri(b []uint32, ax, ay, bx, by, cx, cy float64, c rgb) {
	fillShape(b, math.Min(ax, math.Min(bx, cx)), math.Min(ay, math.Min(by, cy)), math.Max(ax, math.Max(bx, cx)), math.Max(ay, math.Max(by, cy)),
		inPoly([][2]float64{{ax, ay}, {bx, by}, {cx, cy}}), c, 1)
}

// d = +1 points right, -1 points left
func glyphSkip(b []uint32, cx, cy, u, d float64, c rgb) {
	tri(b, cx-4.5*u*d, cy-6*u, cx+2.5*u*d, cy, cx-4.5*u*d, cy+6*u, c)
	bx := cx + 3.6*u*d
	fillRRect(b, bx-1.1*u, cy-6*u, bx+1.1*u, cy+6*u, 1*u, c, 1)
}

func glyphSeek(b []uint32, cx, cy, u, d float64, c rgb) {
	tri(b, cx-7*u*d, cy-5.5*u, cx, cy, cx-7*u*d, cy+5.5*u, c)
	tri(b, cx, cy-5.5*u, cx+7*u*d, cy, cx, cy+5.5*u, c)
}

func glyphSpeaker(b []uint32, cx, cy, u float64, pct int, c rgb) {
	ox := cx - 3*u
	fillRRect(b, ox-9*u, cy-4*u, ox-4.5*u, cy+4*u, 1.2*u, c, 1)
	fillShape(b, ox-6*u, cy-9*u, ox+1*u, cy+9*u, inPoly([][2]float64{
		{ox - 5*u, cy - 4*u}, {ox + 0.5*u, cy - 8.5*u}, {ox + 0.5*u, cy + 8.5*u}, {ox - 5*u, cy + 4*u}}), c, 1)
	waves := 0
	if pct > 0 {
		waves = 1
	}
	if pct >= 50 {
		waves = 2
	}
	for w := 0; w < waves; w++ {
		r := (5.0 + 4.5*float64(w)) * u
		acx := ox + 0.5*u
		fillShape(b, acx, cy-r-2*u, acx+r+2*u, cy+r+2*u, func(x, y float64) bool {
			dx, dy := x-acx, y-cy
			return dx > 0 && math.Abs(math.Hypot(dx, dy)-r) <= 1.1*u && math.Abs(math.Atan2(dy, dx)) < 0.85
		}, c, 1)
	}
	if pct == 0 {
		x0 := ox + 4*u
		fillShape(b, x0, cy-5*u, x0+9*u, cy+5*u, func(x, y float64) bool {
			dx, dy := x-(x0+4*u), y-cy
			return math.Abs(dx) < 4*u && math.Abs(dy) < 4*u && (math.Abs(dx-dy) < 1.5*u || math.Abs(dx+dy) < 1.5*u)
		}, c, 1)
	}
}

// ---- text: GDI draws into a mask, which we use as coverage ----

func drawText(b []uint32, s string, font uintptr, c rgb, a float64, r rect, flags uintptr, spacing int) {
	for y := int(r.T); y < int(r.B) && y < winH; y++ { // DrawText clips to r, so only r needs clearing
		if y < 0 {
			continue
		}
		x0, x1 := int(r.L), int(r.R)
		if x0 < 0 {
			x0 = 0
		}
		if x1 > winW {
			x1 = winW
		}
		for x := x0; x < x1; x++ {
			mask[y*winW+x] = 0
		}
	}
	pSelectObject.Call(maskDC, font)
	pSetTextColor.Call(maskDC, 0x00FFFFFF)
	pSetBkMode.Call(maskDC, 1)
	pSetTextCharacterExtra.Call(maskDC, uintptr(spacing))
	p, _ := syscall.UTF16FromString(s)
	pDrawTextW.Call(maskDC, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), flags)
	pSetTextCharacterExtra.Call(maskDC, 0)
	for y := int(r.T); y < int(r.B) && y < winH; y++ {
		for x := int(r.L); x < int(r.R) && x < winW; x++ {
			if y < 0 || x < 0 {
				continue
			}
			m := mask[y*winW+x]
			if cov := float64((m>>16&0xFF)+(m>>8&0xFF)+(m&0xFF)) / 765; cov > 0 {
				blendPx(&b[y*winW+x], c, cov*a)
			}
		}
	}
}

const (
	dtLeft, dtRight, dtVC, dtSingle, dtEllipsis, dtNoPrefix = 0, 2, 4, 0x20, 0x8000, 0x800
	dtLine                                                  = dtLeft | dtVC | dtSingle | dtEllipsis | dtNoPrefix
)

// ---- layers ----

func cardRect() (x0, y0, x1, y1 float64) {
	return L(margin), L(margin), L(margin + cardW), L(margin + cardH)
}

// shadow + neutral card never change, so they're drawn once
func renderStatic() {
	staticBg = make([]uint32, winW*winH)
	x0, y0, x1, y1 := cardRect()
	rad := L(cardR)
	sh := L(20)
	for y := 0; y < winH; y++ {
		for x := 0; x < winW; x++ {
			d := rrSDF(float64(x)+0.5, float64(y)+0.5-L(5), x0, y0, x1, y1, rad)
			if d > -L(4) && d < sh {
				t := 1 - clamp01((d+L(4))/(sh+L(4)))
				blendPx(&staticBg[y*winW+x], colBlack, 0.34*t*t)
			}
		}
	}
	fillRRect(staticBg, x0, y0, x1, y1, rad, colCard, 1)
	cardCov = make([]float32, winW*winH)
	for y := 0; y < winH; y++ {
		for x := 0; x < winW; x++ {
			cardCov[y*winW+x] = float32(clamp01(0.5 - rrSDF(float64(x)+0.5, float64(y)+0.5, x0+L(1), y0+L(1), x1-L(1), y1-L(1), rad-L(1))))
		}
	}
}

// card background tinted with the cover art's colours (a cheap, very soft blur)
func paintTint(b []uint32, src []uint32, n int) {
	const g = 5
	var grid [g][g]rgb
	for gy := 0; gy < g; gy++ {
		for gx := 0; gx < g; gx++ {
			var r, gg, bb, cnt float64
			for y := gy * n / g; y < (gy+1)*n/g; y++ {
				for x := gx * n / g; x < (gx+1)*n/g; x++ {
					p := src[y*n+x]
					r += float64(p >> 16 & 0xFF)
					gg += float64(p >> 8 & 0xFF)
					bb += float64(p & 0xFF)
					cnt++
				}
			}
			if cnt > 0 {
				grid[gy][gx] = rgb{r / cnt, gg / cnt, bb / cnt}
			}
		}
	}
	smooth := func(t float64) float64 { return t * t * (3 - 2*t) }
	sample := func(u, v float64) rgb {
		fx, fy := u*(g-1), v*(g-1)
		ix, iy := int(fx), int(fy)
		if ix >= g-1 {
			ix = g - 2
		}
		if iy >= g-1 {
			iy = g - 2
		}
		tx, ty := smooth(fx-float64(ix)), smooth(fy-float64(iy))
		lerp := func(a, b rgb, t float64) rgb {
			return rgb{a.R + (b.R-a.R)*t, a.G + (b.G-a.G)*t, a.B + (b.B-a.B)*t}
		}
		return lerp(lerp(grid[iy][ix], grid[iy][ix+1], tx), lerp(grid[iy+1][ix], grid[iy+1][ix+1], tx), ty)
	}
	x0, y0, x1, y1 := cardRect()
	rad := L(cardR)
	for y := int(y0); y <= int(y1); y++ {
		for x := int(x0); x <= int(x1); x++ {
			if x < 0 || y < 0 || x >= winW || y >= winH {
				continue
			}
			cov := clamp01(0.5 - rrSDF(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, rad))
			if cov <= 0 {
				continue
			}
			s := sample((float64(x)-x0)/(x1-x0), (float64(y)-y0)/(y1-y0))
			c := rgb{colTint.R + (s.R-colTint.R)*0.42, colTint.G + (s.G-colTint.G)*0.42, colTint.B + (s.B-colTint.B)*0.42}
			blendPx(&b[y*winW+x], c, cov)
		}
	}
}

func paintArt(b []uint32) {
	n := osdArtN
	ax := int(math.Round(L(margin + artX)))
	ay := int(math.Round(L(margin) + (L(cardH)-float64(n))/2))
	r := L(10)
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			wx, wy := ax+i, ay+j
			if wx < 0 || wy < 0 || wx >= winW || wy >= winH {
				continue
			}
			cov := clamp01(0.5 - rrSDF(float64(wx)+0.5, float64(wy)+0.5, float64(ax), float64(ay), float64(ax+n), float64(ay+n), r))
			if cov > 0 {
				p := osdArt[j*n+i]
				blendPx(&b[wy*winW+wx], rgb{float64(p >> 16 & 0xFF), float64(p >> 8 & 0xFF), float64(p & 0xFF)}, cov)
			}
		}
	}
}

// fallback when there's no cover art: a rounded tile with an icon
func paintTile(b []uint32) {
	x0, y0 := L(margin+artX), L(margin+artY)
	if osdMode == modeMessage {
		drawAppIcon(b, x0, y0, L(artSize))
		return
	}
	x1, y1 := x0+L(artSize), y0+L(artSize)
	tile := colBadge
	if osdSpotify {
		tile = colGreen
	}
	fillRRect(b, x0, y0, x1, y1, L(10), tile, 1)
	cx, cy := (x0+x1)/2, (y0+y1)/2
	switch {
	case osdMode == modeVolume:
		glyphSpeaker(b, cx, cy, scale, osdPct, colGlyph)
	case osdPaused:
		glyphPause(b, cx, cy, scale*1.1, colGlyph)
	default:
		glyphPlay(b, cx, cy, scale*1.1, colGlyph)
	}
}

// renderLayers draws the card background (bg) and its content (fg) separately,
// so content can slide while the background cross-fades.
func renderLayers(bg, fg []uint32) {
	copy(bg, staticBg)
	for i := range fg {
		fg[i] = 0
	}
	if osdArt != nil {
		paintTint(bg, osdArt, osdArtN)
	} else if osdIcon != nil {
		paintTint(bg, iconOverDark(), osdIconN)
	}
	x0, y0, x1, _ := cardRect()
	// hairline edge
	fillShape(bg, x0-1, y0-1, x1+1, y0+L(cardH)+1, func(x, y float64) bool {
		d := rrSDF(x, y, x0, y0, x1, y0+L(cardH), L(cardR))
		return d <= 0 && d > -L(1)
	}, colWhite, 0.10)

	switch {
	case osdArt != nil:
		paintArt(fg)
	case osdIcon != nil:
		paintIcon(fg)
	default:
		paintTile(fg)
	}

	tl := int32(x0 + L(textX))
	tr := int32(x1 - L(64)) // room for the round button on the right
	if osdMode == modeVolume || osdMode == modeMessage {
		tr = int32(x1 - L(22)) // no button: text can use the full width
	}
	top := int32(y0)
	ls := int(math.Round(L(0.9)))
	switch {
	case osdMode == modeVolume:
		drawText(fg, osdEyebrow, fontEyebrow, colWhite, 0.62, rect{tl, top + int32(L(20)), tr - int32(L(60)), top + int32(L(38))}, dtLine, ls)
		drawText(fg, strconv.Itoa(osdPct)+"%", fontPct, colWhite, 1, rect{tr - int32(L(70)), top + int32(L(17)), tr, top + int32(L(40))}, dtRight|dtVC|dtSingle, 0)
	case osdEyebrow != "":
		drawText(fg, osdEyebrow, fontEyebrow, colWhite, 0.62, rect{tl, top + int32(L(13)), tr, top + int32(L(29))}, dtLine, ls)
		drawText(fg, osdTitle, fontTitle, colWhite, 1, rect{tl, top + int32(L(28)), tr, top + int32(L(51))}, dtLine, 0)
		if osdSub != "" {
			drawText(fg, osdSub, fontSub, colWhite, 0.72, rect{tl, top + int32(L(51)), tr, top + int32(L(71))}, dtLine, 0)
		}
	case osdSub != "":
		drawText(fg, osdTitle, fontTitle, colWhite, 1, rect{tl, top + int32(L(19)), tr, top + int32(L(42))}, dtLine, 0)
		drawText(fg, osdSub, fontSub, colWhite, 0.72, rect{tl, top + int32(L(42)), tr, top + int32(L(63))}, dtLine, 0)
	default:
		drawText(fg, osdTitle, fontTitle, colWhite, 1, rect{tl, top, tr, top + int32(L(cardH))}, dtLine, 0)
	}
}

// parts that move: volume bar, play/pause button with equalizer
func renderFrame() {
	if baseDirty {
		trans := pendingTrans
		pendingTrans = transUnset
		if trans == transUnset {
			trans = transFade
		}
		if trans != transNone && haveLayers {
			copy(bgOld, bgCur)
			copy(fgOld, fgCur)
			transDir, transStart, transOn = trans, tick(), true
		}
		renderLayers(bgCur, fgCur)
		composeStill(base)
		haveLayers, baseDirty = true, false
	}
	if transOn {
		t := float64(tick()-transStart) / transMs
		if t >= 1 {
			transOn = false
			copy(buf, base)
		} else {
			composeTrans(buf, easeInOut(clamp01(t)))
		}
	} else {
		copy(buf, base)
	}
	x0, y0, x1, _ := cardRect()
	switch osdMode {
	case modeVolume:
		bx0, bx1 := x0+L(textX), x1-L(20)
		by0, by1 := y0+L(48), y0+L(54)
		fillRRect(buf, bx0, by0, bx1, by1, L(3), colWhite, 0.16)
		accent := colWhite
		if osdSpotify {
			accent = colGreen
		}
		if w := (bx1 - bx0) * barShown / 100; w > L(1) {
			fillRRect(buf, bx0, by0, bx0+math.Max(w, L(6)), by1, L(3), accent, 1)
		}
	case modePlayback:
		cx, cy := x1-L(34), y0+L(cardH/2)
		fillCircle(buf, cx, cy, L(btnR), colWhite, 0.13)
		if osdAction != actNone {
			switch osdAction {
			case actAudio:
				pct := 60
				if osdMuted {
					pct = 0
				}
				glyphSpeaker(buf, cx+L(1), cy, scale*0.9, pct, colWhite)
			case actNext:
				glyphSkip(buf, cx, cy, scale, 1, colWhite)
			case actPrev:
				glyphSkip(buf, cx, cy, scale, -1, colWhite)
			case actFwd:
				glyphSeek(buf, cx, cy, scale, 1, colWhite)
			case actBack:
				glyphSeek(buf, cx, cy, scale, -1, colWhite)
			}
		} else if osdPaused {
			glyphPause(buf, cx, cy, scale, colWhite)
		} else {
			bar := colWhite
			if osdSpotify {
				bar = colGreen
			}
			t := float64(tick()) / 1000
			speeds := [4]float64{5.1, 6.7, 4.3, 5.9}
			phases := [4]float64{0, 1.3, 2.2, 0.7}
			for i := 0; i < 4; i++ {
				h := L(14) * (0.35 + 0.65*math.Abs(math.Sin(t*speeds[i]+phases[i])))
				bx := cx + L(-6.6+4.4*float64(i))
				fillRRect(buf, bx-L(1.3), cy+L(7)-h, bx+L(1.3), cy+L(7), L(1.3), bar, 1)
			}
		}
	}
	copy(out, buf)

	// bottom centre of the work area, sliding up as it fades in
	wa := osdWork
	e := 1 - math.Pow(1-alpha, 3)
	x := (int(wa.L)+int(wa.R))/2 - winW/2
	y := int(wa.B) - winH - int(L(30)) + int((1-e)*L(14))
	pt := point{int32(x), int32(y)}
	sz := size{int32(winW), int32(winH)}
	src := point{0, 0}
	blend := uint32(uint32(math.Round(e*255))<<16 | 1<<24) // AC_SRC_OVER, const alpha, AC_SRC_ALPHA
	screen, _, _ := pGetDC.Call(0)
	pUpdateLayeredWindow.Call(osdWnd, screen, uintptr(unsafe.Pointer(&pt)), uintptr(unsafe.Pointer(&sz)), outDC,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2)
	pReleaseDC.Call(0, screen)
}

func newDIB() (dc, bits uintptr, px []uint32) {
	bi := bitmapinfo{Width: int32(winW), Height: -int32(winH), Planes: 1, BitCount: 32}
	bi.Size = 40
	dc, _, _ = pCreateCompatibleDC.Call(0)
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	pSelectObject.Call(dc, bmp)
	px = unsafe.Slice((*uint32)(unsafe.Pointer(bits)), winW*winH)
	return
}

func osdProc(h uintptr, m uint32, wParam, lParam uintptr) uintptr {
	if m == 0x113 { // WM_TIMER
		defer guard("animate")
		animate()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(m), wParam, lParam)
	return r
}

func animate() {
	now := tick()
	dt := float64(now - lastTick)
	lastTick = now
	if dt > 100 {
		dt = 16
	}
	if now < holdUntil {
		alpha = clamp01(alpha + dt/90)
	} else {
		alpha = clamp01(alpha - dt/240)
	}
	barShown += (barTarget - barShown) * (1 - math.Exp(-dt/55))
	if math.Abs(barTarget-barShown) < 0.05 {
		barShown = barTarget
	}
	moving := transOn || (osdMode == modePlayback && !osdPaused && osdAction == actNone) // transition / equalizer
	if baseDirty || moving || alpha != lastAlpha || math.Abs(barShown-lastBar) > 0.01 {
		renderFrame()
		lastAlpha, lastBar = alpha, barShown
	}
	if alpha == 0 && now >= holdUntil {
		pKillTimer.Call(osdWnd, 1)
		pShowWindow.Call(osdWnd, 0)
		animOn = false
	}
}

func mkFont(px float64, weight int) uintptr {
	f, _, _ := pCreateFontW.Call(uintptr(-int32(math.Round(L(px)))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 4, 0,
		uintptr(unsafe.Pointer(u16("Segoe UI"))))
	return f
}

func createOSD(inst uintptr) {
	winW = int(math.Ceil(L(cardW + 2*margin)))
	winH = int(math.Ceil(L(cardH + 2*margin)))
	buf = make([]uint32, winW*winH)
	base = make([]uint32, winW*winH)
	bgCur, fgCur = make([]uint32, winW*winH), make([]uint32, winW*winH)
	bgOld, fgOld = make([]uint32, winW*winH), make([]uint32, winW*winH)
	maskDC, maskBits, mask = newDIB()
	outDC, outBits, out = newDIB()
	renderStatic()
	fontEyebrow = mkFont(11, 700)
	fontTitle = mkFont(16, 600)
	fontSub = mkFont(13.5, 400)
	fontPct = mkFont(17, 600)

	cls := u16("MediaKeysOSD")
	wc := wndclassex{WndProc: syscall.NewCallback(osdProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const exStyle = 0x8 | 0x80 | 0x80000 | 0x20 | 0x08000000 // topmost, toolwindow, layered, click-through, no-activate
	osdWnd, _, _ = pCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(cls)), 0, 0x80000000, 0, 0, uintptr(winW), uintptr(winH), 0, 0, inst, 0)
}

var osdWork rect

func startAnim() {
	if !settings.ShowCards {
		return
	}
	holdUntil = tick() + 1700
	if !animOn {
		osdWork = activeWorkArea() // the screen you're on
		pendingTrans = transNone   // card was hidden: nothing to transition from
		animOn = true
		lastTick = tick()
		lastAlpha = -1
		alpha = 0.6 // appear immediately, finish the fade over the next few frames
		renderFrame()
		pShowWindow.Call(osdWnd, 4) // SW_SHOWNOACTIVATE
		pSetTimer.Call(osdWnd, 1, 15, 0)
	}
}

func isSpotifyApp(app string) bool { return strings.Contains(strings.ToLower(app), "spotify") }

func showVolume(app string, pct int, fromMedia bool) {
	if osdWnd == 0 {
		return
	}
	wasVolume := animOn && osdMode == modeVolume
	if wasVolume {
		pendingTrans = transNone
	}
	osdMode, osdSpotify, osdPct = modeVolume, app == "Spotify", pct
	osdEyebrow, osdTitle, osdSub = strings.ToUpper(app)+" VOLUME", "", ""
	osdFromMedia, osdArt, osdAction, osdIcon = false, nil, actNone, nil
	if s := getSnap(); fromMedia && s.valid {
		if s.audioOnly {
			osdIcon, osdIconN = s.icon, s.iconN
		} else if s.art != nil {
			osdArt, osdArtN = s.art, s.artN // cover of what's playing in that app
		}
	}
	barTarget = float64(pct)
	if !wasVolume {
		barShown = barTarget // don't sweep from an old value when the card first appears
	}
	baseDirty = true
	startAnim()
}

// fallback playback card (no Windows media info)
func showPlayback(paused bool, song string, spotify bool) {
	if osdWnd == 0 {
		return
	}
	osdMode, osdPaused, osdSpotify = modePlayback, paused, spotify
	osdFromMedia, osdArt, osdAction, osdIcon = false, nil, actNone, nil
	state := "Playing"
	if paused {
		state = "Paused"
	}
	if song != "" {
		osdEyebrow, osdTitle, osdSub = strings.ToUpper(map[bool]string{true: "Paused", false: "Now playing"}[paused]), song, ""
		if i := strings.Index(song, " - "); i > 0 { // Spotify titles are "Artist - Song"
			osdTitle, osdSub = song[i+3:], song[:i]
		}
	} else {
		osdEyebrow, osdTitle, osdSub = "", state, ""
	}
	baseDirty = true
	startAnim()
}

// card from Windows' media info: eyebrow, song title, artist, cover art
func showMedia(paused bool, s mediaSnap) {
	if osdWnd == 0 {
		return
	}
	osdMode, osdPaused, osdAction, osdIcon = modePlayback, paused, actNone, nil
	osdFromMedia, osdArt, osdArtN, osdArtVersion, osdMediaTitle = true, s.art, s.artN, s.artVersion, s.title
	osdSpotify = isSpotifyApp(s.app)
	if s.title != "" {
		osdEyebrow = "NOW PLAYING"
		if paused {
			osdEyebrow = "PAUSED"
		}
		osdTitle, osdSub = s.title, s.artist
	} else {
		osdEyebrow, osdSub = "", ""
		osdTitle = map[bool]string{true: "Paused", false: "Playing"}[paused]
	}
	baseDirty = true
	startAnim()
}

func showMessage(title, sub string) {
	if osdWnd == 0 {
		return
	}
	osdMode, osdPaused, osdSpotify = modeMessage, true, true
	osdEyebrow, osdTitle, osdSub = "", title, sub
	osdFromMedia, osdArt, osdAction, osdIcon = false, nil, actNone, nil
	baseDirty = true
	startAnim()
}

// card after a skip: eyebrow says what happened, then the (new) song
func showSkip(act int, eyebrow string, s mediaSnap) {
	if osdWnd == 0 {
		return
	}
	osdMode, osdPaused, osdAction, osdIcon = modePlayback, false, act, nil
	osdFromMedia, osdArt, osdArtN, osdArtVersion, osdMediaTitle = true, s.art, s.artN, s.artVersion, s.title
	osdSpotify = isSpotifyApp(s.app)
	osdEyebrow = eyebrow
	if s.title != "" {
		osdTitle, osdSub = s.title, s.artist
	} else {
		osdTitle, osdSub = map[bool]string{true: "Skipped back", false: "Skipped"}[act == actPrev || act == actBack], ""
	}
	baseDirty = true
	startAnim()
}

// card after Shift+Left/Right: which app the keys now control, and what it's playing
func showSwitched(s mediaSnap, apps int) {
	if osdWnd == 0 {
		return
	}
	if s.audioOnly {
		name := s.title
		eyebrow := "CONTROLLING " + strings.ToUpper(name)
		if apps == 1 {
			eyebrow = "ONLY " + strings.ToUpper(name) + " IS MAKING SOUND"
		}
		sub := "Pause mutes · Shift ↑↓ volume"
		if s.muted {
			sub = "Muted  ·  Pause to unmute"
		}
		showAudioCard(s, eyebrow, name, sub)
		return
	}
	name := friendlyName(exeHint(s.app))
	if exeHint(s.app) == "" {
		name = "this app"
	}
	osdMode, osdAction, osdIcon = modePlayback, actNone, nil
	osdPaused = s.status != statusPlaying
	osdFromMedia, osdArt, osdArtN, osdArtVersion, osdMediaTitle = true, s.art, s.artN, s.artVersion, s.title
	osdSpotify = isSpotifyApp(s.app)
	osdEyebrow = "CONTROLLING " + strings.ToUpper(name)
	if apps == 1 {
		osdEyebrow = "ONLY " + strings.ToUpper(name) + " IS PLAYING MEDIA"
	}
	osdTitle, osdSub = s.title, s.artist
	if osdTitle == "" {
		osdTitle = name
	}
	baseDirty = true
	startAnim()
}

const actAudio = 100

// premultiplied icon composited over the dark card colour, for the tint
func iconOverDark() []uint32 {
	out := make([]uint32, len(osdIcon))
	for i, p := range osdIcon {
		a := float64(p>>24) / 255
		r := float64(p>>16&0xFF) + colTint.R*(1-a)
		g := float64(p>>8&0xFF) + colTint.G*(1-a)
		b := float64(p&0xFF) + colTint.B*(1-a)
		out[i] = 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	}
	return out
}

// app icon on a soft tile, in the cover-art spot
func paintIcon(b []uint32) {
	x0, y0 := L(margin+artX), L(margin+artY)
	x1, y1 := x0+L(artSize), y0+L(artSize)
	fillRRect(b, x0, y0, x1, y1, L(10), colWhite, 0.10)
	n := osdIconN
	ix := int(math.Round((x0+x1)/2 - float64(n)/2))
	iy := int(math.Round((y0+y1)/2 - float64(n)/2))
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			wx, wy := ix+i, iy+j
			if wx < 0 || wy < 0 || wx >= winW || wy >= winH {
				continue
			}
			p := osdIcon[j*n+i]
			a := float64(p>>24) / 255
			if a <= 0 {
				continue
			}
			// icon pixels are premultiplied: un-premultiply for blendPx
			blendPx(&b[wy*winW+wx], rgb{float64(p>>16&0xFF) / a, float64(p>>8&0xFF) / a, float64(p&0xFF) / a}, a)
		}
	}
}

// card for a Volume Mixer app (Discord, a game...)
func showAudioCard(s mediaSnap, eyebrow, title, sub string) {
	if osdWnd == 0 {
		return
	}
	osdMode, osdAction, osdPaused, osdSpotify = modePlayback, actAudio, true, false
	osdFromMedia, osdArt, osdIcon, osdIconN, osdMuted = false, nil, s.icon, s.iconN, s.muted
	osdEyebrow, osdTitle, osdSub = eyebrow, title, sub
	baseDirty = true
	startAnim()
}

// ---- transitions ----

const (
	transUnset = -99 // default: cross-fade if the card is showing
	transNone  = -98 // swap instantly
	transFade  = 0
	transNext  = 1  // old content slides out left, new comes in from the right
	transPrev  = -1 // the reverse
	transMs    = 300.0
)

var (
	bgCur, fgCur, bgOld, fgOld []uint32
	cardCov                    []float32
	haveLayers, transOn        bool
	transDir                   = 0
	transStart                 uint64
	pendingTrans               = transUnset
)

func easeInOut(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// premultiplied "over", with the source scaled by k
func overPx(dst *uint32, src uint32, k float64) {
	sa := float64(src>>24) * k
	if sa <= 0 {
		return
	}
	d := *dst
	ia := 1 - sa/255
	a := sa + float64(d>>24)*ia
	r := float64(src>>16&0xFF)*k + float64(d>>16&0xFF)*ia
	g := float64(src>>8&0xFF)*k + float64(d>>8&0xFF)*ia
	b := float64(src&0xFF)*k + float64(d&0xFF)*ia
	*dst = uint32(math.Min(a, 255)+0.5)<<24 | uint32(math.Min(r, 255)+0.5)<<16 | uint32(math.Min(g, 255)+0.5)<<8 | uint32(math.Min(b, 255)+0.5)
}

func composeStill(dst []uint32) {
	copy(dst, bgCur)
	for i, p := range fgCur {
		if p != 0 {
			overPx(&dst[i], p, 1)
		}
	}
}

func composeTrans(dst []uint32, e float64) {
	d := float64(transDir) * L(26)
	offOld := int(math.Round(d * e))        // old content drifts away
	offNew := int(math.Round(-d * (1 - e))) // new content arrives
	ie := 1 - e
	for y := 0; y < winH; y++ {
		row := y * winW
		for x := 0; x < winW; x++ {
			i := row + x
			a, b := bgOld[i], bgCur[i]
			var px uint32
			if a == b {
				px = a
			} else {
				px = uint32(float64(a>>24)*ie+float64(b>>24)*e+0.5)<<24 |
					uint32(float64(a>>16&0xFF)*ie+float64(b>>16&0xFF)*e+0.5)<<16 |
					uint32(float64(a>>8&0xFF)*ie+float64(b>>8&0xFF)*e+0.5)<<8 |
					uint32(float64(a&0xFF)*ie+float64(b&0xFF)*e+0.5)
			}
			if c := float64(cardCov[i]); c > 0 {
				if xo := x + offOld; xo >= 0 && xo < winW {
					if p := fgOld[row+xo]; p != 0 {
						overPx(&px, p, ie*c)
					}
				}
				if xn := x + offNew; xn >= 0 && xn < winW {
					if p := fgCur[row+xn]; p != 0 {
						overPx(&px, p, e*c)
					}
				}
			}
			dst[i] = px
		}
	}
}
