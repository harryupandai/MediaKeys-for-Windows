package main

// Your own Spotify playlists, for the search bar. Spotify doesn't let apps list
// them without a login, so MediaKeys reads them from the "Your Library" sidebar
// in the Spotify app (Windows accessibility, like a screen reader would) and
// remembers them in %APPDATA%\MediaKeys\playlists.json.

import (
	"encoding/json"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const WM_PLAYLISTS = WM_APP + 24 // a scan finished

type playlistEntry struct {
	Title string `json:"title"`
	Owner string `json:"owner"`
	URI   string `json:"uri,omitempty"` // spotify:playlist:<id>, when Spotify exposes it
	Seen  int64  `json:"seen"`
	Art   string `json:"art,omitempty"`   // cover, small PNG (base64)
	NoArt bool   `json:"noArt,omitempty"` // Spotify has no public preview (private playlist)
	Gone  int    `json:"gone,omitempty"`  // reads in a row it wasn't in your library

	px []uint32
}

type playlistStore struct {
	You       string          `json:"you"` // your Spotify display name, worked out from the owners
	Playlists []playlistEntry `json:"playlists"`
}

var (
	plStore    playlistStore
	plLastScan time.Time
	plScanning bool

	plScanMu     sync.Mutex
	plScanResult []playlistEntry

	reLibRow  = regexp.MustCompile(`(?i)^(?:pinned\s+)?(.*?)\s*playlist\s*[•·]\s*(.+)$`)
	rePlaylID = regexp.MustCompile(`playlist[:/]([A-Za-z0-9]{22})`)
	reIDLabel = regexp.MustCompile(`spotify:playlist:([A-Za-z0-9]{22})`)
)

func loadPlaylists() {
	b, err := os.ReadFile(filepath.Join(dataDir(), "playlists.json"))
	if err == nil {
		json.Unmarshal(b, &plStore)
	}
	n := histArtSize()
	for i := range plStore.Playlists {
		plStore.Playlists[i].px = decodeArt(plStore.Playlists[i].Art, n)
	}
}

func savePlaylists() {
	d := dataDir()
	if d == "" {
		return
	}
	os.MkdirAll(d, 0755)
	b, _ := json.MarshalIndent(plStore, "", "  ")
	os.WriteFile(filepath.Join(d, "playlists.json"), b, 0644)
}

// ---- reading the sidebar (background thread) ----

func scanSpotifyLibrary() []playlistEntry {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0)
	h := findSpotifyHwnd()
	if h == 0 {
		return nil
	}
	var ua uintptr
	if r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAuto)), 0, 1|4,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&ua))); r != 0 || ua == 0 {
		return nil
	}
	defer release(ua)
	var all, linkCond uintptr
	comCall(ua, 21, uintptr(unsafe.Pointer(&all))) // CreateTrueCondition
	if all == 0 {
		return nil
	}
	defer release(all)
	v := variant{VT: 3, Val: 50005} // UIA_HyperlinkControlTypeId
	comCall(ua, 23, 30003, uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&linkCond)))
	if linkCond != 0 {
		defer release(linkCond)
	}

	var root uintptr
	if comCall(ua, 6, h, uintptr(unsafe.Pointer(&root))) != 0 || root == 0 {
		return nil
	}
	defer release(root)
	var arr uintptr
	comCall(root, 6, 4, all, uintptr(unsafe.Pointer(&arr))) // FindAll descendants
	if arr == 0 {
		return nil
	}
	defer release(arr)
	var n int32
	comCall(arr, 3, uintptr(unsafe.Pointer(&n)))

	var found []playlistEntry
	seen := map[string]bool{}
	idByTitle := map[string]string{} // from Spotify's hidden labels, e.g. "listrow-title-spotify:playlist:<id>"
	samples := 0
	for i := int32(0); i < n && i < 8000; i++ {
		var el uintptr
		comCall(arr, 4, uintptr(i), uintptr(unsafe.Pointer(&el)))
		if el == 0 {
			continue
		}
		var bn, ba uintptr
		comCall(el, 23, uintptr(unsafe.Pointer(&bn)))
		name := strings.TrimSpace(takeBSTR(bn))
		comCall(el, 29, uintptr(unsafe.Pointer(&ba))) // get_CurrentAutomationId (the element's HTML id)
		if aid := takeBSTR(ba); aid != "" && name != "" {
			if id := reIDLabel.FindStringSubmatch(aid); id != nil {
				t := name
				if mm := reLibRow.FindStringSubmatch(name); mm != nil {
					t = mm[1]
				}
				idByTitle[strings.ToLower(strings.TrimSpace(t))] = "spotify:playlist:" + id[1]
			}
		}
		m := reLibRow.FindStringSubmatch(name)
		if m == nil || len(name) > 200 {
			release(el)
			continue
		}
		title := strings.TrimSpace(m[1])
		if t := strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(title, "Pinned")), " ·"); len(t) < len(title) && t != "" {
			title = strings.TrimSpace(t) // "Gym Pinned Playlist • you"
		}
		owner := strings.TrimSpace(strings.SplitN(strings.SplitN(m[2], "•", 2)[0], "·", 2)[0])
		if title == "" || seen[strings.ToLower(title)] {
			release(el)
			continue
		}
		seen[strings.ToLower(title)] = true
		e := playlistEntry{Title: title, Owner: owner, Seen: time.Now().Unix()}
		// the row usually contains a link to the playlist: that gives its exact ID
		if linkCond != 0 {
			var link uintptr
			comCall(el, 5, 4, linkCond, uintptr(unsafe.Pointer(&link))) // FindFirst
			if link != 0 {
				var val variant
				comCall(link, 10, 30045, uintptr(unsafe.Pointer(&val))) // Value property
				if val.VT == 8 {
					if id := rePlaylID.FindStringSubmatch(takeBSTR(uintptr(val.Val))); id != nil {
						e.URI = "spotify:playlist:" + id[1]
					}
				}
				release(link)
			}
		}
		found = append(found, e)
		if samples < 3 {
			samples++
			logf("playlists: found a library row (owner %q, link %v)", owner, e.URI != "")
		}
		release(el)
	}
	withID := 0
	for i := range found {
		if found[i].URI == "" {
			found[i].URI = idByTitle[strings.ToLower(found[i].Title)]
		}
		if found[i].URI != "" {
			withID++
		}
	}
	logf("playlists: scanned %d elements, %d playlists, %d with their Spotify ID", n, len(found), withID)
	return found
}

// startPlaylistScan reads the library in the background (at most once a minute)
func startPlaylistScan(force bool) {
	if plScanning || (!force && time.Since(plLastScan) < time.Minute) {
		return
	}
	plScanning = true
	plLastScan = time.Now()
	go func() {
		defer guard("playlistScan")
		res := scanSpotifyLibrary()
		plScanMu.Lock()
		plScanResult = res
		plScanMu.Unlock()
		pPostMessageW.Call(hwnd, WM_PLAYLISTS, 0, 0)
	}()
}

// main thread: merge what the scan found (the sidebar only shows part of a long
// library at a time, so playlists are added up over time rather than replaced)
func onPlaylistsScanned() {
	plScanning = false
	plScanMu.Lock()
	res := plScanResult
	plScanMu.Unlock()
	if len(res) == 0 {
		return
	}
	// match each playlist by its Spotify link first (it never changes, even when you
	// rename the playlist), and only by name when the link isn't known
	find := func(p playlistEntry) int {
		if p.URI != "" {
			for i, o := range plStore.Playlists {
				if o.URI == p.URI {
					return i
				}
			}
		}
		for i, o := range plStore.Playlists {
			if strings.EqualFold(o.Title, p.Title) && (o.URI == "" || p.URI == "" || o.URI == p.URI) {
				return i
			}
		}
		return -1
	}
	histChanged := false
	before := len(plStore.Playlists)
	matched := map[int]bool{}
	var added []playlistEntry
	for _, p := range res {
		i := find(p)
		if i < 0 {
			added = append(added, p)
			continue
		}
		matched[i] = true
		old := plStore.Playlists[i]
		if p.URI == "" {
			p.URI = old.URI
		}
		p.Art, p.px, p.NoArt = old.Art, old.px, old.NoArt
		if !strings.EqualFold(old.Title, p.Title) && p.URI != "" {
			// renamed in Spotify: update the recent list too
			for _, h := range history {
				if h.Kind == "playlist" && (h.URI == p.URI || (h.URI == "" && strings.EqualFold(h.Title, old.Title))) {
					h.Title, h.URI = p.Title, p.URI
					histChanged = true
				}
			}
		}
		plStore.Playlists[i] = p
	}
	// a read that saw nearly your whole library can tell which playlists are gone
	var missing []int
	if before > 0 && float64(len(matched)) >= 0.8*float64(before) {
		for i := 0; i < before; i++ {
			if matched[i] {
				plStore.Playlists[i].Gone = 0
			} else {
				plStore.Playlists[i].Gone++
				missing = append(missing, i)
			}
		}
	}
	// exactly one disappeared and one new name appeared: that's a rename
	if len(missing) == 1 && len(added) == 1 {
		old := plStore.Playlists[missing[0]]
		nw := &added[0]
		if nw.px == nil {
			nw.Art, nw.px, nw.NoArt = old.Art, old.px, old.NoArt
		}
		for _, h := range history {
			if h.Kind == "playlist" && strings.EqualFold(h.Title, old.Title) {
				h.Title = nw.Title
				if nw.URI != "" {
					h.URI = nw.URI
				}
				histChanged = true
			}
		}
		plStore.Playlists[missing[0]].Gone = 99
	}
	plStore.Playlists = append(plStore.Playlists, added...)
	if histChanged {
		saveHistory()
	}

	// tidy up: one entry per playlist (keep the most recently seen), and drop
	// playlists that haven't been in your library for 30 days (deleted ones)
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	byURI := map[string]int{}
	var kept []playlistEntry
	for _, p := range plStore.Playlists {
		if p.Seen < cutoff || p.Gone >= 2 { // deleted or renamed
			continue
		}
		if p.URI != "" {
			if j, ok := byURI[p.URI]; ok {
				if p.Seen > kept[j].Seen {
					if p.px == nil {
						p.Art, p.px, p.NoArt = kept[j].Art, kept[j].px, kept[j].NoArt
					}
					kept[j] = p
				}
				continue
			}
			byURI[p.URI] = len(kept)
		}
		kept = append(kept, p)
	}
	// a name-only entry is a duplicate if a linked entry has the same name
	names := map[string]bool{}
	for _, p := range kept {
		if p.URI != "" {
			names[strings.ToLower(p.Title)] = true
		}
	}
	plStore.Playlists = plStore.Playlists[:0]
	for _, p := range kept {
		if p.URI == "" && names[strings.ToLower(p.Title)] {
			continue
		}
		plStore.Playlists = append(plStore.Playlists, p)
	}

	// "you" = the most common owner, leaving out Spotify's own playlists and Liked Songs
	count := map[string]int{}
	for _, p := range plStore.Playlists {
		o := strings.ToLower(p.Owner)
		if o == "" || o == "spotify" || strings.Contains(o, "song") {
			continue
		}
		count[p.Owner]++
	}
	best := ""
	for o, c := range count {
		if best == "" || c > count[best] {
			best = o
		}
	}
	plStore.You = best
	savePlaylists()
	fetchPlaylistCovers()
}

func myPlaylists() []playlistEntry {
	var out []playlistEntry
	for _, p := range plStore.Playlists {
		if plStore.You == "" || strings.EqualFold(p.Owner, plStore.You) {
			out = append(out, p)
		}
	}
	return out
}

// matchPlaylists: your playlists whose name fits what you typed, best first
func matchPlaylists(q string) []playlistEntry {
	qn := normText(q)
	qw := strings.Fields(qn)
	if len(qw) == 0 {
		return nil
	}
	type sc struct {
		p playlistEntry
		s int
	}
	var hits []sc
	for _, p := range myPlaylists() {
		tn := normText(p.Title)
		tw := strings.Fields(tn)
		s := 0
		switch {
		case tn == qn:
			s = 3
		case strings.HasPrefix(tn, qn):
			s = 2
		default:
			all := true
			for i, w := range qw {
				if !containsWord(tw, w, i == len(qw)-1) {
					all = false
					break
				}
			}
			if all {
				s = 1
			}
		}
		if s > 0 {
			hits = append(hits, sc{p, s})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].s > hits[b].s })
	var out []playlistEntry
	for i := 0; i < len(hits) && i < 3; i++ {
		out = append(out, hits[i].p)
	}
	return out
}

// ---- covers, from Spotify's public link-preview service (oEmbed) ----

type coverResult struct {
	uri string
	px  []uint32
	ok  bool
}

var (
	plCoverMu  sync.Mutex
	plCovers   []coverResult
	plFetching = map[string]bool{}
)

const WM_PLCOVER = WM_APP + 26

func fetchPlaylistCovers() {
	n := histArtSize()
	for _, p := range myPlaylists() {
		if p.URI == "" || p.px != nil || p.NoArt || plFetching[p.URI] {
			continue
		}
		plFetching[p.URI] = true
		go func(uri string) {
			defer guard("playlistCover")
			id := strings.TrimPrefix(uri, "spotify:playlist:")
			res := coverResult{uri: uri}
			defer func() {
				plCoverMu.Lock()
				plCovers = append(plCovers, res)
				plCoverMu.Unlock()
				pPostMessageW.Call(hwnd, WM_PLCOVER, 0, 0)
			}()
			resp, err := catClient.Get("https://open.spotify.com/oembed?url=" + url.QueryEscape("https://open.spotify.com/playlist/"+id))
			if err != nil {
				return // try again another time
			}
			defer resp.Body.Close()
			if resp.StatusCode == 404 || resp.StatusCode == 400 || resp.StatusCode == 403 {
				res.ok = true // private playlist: no preview, don't keep asking
				return
			}
			var o struct {
				Thumb string `json:"thumbnail_url"`
			}
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&o) != nil || o.Thumb == "" {
				return
			}
			img, err := catClient.Get(o.Thumb)
			if err != nil {
				return
			}
			defer img.Body.Close()
			pic, _, err := image.Decode(img.Body)
			if err != nil {
				return
			}
			res.px, res.ok = scaleSquare(pic, n), true
		}(p.URI)
	}
}

// main thread: store covers as they arrive
func onPlaylistCover() {
	plCoverMu.Lock()
	got := plCovers
	plCovers = nil
	plCoverMu.Unlock()
	n := histArtSize()
	changed := false
	for _, c := range got {
		delete(plFetching, c.uri)
		if !c.ok {
			continue
		}
		for i := range plStore.Playlists {
			if plStore.Playlists[i].URI != c.uri {
				continue
			}
			if c.px == nil {
				plStore.Playlists[i].NoArt = true
			} else {
				plStore.Playlists[i].px = c.px
				plStore.Playlists[i].Art = artToPNG(c.px, n)
			}
			changed = true
		}
	}
	if changed {
		savePlaylists()
		if searchActive {
			renderSearch()
		}
	}
}

// cover for a playlist by its link or name (for the recent list)
func playlistArt(uri, title string) []uint32 {
	for _, p := range plStore.Playlists {
		if (uri != "" && p.URI == uri) || strings.EqualFold(p.Title, title) {
			return p.px
		}
	}
	return nil
}
