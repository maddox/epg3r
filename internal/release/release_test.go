package release

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.DiscardHandler)

// The whole point of a timestamp version: comparing two of them as strings compares them as
// times, so this needs no version library and cannot disagree with one.
func TestBehind(t *testing.T) {
	for _, tc := range []struct {
		name, current, latest string
		want                  bool
	}{
		{"a minute newer", "2026.09.10.1423", "2026.09.10.1424", true},
		{"a day newer", "2026.09.10.2359", "2026.09.11.0000", true},
		{"a year newer", "2026.12.31.2359", "2027.01.01.0000", true},
		{"the same", "2026.09.10.1423", "2026.09.10.1423", false},
		// Two releases inside one minute; the second carries seconds.
		{"seconds beat the bare minute", "2026.09.10.1423", "2026.09.10.142305", true},
		{"the bare minute does not beat seconds", "2026.09.10.142305", "2026.09.10.1423", false},
		{"the next minute beats seconds", "2026.09.10.142359", "2026.09.10.1424", true},

		// A release deleted, or latest rolled back to an older tag. Comparing with != rather
		// than > would tell every running copy to "upgrade" to something older.
		{"rolled back", "2026.09.10.1424", "2026.09.10.1423", false},
		{"rolled back a day", "2026.09.11.0000", "2026.09.10.2359", false},

		{"a build from source", DevVersion, "2026.09.10.1424", false},
		{"nothing published yet", "2026.09.10.1423", "", false},
		{"no version at all", "", "2026.09.10.1424", false},
		{"a tag someone made by hand", "2026.09.10.1423", "v1.2.3", false},
		{"a version someone built by hand", "1.2.3", "2026.09.10.1424", false},
		{"almost a stamp", "2026.9.10.1423", "2026.09.10.1424", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := behind(tc.current, tc.latest); got != tc.want {
				t.Errorf("behind(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func githubStub(t *testing.T, hits *atomic.Int64, handler http.HandlerFunc) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New("owner/repo", "2026.09.10.1423", quiet)
	c.base = srv.URL
	return c
}

func TestChecksAndReports(t *testing.T) {
	var hits atomic.Int64
	c := githubStub(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/repos/owner/repo/releases/latest" {
			t.Errorf("path = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		w.Write([]byte(`{"tag_name":"2026.09.11.0900","html_url":"https://example.invalid/rel"}`))
	})

	// Before any check there is nothing to say, and the link points at what is running.
	if got := c.Status(); got.Behind || got.Latest != "" {
		t.Errorf("before checking: %+v", got)
	}

	c.check(context.Background())
	got := c.Status()
	if !got.Behind {
		t.Error("should be behind")
	}
	if got.Latest != "2026.09.11.0900" || got.URL != "https://example.invalid/rel" {
		t.Errorf("status = %+v", got)
	}
	if got.Current != "2026.09.10.1423" {
		t.Errorf("current = %q", got.Current)
	}
	if hits.Load() != 1 {
		t.Errorf("hit GitHub %d times", hits.Load())
	}
}

// Up to date links to the release that is running, not to whatever the last check returned,
// so the link never sends someone somewhere that is not what they have.
func TestUpToDateLinksAtWhatIsRunning(t *testing.T) {
	c := githubStub(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"2026.09.10.1423","html_url":"https://example.invalid/rel"}`))
	})
	c.check(context.Background())
	got := c.Status()
	if got.Behind {
		t.Error("should not be behind")
	}
	if want := "https://github.com/owner/repo/releases/tag/2026.09.10.1423"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

// The footer names the running version, so its link has to point at that release even when
// a newer one exists — otherwise it reads as one version and goes to another.
func TestCurrentURLAlwaysNamesTheRunningBuild(t *testing.T) {
	c := githubStub(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"2026.09.11.0900","html_url":"https://example.invalid/newer"}`))
	})
	c.check(context.Background())
	got := c.Status()
	if !got.Behind {
		t.Fatal("should be behind")
	}
	if want := "https://github.com/owner/repo/releases/tag/2026.09.10.1423"; got.CurrentURL != want {
		t.Errorf("CurrentURL = %q, want the running release %q", got.CurrentURL, want)
	}
	if got.URL != "https://example.invalid/newer" {
		t.Errorf("URL should offer the newer release, got %q", got.URL)
	}
}

// A build from source links at the project rather than at a release that does not exist.
func TestDevLinksAtTheRepo(t *testing.T) {
	c := New("owner/repo", DevVersion, quiet)
	got := c.Status()
	if got.Behind {
		t.Error("a build from source is never behind")
	}
	if want := "https://github.com/owner/repo"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

// A failed check keeps the last good answer rather than blanking the header, and says
// nothing to the person using the app.
func TestFailureKeepsTheLastAnswer(t *testing.T) {
	var fail atomic.Bool
	c := githubStub(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusForbidden) // what rate limiting looks like
			return
		}
		w.Write([]byte(`{"tag_name":"2026.09.11.0900","html_url":"https://example.invalid/rel"}`))
	})
	c.check(context.Background())

	fail.Store(true)
	c.check(context.Background())
	if got := c.Status(); !got.Behind || got.Latest != "2026.09.11.0900" {
		t.Errorf("a failed check lost the last answer: %+v", got)
	}

	// Nonsense in the body is a failure too, not an answer.
	fail.Store(false)
	c2 := githubStub(t, nil, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`<html>`)) })
	c2.check(context.Background())
	if got := c2.Status(); got.Latest != "" {
		t.Errorf("garbage was taken as an answer: %+v", got)
	}
}

// Start checks once immediately and then waits. It must not hammer GitHub, and a build from
// source must not call it at all.
func TestStartChecksOnceThenWaits(t *testing.T) {
	var hits atomic.Int64
	c := githubStub(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"2026.09.11.0900"}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for c.Status().Latest == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Status().Latest != "2026.09.11.0900" {
		t.Fatal("Start did not check on startup")
	}
	time.Sleep(50 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("checked %d times; the next one is not due for hours", n)
	}

	var devHits atomic.Int64
	dev := githubStub(t, &devHits, func(w http.ResponseWriter, r *http.Request) {})
	dev.current = DevVersion
	dev.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	if devHits.Load() != 0 {
		t.Error("a build from source should not ask GitHub anything")
	}
}
