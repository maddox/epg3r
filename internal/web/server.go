// Package web is the HTTP layer: output endpoints Channels DVR pulls, the admin UI,
// and the health probe. Handlers are thin; work happens in the packages they call.
package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
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
	Version   string
	Log       *slog.Logger
	Snapshots *Snapshots
	started   time.Time
}

// New creates a Server.
func New(version string, log *slog.Logger) *Server {
	return &Server{Version: version, Log: log, Snapshots: &Snapshots{}, started: time.Now()}
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
