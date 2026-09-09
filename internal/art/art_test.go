package art

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// solidPNG stands in for a crest. Magenta because nothing else in the palette is, so a test
// can tell whether the mark actually reached the canvas.
func solidPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	magenta = color.RGBA{255, 0, 255, 255}
	cyan    = color.RGBA{0, 255, 255, 255}
)

// source is a stand-in for the crest CDN. Nothing in these tests touches the network.
type source struct {
	*httptest.Server
	hits     atomic.Int64
	peak     atomic.Int64
	inFlight atomic.Int64
	block    chan struct{}
	body     []byte // what most paths serve
	other    []byte // one team's, so two sides of a matchup are not the same picture
}

func newSource(t *testing.T) *source {
	t.Helper()
	s := &source{body: solidPNG(t, magenta), other: solidPNG(t, cyan)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		n := s.inFlight.Add(1)
		defer s.inFlight.Add(-1)
		for {
			was := s.peak.Load()
			if n <= was || s.peak.CompareAndSwap(was, n) {
				break
			}
		}
		if s.block != nil {
			<-s.block
		}
		switch {
		case strings.Contains(r.URL.Path, "/nope.png"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/junk.png"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("not a png"))
		case strings.Contains(r.URL.Path, "/hou.png"):
			w.Header().Set("ETag", `"v1-hou"`)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(s.other)
		default:
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(s.body)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newService(t *testing.T, src *source, opts ...func(*Options)) *Service {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Catalog: cat, CacheDir: t.TempDir(), Log: discard(), Now: time.Now}
	if src != nil {
		o.Source = src.URL
	}
	for _, f := range opts {
		f(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decodePNG(t *testing.T, img Image) *image.RGBA {
	t.Helper()
	got, err := png.Decode(bytes.NewReader(img.PNG))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := image.NewRGBA(got.Bounds())
	for y := got.Bounds().Min.Y; y < got.Bounds().Max.Y; y++ {
		for x := got.Bounds().Min.X; x < got.Bounds().Max.X; x++ {
			out.Set(x, y, got.At(x, y))
		}
	}
	return out
}

// No golden PNGs anywhere in these tests. x/image's resampling accumulates with a
// multiply-add the compiler fuses on arm64 and not on amd64, so a resampled pixel can differ
// by one between a dev container and CI — and one pixel is a different file. Every
// assertion here is about what the picture contains.
func TestEveryProductIsA4x3PictureWithSomethingOnIt(t *testing.T) {
	s := newService(t, newSource(t))
	ctx := t.Context()
	for name, get := range map[string]func() (Image, error){
		"placard": func() (Image, error) { return s.LeaguePlacard(ctx, "nfl") },
		"matchup": func() (Image, error) { return s.MatchupPlacard(ctx, "nfl", "buffalo-bills", "houston-texans") },
	} {
		img, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := decodePNG(t, img)
		if w, h := got.Bounds().Dx(), got.Bounds().Dy(); w != canvasW || h != canvasH {
			t.Errorf("%s: %dx%d", name, w, h)
		}
		// A blank rectangle is the right size too, so count what is actually on it.
		seen := map[color.RGBA]bool{}
		for y := 40; y < canvasH; y += 40 {
			for x := 40; x < canvasW; x += 40 {
				c := got.RGBAAt(x, y)
				seen[color.RGBA{c.R, c.G, c.B, 255}] = true
			}
		}
		if len(seen) < 12 {
			t.Errorf("%s: only %d distinct colours; is anything drawn?", name, len(seen))
		}
		if img.ETag == "" || !strings.HasPrefix(img.ETag, `"`+Version+"-") {
			t.Errorf("%s: etag %q", name, img.ETag)
		}
		if len(img.PNG) > 250<<10 {
			t.Errorf("%s: %d bytes", name, len(img.PNG))
		}
	}
}

// A channel logo is the crest itself, passed through untouched. Whatever is behind it in a
// guide belongs to whoever is showing it, so there is nothing to compose onto.
func TestChannelLogoIsTheCrestItself(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	img, err := s.TeamLogo(t.Context(), "nfl", "buffalo-bills")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img.PNG, src.body) {
		t.Error("the crest was redrawn rather than served as it arrived")
	}
	if img.Fallback {
		t.Error("a crest that fetched fine should not be a fallback")
	}
}

// A crest does have to reach the 4:3 canvas, where there is a composition to get wrong.
func TestTheCrestIsDrawnOnAMatchup(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	img, err := s.MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "houston-texans")
	if err != nil {
		t.Fatal(err)
	}
	got := decodePNG(t, img)
	if c := got.RGBAAt(288, 580); c.R < 200 || c.B < 200 || c.G > 80 {
		t.Errorf("the away cell is %v, not the crest", c)
	}
}

// A drawn logo is transparent outside its roundel: it is an identity mark, not a picture.
func TestDrawnLogoIsTransparent(t *testing.T) {
	s := newService(t, newSource(t))
	defer withLeagueLogoID(t, s, "ncaab", "")() // as a league with no mark of its own
	img, err := s.LeagueLogo(t.Context(), "ncaab")
	if err != nil {
		t.Fatal(err)
	}
	got := decodePNG(t, img)
	if w, h := got.Bounds().Dx(), got.Bounds().Dy(); w != logoSize || h != logoSize {
		t.Errorf("%dx%d, want a %d square", w, h, logoSize)
	}
	if a := got.RGBAAt(2, 2).A; a != 0 {
		t.Errorf("corner alpha is %d; a logo has no background of its own", a)
	}
	if a := got.RGBAAt(logoSize/2, logoSize/2).A; a == 0 {
		t.Error("nothing drawn in the middle of the roundel")
	}
}

// A team the source has no crest for is lettered instead, and that answer is not kept: the
// next request should ask again rather than leaving a stand-in in front of people.
func TestMissingCrestFallsBackToLetters(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	defer withLogoID(t, s, "buffalo-bills", "nope")()

	img, err := s.TeamLogo(t.Context(), "nfl", "buffalo-bills")
	if err != nil {
		t.Fatal(err)
	}
	if !img.Fallback {
		t.Error("expected a fallback")
	}
	if bytes.Equal(img.PNG, src.body) {
		t.Error("a crest was served despite the source having none")
	}
	if _, err := os.Stat(s.renderPath("logo/nfl/buffalo-bills")); err == nil {
		t.Error("a fallback should not be kept on disk")
	}
}

// A 404 is an answer, not a failure, and is worth remembering: without this every render
// asks the source again for a crest it has already said it does not have.
func TestMissingCrestIsAskedForOnce(t *testing.T) {
	src := newSource(t)
	dir := t.TempDir()
	cat, _ := catalog.Load()
	lg, _ := cat.League("nfl")
	team, _ := cat.Teams(lg, false).ByKey("buffalo-bills")
	was := team.LogoID
	team.LogoID = "nope"
	defer func() { team.LogoID = was }()

	now := time.Now()
	build := func() *Service {
		s, err := New(Options{Catalog: cat, CacheDir: dir, Source: src.URL, Log: discard(),
			Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for range 3 {
		if _, err := build().TeamLogo(t.Context(), "nfl", "buffalo-bills"); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.hits.Load(); n != 1 {
		t.Errorf("asked %d times for a crest the source has already denied", n)
	}
	now = now.Add(8 * 24 * time.Hour) // past the negative TTL
	if _, err := build().TeamLogo(t.Context(), "nfl", "buffalo-bills"); err != nil {
		t.Fatal(err)
	}
	if n := src.hits.Load(); n != 2 {
		t.Errorf("after the entry expired the source should be asked again; hits %d", n)
	}
}

// A body that is not a picture is remembered the same way a 404 is.
func TestUndecodableBodyIsRemembered(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	defer withLogoID(t, s, "buffalo-bills", "junk")()

	for range 2 {
		s.cache = nil // make it recompose rather than answering from memory
		if _, err := s.TeamLogo(t.Context(), "nfl", "buffalo-bills"); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.hits.Load(); n != 1 {
		t.Errorf("asked %d times for a body already known not to be a picture", n)
	}
}

// A guide load asks for the same picture hundreds of times at once. Those have to collapse
// into one drawing and one fetch, or the first load is a stampede.
func TestConcurrentRequestsCollapse(t *testing.T) {
	src := newSource(t)
	src.block = make(chan struct{})
	s := newService(t, src)

	var wg sync.WaitGroup
	results := make([]string, 50)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			img, err := s.MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "houston-texans")
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = img.ETag
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(src.block)
	wg.Wait()

	// Two teams and the league's own mark, once each, however many callers asked.
	if n := src.hits.Load(); n != 3 {
		t.Errorf("%d fetches for one matchup", n)
	}
	for _, tag := range results[1:] {
		if tag != results[0] {
			t.Fatalf("callers got different pictures: %q and %q", results[0], tag)
		}
	}
}

// The source is a third party and gets a polite number of connections, whatever the guide
// asks for at once.
func TestFetchesAreBounded(t *testing.T) {
	src := newSource(t)
	src.block = make(chan struct{})
	s := newService(t, src, func(o *Options) { o.MaxFetches = 4 })
	cat, _ := catalog.Load()
	lg, _ := cat.League("ncaaf")
	teams := cat.Teams(lg, false).Teams

	var wg sync.WaitGroup
	for _, team := range teams[:24] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.TeamLogo(t.Context(), "ncaaf", team.Key)
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(src.block)
	wg.Wait()
	if peak := src.peak.Load(); peak > 4 {
		t.Errorf("%d fetches in flight at once, bound was 4", peak)
	}
}

// Byte equality across machines is not something this can promise, but within one process
// the same inputs must draw the same picture, or the caches below are meaningless.
func TestSameInputsDrawTheSamePicture(t *testing.T) {
	src := newSource(t)
	first, err := newService(t, src).MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "houston-texans")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newService(t, src).MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "houston-texans")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PNG, second.PNG) {
		t.Error("two renders of one matchup differ")
	}
	if first.ETag != second.ETag {
		t.Errorf("etags differ: %s %s", first.ETag, second.ETag)
	}
}

// Home is last, and the order is never sorted: these are different games.
func TestMatchupOrderMatters(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	one, err := s.MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "houston-texans")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.MatchupPlacard(t.Context(), "nfl", "houston-texans", "buffalo-bills")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(one.PNG, two.PNG) || one.ETag == two.ETag {
		t.Error("reversing the sides drew the same picture")
	}
}

// Our URLs are built from the catalog, so one naming something that is not in it is a bug
// rather than a picture we could not draw.
func TestUnknownKeys(t *testing.T) {
	s := newService(t, newSource(t))
	for _, call := range []func() (Image, error){
		func() (Image, error) { return s.LeaguePlacard(t.Context(), "kabaddi") },
		func() (Image, error) { return s.TeamLogo(t.Context(), "nfl", "not-a-team") },
		func() (Image, error) { return s.TeamLogo(t.Context(), "nfl", "../../etc/passwd") },
		func() (Image, error) { return s.MatchupPlacard(t.Context(), "nfl", "buffalo-bills", "nope") },
	} {
		if _, err := call(); err != ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	}
}

// A data directory that cannot be written to should slow the app down, not stop it.
func TestUnwritableCacheStillDraws(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	src := newSource(t)
	s := newService(t, src, func(o *Options) { o.CacheDir = filepath.Join(dir, "art") })
	img, err := s.LeaguePlacard(t.Context(), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	if len(img.PNG) == 0 {
		t.Error("no picture")
	}
}

// Six of the eight leagues have a mark of their own on the source; those are served as they
// arrive, exactly as a team crest is.
func TestLeagueWithAMarkServesIt(t *testing.T) {
	src := newSource(t)
	s := newService(t, src)
	img, err := s.LeagueLogo(t.Context(), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img.PNG, src.body) {
		t.Error("the league mark was redrawn rather than served as it arrived")
	}
}

func TestLetters(t *testing.T) {
	for _, tc := range []struct{ abbr, name, want string }{
		{"BUF", "Buffalo Bills", "BUF"},
		{"", "Team Coop", "TC"},
		{"alsga", "Alabama State", "ALSGA"},
		{"", "Southeastern Louisiana", "SL"},
		{"", "Anderson", "AND"},
		{"", "FC Dallas", "FCD"},
	} {
		if got := letters(tc.abbr, tc.name); got != tc.want {
			t.Errorf("letters(%q, %q) = %q, want %q", tc.abbr, tc.name, got, tc.want)
		}
	}
}

func TestPathsAndKeys(t *testing.T) {
	if got := MatchupPlacardPath("nfl", "a-b", "c-d"); got != "/art/"+Version+"/placard/nfl/a-b/c-d.png" {
		t.Errorf("MatchupPlacardPath = %s", got)
	}
	if got := LeagueLogoPath("nfl"); got != "/art/"+Version+"/logo/nfl.png" {
		t.Errorf("LeagueLogoPath = %s", got)
	}
	if got := TeamLogoPath("nfl", "buffalo-bills"); got != "/art/"+Version+"/logo/nfl/buffalo-bills.png" {
		t.Errorf("TeamLogoPath = %s", got)
	}
	if got := LeaguePlacardPath("nfl"); got != "/art/"+Version+"/placard/nfl.png" {
		t.Errorf("LeaguePlacardPath = %s", got)
	}
	for _, bad := range []string{"", "../x", "a/b", "A", "x y", "x%2f", strings.Repeat("x", 65)} {
		if safeKey(bad) {
			t.Errorf("safeKey(%q) should be false", bad)
		}
	}
	for _, ok := range []string{"nfl", "buffalo-bills", "a1"} {
		if !safeKey(ok) {
			t.Errorf("safeKey(%q) should be true", ok)
		}
	}
}

// withLogoID points a team at a different crest for the duration of a test, on the catalog
// the service actually reads.
// withLeagueLogoID does the same for a league's own mark. A league whose mark is named by a
// whole URL would otherwise be fetched from wherever that names, and no test here goes near
// the network.
func withLeagueLogoID(t *testing.T, s *Service, leagueKey, id string) func() {
	t.Helper()
	lg, ok := s.cat.League(leagueKey)
	if !ok {
		t.Fatalf("no league %s", leagueKey)
	}
	was := lg.LogoID
	lg.LogoID = id
	return func() { lg.LogoID = was }
}

func withLogoID(t *testing.T, s *Service, teamKey, id string) func() {
	t.Helper()
	lg, _ := s.cat.League("nfl")
	team, ok := s.cat.Teams(lg, false).ByKey(teamKey)
	if !ok {
		t.Fatalf("no team %s", teamKey)
	}
	was := team.LogoID
	team.LogoID = id
	return func() { team.LogoID = was }
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
