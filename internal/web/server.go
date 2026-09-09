// Package web is the HTTP layer: output endpoints Channels DVR pulls, the admin UI,
// and the health probe. Handlers are thin; work happens in the packages they call.
package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/store"
)

// HealthPath is the liveness probe route.
const HealthPath = "/healthz"

// Health is what the probe reports.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
}

// Server holds the dependencies handlers need.
type Server struct {
	Version string

	Log        *slog.Logger
	Snapshots  *Snapshots
	Store      *store.Store
	Catalog    *catalog.Catalog
	Refresher  Refresher
	TestSource SourceTester

	// PublicBase reads the public_base_url setting. It arrives as a function, the way
	// Snapshots.GuideTags does, because the output routes are deliberately registered
	// outside the Store guard below and must keep answering without one.
	PublicBase func() string
	started    time.Time
	tpl        *templates
}

// New creates a Server. With dev set, templates and static files are read from the
// source tree on every request. zone is the zone the UI shows times in; nil means UTC.
func New(version string, log *slog.Logger, dev bool, zone func() *time.Location) *Server {
	return &Server{Version: version, Log: log, Snapshots: &Snapshots{}, started: time.Now(), tpl: newTemplates(dev, zone)}
}

// Output routes Channels DVR pulls.
const (
	XMLTVPath = "/xmltv"
	M3UPath   = "/m3u"
)

// Handler builds the routed, middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Polled routes are marked quiet so they log at debug instead of info.
	mux.Handle("GET "+HealthPath, quiet(http.HandlerFunc(s.handleHealth)))
	mux.Handle("GET "+XMLTVPath, quiet(http.HandlerFunc(s.handleXMLTV)))
	mux.Handle("GET /epg.xml", quiet(http.HandlerFunc(s.handleXMLTV)))
	mux.Handle("GET "+M3UPath, quiet(http.HandlerFunc(s.handleM3U)))
	// A collection is exported at its own URLs, so a consumer can be pointed at a set
	// of channels rather than at everything.
	mux.Handle("GET "+XMLTVPath+"/{collection}", quiet(http.HandlerFunc(s.handleXMLTV)))
	mux.Handle("GET "+M3UPath+"/{collection}", quiet(http.HandlerFunc(s.handleM3U)))

	static, _ := fs.Sub(s.tpl.fsys, "static")
	files := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", quiet(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.tpl.dev {
			// Without this the browser caches on its own guess and goes on running an
			// old script against a new page.
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})))

	if s.Store != nil {
		mux.HandleFunc("GET /{$}", s.handleDashboard)
		mux.HandleFunc("POST /refresh", s.handleRefresh)
		mux.Handle("GET /status", quiet(http.HandlerFunc(s.handleStatus)))
		mux.HandleFunc("GET /sources", s.handleSources)
		mux.HandleFunc("POST /sources", s.handleCreateSource)
		mux.HandleFunc("POST /sources/test", s.handleTestSource)
		mux.HandleFunc("GET /sources/{id}", s.sourceRow("source_row"))
		mux.HandleFunc("GET /sources/{id}/edit", s.sourceRow("source_row_edit"))
		mux.HandleFunc("PUT /sources/{id}", s.handleUpdateSource)
		mux.HandleFunc("DELETE /sources/{id}", s.handleDeleteSource)
		mux.HandleFunc("GET /runs", s.handleRuns)
		mux.HandleFunc("GET /runs/{id}", s.handleRun)
		mux.HandleFunc("GET /settings", s.handleSettings)
		mux.HandleFunc("PUT /settings", s.handleSaveSettings)
		mux.HandleFunc("GET /lineup", s.handleLineup)
		mux.HandleFunc("GET /lineup/{key}", s.handleChannel)
		mux.HandleFunc("POST /lineup/numbers", s.handleRenumber)
		mux.HandleFunc("POST /lineup/collect", s.handleAddToCollection)
		mux.HandleFunc("POST /lineup/collect/new", s.handleAddToNewCollection)
		mux.HandleFunc("POST /lineup/uncollect", s.handleRemoveFromCollection)
		mux.HandleFunc("GET /collections", s.handleCollections)
		mux.HandleFunc("POST /collections", s.handleSaveCollection)
		mux.HandleFunc("PUT /collections/{id}", s.handleSaveCollection)
		mux.HandleFunc("DELETE /collections/{id}", s.handleDeleteCollection)
		mux.HandleFunc("GET /leagues", s.handleLeagues)
		mux.HandleFunc("PUT /leagues/{key}", s.handleSaveLeague)
		mux.HandleFunc("DELETE /leagues/{key}", s.handleResetLeague)
		mux.HandleFunc("GET /preview/{kind}", s.handlePreview)
	}

	var h http.Handler = mux
	h = s.recoverer(h)
	h = s.accessLog(h)
	h = requestID(h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Health{
		Status:  "ok",
		Version: s.Version,
		Uptime:  time.Since(s.started).Truncate(time.Second).String(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- middleware ---

type ctxKey int

const (
	requestIDKey ctxKey = iota
	quietKey
)

// RequestID returns the id assigned to this request, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// reqInfo is planted in the context by accessLog so inner handlers can annotate
// how the request should be logged.
type reqInfo struct{ quiet bool }

// quiet marks a route as frequently polled; the access log demotes it to debug.
func quiet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, ok := r.Context().Value(quietKey).(*reqInfo); ok {
			info.quiet = true
		}
		next.ServeHTTP(w, r)
	})
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := rand.Text()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		info := &reqInfo{}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), quietKey, info)))

		level := slog.LevelInfo
		if info.quiet {
			level = slog.LevelDebug
		}
		s.Log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"dur", time.Since(start).Round(time.Microsecond),
			"req", RequestID(r.Context()),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic serving request", "path", r.URL.Path, "panic", rec, "req", RequestID(r.Context()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
