package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonmaddox/epg3r/internal/art"
	"github.com/jonmaddox/epg3r/internal/catalog"
)

// artServer serves art with a stand-in for the crest source that has nothing, so every
// picture is drawn from names and no test touches the network.
func artServer(t *testing.T) http.Handler {
	t.Helper()
	source := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(source.Close)
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pictures, err := art.New(art.Options{
		Catalog: cat, CacheDir: t.TempDir(), Source: source.URL,
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	s.Art = pictures
	return s.Handler()
}

func TestArtRoutes(t *testing.T) {
	h := artServer(t)
	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{
		art.LeagueLogoPath("nfl"),
		art.TeamLogoPath("nfl", "buffalo-bills"),
		art.LeaguePlacardPath("nfl"),
		art.MatchupPlacardPath("nfl", "buffalo-bills", "houston-texans"),
	} {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s: content-type %q", path, ct)
		}
		if !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
			t.Errorf("%s: body is not a png", path)
		}
		tag := rec.Header().Get("ETag")
		if tag == "" {
			t.Fatalf("%s: no etag", path)
		}
		// The tag names what the picture was drawn from, so revalidating never has to
		// draw it again.
		if again := get(path, "If-None-Match", tag); again.Code != http.StatusNotModified {
			t.Errorf("%s: revalidation gave %d", path, again.Code)
		}
	}

	// Our URLs come from the catalog. One that names something else is a bug, not a
	// picture to invent.
	for _, path := range []string{
		art.LeagueLogoPath("kabaddi"),
		art.TeamLogoPath("nfl", "not-a-team"),
		art.MatchupPlacardPath("nfl", "buffalo-bills", "not-a-team"),
		"/art/logo/nfl",                   // no extension: not one of ours
		"/art/logo/nfl.jpg",               // nor is this
		"/art/logo/nfl/buffalo/extra.png", // nor is this
		"/art/placard/nfl/buffalo.png",
	} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, rec.Code)
		}
	}
}

// A picture drawn without a crest it wanted is served for minutes rather than a day, so it
// is not left in front of people once the source is reachable again.
func TestArtCacheHeaders(t *testing.T) {
	h := artServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, art.LeaguePlacardPath("nfl"), nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("a placard needs no crest, so it should be cached for a day: %q", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, art.TeamLogoPath("nfl", "buffalo-bills"), nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("a lettered stand-in should be short-lived: %q", got)
	}
}

// Art is served without a store, the way the guide is: these are what the app is for.
func TestArtNeedsNoStore(t *testing.T) {
	h := artServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, art.LeagueLogoPath("mls"), nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}
