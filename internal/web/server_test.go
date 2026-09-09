package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/model"
)

func newTestServer() *Server { return New("test-1", slog.New(slog.DiscardHandler), false, nil) }

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, HealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID")
	}
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "ok" || h.Version != "test-1" {
		t.Errorf("unexpected body: %+v", h)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, HealthPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d", rec.Code)
	}
}

func TestQuietRoutesLogAtDebug(t *testing.T) {
	var buf bytes.Buffer
	s := New("t", slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), false, nil)
	h := s.Handler()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if strings.Contains(buf.String(), "msg=http") {
		t.Errorf("health probe should not log at info: %s", buf.String())
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	if !strings.Contains(buf.String(), "msg=http") || !strings.Contains(buf.String(), "status=404") {
		t.Errorf("normal routes should log at info: %s", buf.String())
	}
}

func TestRecovererTurnsPanicInto500(t *testing.T) {
	h := newTestServer().recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d", rec.Code)
	}
}

func TestOutputsBeforeAndAfterSnapshot(t *testing.T) {
	s := newTestServer()
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, XMLTVPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before any run: %d", rec.Code)
	}

	kick := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	s.Snapshots.Set(&model.Snapshot{RunID: 7, Channels: []model.Channel{{
		ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://x/1",
		Programmes: []model.Programme{{Event: model.Event{ID: "191277-abc", SeriesID: "191277", Title: "NFL Football", SubTitle: "A vs B", Start: kick, Stop: kick.Add(time.Hour), Kickoff: kick}}},
	}}})

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, XMLTVPath, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<channel id="NFL 04">`) ||
		rec.Header().Get("ETag") == "" {
		t.Errorf("xmltv: %d %s %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String()[:80])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type %q", ct)
	}

	req := httptest.NewRequest(http.MethodGet, M3UPath, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `channel-number="8504"`) {
		t.Errorf("m3u: %d %s", rec.Code, rec.Body.String())
	}

	tag := rec.Header().Get("ETag")
	req = httptest.NewRequest(http.MethodGet, M3UPath, nil)
	req.Header.Set("If-None-Match", tag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("etag revalidation: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/epg.xml", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("alias: %d", rec.Code)
	}
}

func TestOutputCacheRespectsGuideTagsAndNeverRegresses(t *testing.T) {
	s := newTestServer()
	tags := false
	s.Snapshots.GuideTags = func() bool { return tags }
	kick := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	mk := func(run int64) *model.Snapshot {
		return &model.Snapshot{RunID: run, Channels: []model.Channel{{
			ID: "NFL 04", Number: 8504, Name: fmt.Sprintf("NFL 04 run %d", run), Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://x/1",
			Programmes: []model.Programme{{Event: model.Event{ID: "191277-abc", SeriesID: "191277", Title: "NFL Football", SubTitle: "A vs B", Start: kick.Add(-time.Hour), Stop: kick.Add(time.Hour), Kickoff: kick}}},
		}}}
	}
	h := s.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	s.Snapshots.Set(mk(2))
	before := get(M3UPath)
	if strings.Contains(before.Body.String(), "tvc-guide-title") {
		t.Error("guide tags should be off")
	}
	// Flipping the setting must change the very next response, same run. The tag has to
	// move with it: a client revalidating against one that did not would keep the old body.
	tags = true
	after := get(M3UPath)
	if !strings.Contains(after.Body.String(), "tvc-guide-title") {
		t.Error("guide tags setting change was not reflected")
	}
	if a, b := before.Header().Get("ETag"), after.Header().Get("ETag"); a == b {
		t.Errorf("body changed but the tag did not: %s", a)
	}

	// A request that observes an older snapshot is served it, but the cache keeps the newer run.
	s.Snapshots.Set(mk(1))
	if body := get(XMLTVPath).Body.String(); !strings.Contains(body, "NFL 04 run 1") {
		t.Errorf("older snapshot should be served as is:\n%s", body)
	}
	if got := s.Snapshots.cache[outputKey{base: "http://example.com"}].runID; got != 2 {
		t.Errorf("cache regressed to run %d", got)
	}
	s.Snapshots.Set(mk(3))
	if body := get(XMLTVPath).Body.String(); !strings.Contains(body, "NFL 04 run 3") {
		t.Errorf("newer snapshot not served:\n%s", body)
	}
}

// Most refreshes find nothing new. Those must not cost every consumer a fresh download of
// the whole guide, which is what naming the run in the tag would do.
func TestUnchangedGuideKeepsItsTag(t *testing.T) {
	s := newTestServer()
	h := s.Handler()
	same := func(run int64) *model.Snapshot {
		return &model.Snapshot{RunID: run, Channels: []model.Channel{{
			ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://x/1",
		}}}
	}
	tag := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, XMLTVPath, nil))
		return rec.Header().Get("ETag")
	}

	s.Snapshots.Set(same(1))
	first := tag()
	s.Snapshots.Set(same(2))
	if second := tag(); second != first {
		t.Errorf("a refresh that changed nothing moved the tag: %s then %s", first, second)
	}

	req := httptest.NewRequest(http.MethodGet, XMLTVPath, nil)
	req.Header.Set("If-None-Match", first)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("revalidation after an empty refresh: %d", rec.Code)
	}
}

// Art is stored as a path, so the host a consumer used is what makes the links in its copy
// of the guide resolve. Two consumers reaching the app by different names must each get
// their own, and the tag has to tell them apart or a shared proxy will hand one the other's.
func TestOutputsAreAddressedForTheRequester(t *testing.T) {
	s := newTestServer()
	s.Snapshots.Set(&model.Snapshot{RunID: 1, Channels: []model.Channel{{
		ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl",
		LogoURL: "/art/league/nfl.png", StreamURL: "http://x/1",
	}}})
	h := s.Handler()
	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, M3UPath, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	one, two := get("box.lan:8080"), get("epg3r.example:9000")
	if !strings.Contains(one.Body.String(), `tvg-logo="http://box.lan:8080/art/league/nfl.png"`) {
		t.Errorf("box.lan:\n%s", one.Body.String())
	}
	if !strings.Contains(two.Body.String(), `tvg-logo="http://epg3r.example:9000/art/league/nfl.png"`) {
		t.Errorf("epg3r.example:\n%s", two.Body.String())
	}
	if a, b := one.Header().Get("ETag"), two.Header().Get("ETag"); a == b {
		t.Errorf("two hosts share a tag: %s", a)
	}
	// Both are kept: the media server polls under one name while the preview page renders
	// under whatever the browser used, and a single slot would have them evict each other.
	if again := get("box.lan:8080"); again.Header().Get("ETag") != one.Header().Get("ETag") {
		t.Error("same host should be served the same rendering")
	}
	if n := len(s.Snapshots.cache); n != 2 {
		t.Errorf("cache holds %d entries; both hosts should be cached", n)
	}
	// A stream of invented names cannot grow it without bound.
	for i := range maxRenderings * 2 {
		get(fmt.Sprintf("h%d.invalid", i))
	}
	if n := len(s.Snapshots.cache); n > maxRenderings {
		t.Errorf("cache grew to %d entries", n)
	}

	// Once the operator says how the app is reached, that is the answer for everyone.
	s.PublicBase = func() string { return "https://guide.example" }
	fixed := get("box.lan:8080")
	if !strings.Contains(fixed.Body.String(), `tvg-logo="https://guide.example/art/league/nfl.png"`) {
		t.Errorf("public_base_url ignored:\n%s", fixed.Body.String())
	}
}
