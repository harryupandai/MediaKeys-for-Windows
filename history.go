package main

// Songs you searched for and played from the Ctrl+Alt+X bar, newest first,
// kept in %APPDATA%\MediaKeys\searches.json (with small cover images).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type histEntry struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	App    string `json:"app"`
	At     int64  `json:"at"`
	Kind   string `json:"kind,omitempty"` // "playlist", or empty for a song
	URI    string `json:"uri,omitempty"`
	Art    string `json:"art,omitempty"` // small PNG, base64

	px []uint32 // decoded cover, histArt×histArt
}

const histMax = 40

var history []*histEntry

func histArtSize() int { return int(math.Round(40 * scale)) }

func (h *histEntry) key() string { return strings.ToLower(h.Kind + "|" + h.Title + "|" + h.Artist) }

func loadHistory() {
	b, err := os.ReadFile(filepath.Join(dataDir(), "searches.json"))
	if err != nil {
		return
	}
	var list []*histEntry
	if json.Unmarshal(b, &list) != nil {
		return
	}
	n := histArtSize()
	for _, h := range list {
		h.px = decodeArt(h.Art, n)
	}
	history = list
}

func saveHistory() {
	d := dataDir()
	if d == "" {
		return
	}
	os.MkdirAll(d, 0755)
	b, _ := json.Marshal(history)
	os.WriteFile(filepath.Join(d, "searches.json"), b, 0644)
}

// downscale an n×n cover to m×m (box filter)
func shrinkArt(src []uint32, n, m int) []uint32 {
	if len(src) != n*n || m <= 0 {
		return nil
	}
	out := make([]uint32, m*m)
	for y := 0; y < m; y++ {
		y0, y1 := y*n/m, (y+1)*n/m
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < m; x++ {
			x0, x1 := x*n/m, (x+1)*n/m
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, c uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					p := src[yy*n+xx]
					r += p >> 16 & 0xFF
					g += p >> 8 & 0xFF
					b += p & 0xFF
					c++
				}
			}
			out[y*m+x] = 0xFF000000 | (r/c)<<16 | (g/c)<<8 | b/c
		}
	}
	return out
}

func artToPNG(px []uint32, n int) string {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for i, p := range px {
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = byte(p>>16), byte(p>>8), byte(p), 255
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// recordSearchPlay puts a song you started from the search bar at the top
func recordSearchPlay(kind, title, artist, uri string, art []uint32) {
	if strings.TrimSpace(title) == "" {
		return
	}
	e := &histEntry{Title: title, Artist: artist, App: "Spotify.exe", At: time.Now().Unix(), Kind: kind, URI: uri}
	k := e.key()
	for i, h := range history {
		if h.key() == k {
			if art == nil {
				art = h.px // keep the cover we already had
			}
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	if n := histArtSize(); art != nil && len(art) == n*n {
		e.px = art
		e.Art = artToPNG(art, n)
	}
	history = append([]*histEntry{e}, history...)
	if len(history) > histMax {
		history = history[:histMax]
	}
	saveHistory()
}

func timeAgo(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + " hr ago"
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return itoa(int(d.Hours()/24)) + " days ago"
	}
	return time.Unix(unix, 0).Format("Jan 2")
}

func itoa(v int) string {
	if v <= 0 {
		return "1"
	}
	s := ""
	for v > 0 {
		s = string(rune('0'+v%10)) + s
		v /= 10
	}
	return s
}

func decodeArt(b64 string, n int) []uint32 {
	if b64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return scaleSquare(img, n)
}
