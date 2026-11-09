package main

// Live song suggestions while typing in the search bar, from Deezer's free
// public catalog (no account or key needed). Picking one opens it in Spotify.

import (
	"encoding/json"
	"image"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unsafe"
)

const WM_SEARCHRESULTS = WM_APP + 22 // wParam = search generation

type catalogTrack struct {
	Title  string
	Artist string
	Album  string
	Secs   int
	art    []uint32
	cover  string
}

var (
	catClient = &http.Client{Timeout: 6 * time.Second}

	catMu      sync.Mutex
	catGen     int // newest search; older answers are ignored
	catResults []catalogTrack
	catQuery   string
	catErr     bool
	catLoading bool

	artCacheMu sync.Mutex
	coverCache = map[string][]uint32{} // cover URL -> pixels
)

type deezerTrack struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Duration int     `json:"duration"`
	Rank     int64   `json:"rank"` // popularity
	Pop      float64 `json:"-"`    // popularity from another catalog (overrides Rank)
	Artist   struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Title  string `json:"title"`
		Cover  string `json:"cover_medium"`
		CoverS string `json:"cover_small"`
	} `json:"album"`
}

// ---- matching ----

var junkWords = []string{"karaoke", "tribute", "instrumental", "originally performed", "made famous", "in the style of", "cover version", "8-bit", "lullaby", "piano version"}

func normText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "+", " and ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '’' || r == '.':
			// "don't" -> "dont", "will.i.am" -> "william"
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// drop "(feat. …)", "- Remastered 2011" and similar from a title
func coreTitle(t string) string {
	lt := strings.ToLower(t)
	for _, sep := range []string{" (feat", " [feat", " (with ", " - remaster", " (remaster", " - radio edit", " (radio edit"} {
		if i := strings.Index(lt, sep); i > 0 {
			t, lt = t[:i], lt[:i]
		}
	}
	return t
}

func containsWord(words []string, w string, prefix bool) bool {
	for _, x := range words {
		if x == w || (prefix && strings.HasPrefix(x, w)) {
			return true
		}
	}
	return false
}

// score: how likely this is the song being searched for
func scoreTrack(q string, t deezerTrack) float64 {
	qn := normText(q)
	qw := strings.Fields(qn)
	if len(qw) == 0 {
		return 0
	}
	title := normText(coreTitle(t.Title))
	tw := strings.Fields(title)
	aw := strings.Fields(normText(t.Artist.Name))
	inTitle, inAny := 0.0, 0.0
	for i, w := range qw {
		last := i == len(qw)-1 // still being typed: allow a prefix
		if w == "and" || w == "the" {
			inTitle += 0.5
			inAny += 0.5
			continue
		}
		if containsWord(tw, w, last) {
			inTitle++
			inAny++
		} else if containsWord(aw, w, last) {
			inAny++
		}
	}
	n := float64(len(qw))
	score := 3*inAny/n + 2*inTitle/n
	if title == qn {
		score += 2 // exact title
	} else if strings.HasPrefix(title, qn) || strings.HasPrefix(qn, title+" ") {
		score += 1 // "scream and shout" vs "scream and shout remix", or query adds the artist
	}
	switch {
	case t.Pop > 0:
		score += t.Pop
	case t.Rank > 0:
		score += 0.45 * math.Log10(float64(t.Rank)+1) // popular songs first
	}
	all := strings.ToLower(t.Title + " " + t.Artist.Name + " " + t.Album.Title)
	for _, j := range junkWords {
		if strings.Contains(all, j) && !strings.Contains(qn, j) {
			score -= 3
			break
		}
	}
	return score
}

// spellings to try: "scream and shout" also as "scream & shout"
func queryVariants(q string) []string {
	out := []string{q}
	add := func(v string) {
		v = strings.Join(strings.Fields(v), " ")
		for _, o := range out {
			if strings.EqualFold(o, v) {
				return
			}
		}
		if v != "" {
			out = append(out, v)
		}
	}
	lq := " " + strings.ToLower(q) + " "
	if strings.Contains(lq, " and ") {
		add(strings.TrimSpace(strings.ReplaceAll(lq, " and ", " & ")))
	}
	if strings.Contains(lq, "&") {
		add(strings.TrimSpace(strings.ReplaceAll(lq, "&", " and ")))
	}
	add(normText(q)) // without punctuation
	return out
}

func fetchDeezer(q string) ([]deezerTrack, bool) {
	resp, err := catClient.Get("https://api.deezer.com/search?limit=25&q=" + url.QueryEscape(q))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	var body struct {
		Data []deezerTrack `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, false
	}
	return body.Data, true
}

// iTunes Search (Apple's free public catalog): very complete, results come
// sorted by popularity, so position stands in for popularity.
func fetchITunes(q string) ([]deezerTrack, bool) {
	u := "https://itunes.apple.com/search?media=music&entity=song&limit=15&country=" + userCountry() + "&term=" + url.QueryEscape(q)
	resp, err := catClient.Get(u)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	var body struct {
		Results []struct {
			ID     int64  `json:"trackId"`
			Title  string `json:"trackName"`
			Artist string `json:"artistName"`
			Album  string `json:"collectionName"`
			Art    string `json:"artworkUrl100"`
			Ms     int    `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, false
	}
	out := make([]deezerTrack, 0, len(body.Results))
	for i, r := range body.Results {
		var t deezerTrack
		t.ID = -r.ID // keep apart from Deezer IDs
		t.Title, t.Duration = r.Title, r.Ms/1000
		t.Artist.Name, t.Album.Title = r.Artist, r.Album
		t.Album.Cover = strings.Replace(r.Art, "100x100bb", "300x300bb", 1)
		t.Pop = 2.7 * (1 - 0.05*float64(i)) // first result ≈ a top Deezer hit
		out = append(out, t)
	}
	return out, true
}

var countryCode string

// two-letter country from the Windows region setting (e.g. "en-US" -> "US")
func userCountry() string {
	if countryCode != "" {
		return countryCode
	}
	countryCode = "US"
	buf := make([]uint16, 85)
	if n, _, _ := kernel32.NewProc("GetUserDefaultLocaleName").Call(uintptr(unsafe.Pointer(&buf[0])), 85); n > 0 {
		name := syscall.UTF16ToString(buf)
		if i := strings.LastIndex(name, "-"); i >= 0 && len(name)-i-1 == 2 {
			countryCode = strings.ToUpper(name[i+1:])
		}
	}
	return countryCode
}

// startCatalogSearch runs in the background and posts WM_SEARCHRESULTS when done
// (and again as covers arrive).
func startCatalogSearch(gen int, q string) {
	go func() {
		defer guard("catalogSearch")
		// Apple's catalog once, Deezer in each spelling, all at the same time; then merge and rank
		vars := queryVariants(q)
		lists := make([][]deezerTrack, len(vars)+1)
		oks := make([]bool, len(vars)+1)
		var qwg sync.WaitGroup
		qwg.Add(1)
		go func() {
			defer qwg.Done()
			lists[0], oks[0] = fetchITunes(q)
		}()
		for i, v := range vars {
			qwg.Add(1)
			go func(i int, v string) {
				defer qwg.Done()
				lists[i], oks[i] = fetchDeezer(v)
			}(i+1, v)
		}
		qwg.Wait()
		counts := make([]int, len(lists))
		for i := range lists {
			counts[i] = len(lists[i])
		}
		if !oks[0] && counts[0] == 0 {
			logf("search: the iTunes catalog didn't answer")
		}
		ok := false
		seen := map[int64]bool{}
		var all []deezerTrack
		for i := range lists {
			ok = ok || oks[i]
			for _, t := range lists[i] {
				if !seen[t.ID] {
					seen[t.ID] = true
					all = append(all, t)
				}
			}
		}
		type scored struct {
			t deezerTrack
			s float64
		}
		ranked := make([]scored, 0, len(all))
		for _, t := range all {
			ranked = append(ranked, scored{t, scoreTrack(q, t)})
		}
		sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].s > ranked[b].s })
		// one entry per song (the same track appears on albums, compilations, singles...)
		var top []deezerTrack
		dup := map[string]bool{}
		for _, r := range ranked {
			aw := strings.Fields(normText(r.t.Artist.Name))
			first := ""
			if len(aw) > 0 {
				first = aw[0]
			}
			k := normText(coreTitle(r.t.Title)) + "|" + first
			if dup[k] {
				continue
			}
			dup[k] = true
			top = append(top, r.t)
			if len(top) == 6 {
				break
			}
		}
		// fetch the covers first (up to ~0.7 s) so the list arrives complete, all at once
		results := make([]catalogTrack, 0, len(top))
		for _, d := range top {
			t := catalogTrack{Title: d.Title, Artist: d.Artist.Name, Album: d.Album.Title, Secs: d.Duration, cover: d.Album.Cover}
			if t.cover == "" {
				t.cover = d.Album.CoverS
			}
			results = append(results, t)
		}
		n := histArtSize()
		published := false // guarded by catMu
		var wg sync.WaitGroup
		for i, t := range results {
			artCacheMu.Lock()
			_, have := coverCache[t.cover]
			artCacheMu.Unlock()
			if have || t.cover == "" {
				continue
			}
			wg.Add(1)
			go func(i int, cover string) {
				defer wg.Done()
				defer guard("cover")
				r, err := catClient.Get(cover)
				if err != nil {
					return
				}
				defer r.Body.Close()
				img, _, err := image.Decode(r.Body)
				if err != nil {
					return
				}
				px := scaleSquare(img, n)
				artCacheMu.Lock()
				if len(coverCache) > 200 {
					coverCache = map[string][]uint32{}
				}
				coverCache[cover] = px
				artCacheMu.Unlock()
				// a cover that missed the deadline fills in afterwards
				catMu.Lock()
				late := published && gen == catGen && i < len(catResults) && catResults[i].cover == cover
				if late {
					catResults[i].art = px
				}
				catMu.Unlock()
				if late {
					pPostMessageW.Call(hwnd, WM_SEARCHRESULTS, uintptr(gen), 0)
				}
			}(i, t.cover)
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(700 * time.Millisecond):
		}
		artCacheMu.Lock()
		for i := range results {
			results[i].art = coverCache[results[i].cover]
		}
		artCacheMu.Unlock()

		catMu.Lock()
		if gen != catGen {
			catMu.Unlock()
			return
		}
		catLoading, catErr, catQuery = false, !ok, q
		catResults = results
		published = true
		catMu.Unlock()
		pPostMessageW.Call(hwnd, WM_SEARCHRESULTS, uintptr(gen), 0)
	}()
}
