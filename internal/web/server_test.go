package web

import (
	"bytes"
	"encoding/json"
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<channel id="NFL 04">`) || rec.Header().Get("ETag") != `"run-7"` {
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

	req = httptest.NewRequest(http.MethodGet, M3UPath, nil)
	req.Header.Set("If-None-Match", `"run-7"`)
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
			ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://x/1",
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
	if body := get(M3UPath).Body.String(); strings.Contains(body, "tvc-guide-title") {
		t.Error("guide tags should be off")
	}
	// Flipping the setting must change the very next response, same run.
	tags = true
	if body := get(M3UPath).Body.String(); !strings.Contains(body, "tvc-guide-title") {
		t.Error("guide tags setting change was not reflected")
	}

	// A request that observes an older snapshot is served it, but the cache keeps the newer run.
	s.Snapshots.Set(mk(1))
	if etag := get(XMLTVPath).Header().Get("ETag"); etag != `"run-1"` {
		t.Errorf("older snapshot should be served as is: %s", etag)
	}
	if got := s.Snapshots.cache[""].runID; got != 2 {
		t.Errorf("cache regressed to run %d", got)
	}
	s.Snapshots.Set(mk(3))
	if etag := get(XMLTVPath).Header().Get("ETag"); etag != `"run-3"` {
		t.Errorf("newer snapshot not served: %s", etag)
	}
}
