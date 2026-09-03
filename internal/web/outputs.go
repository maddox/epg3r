package web

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonmaddox/epg3r/internal/m3u"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/xmltv"
)

// Snapshots holds the latest run output for the output endpoints. Rendering is done
// on demand and cached per run, so Channels DVR polling costs nothing after the first
// request.
type Snapshots struct {
	current   atomic.Pointer[model.Snapshot]
	GuideTags func() bool // reads the m3u_tvc_guide_tags setting

	mu    sync.Mutex
	runID int64
	tags  bool // guide-tags flag the cached m3u was rendered with
	xml   []byte
	m3u   []byte
}

// Set publishes a snapshot.
func (s *Snapshots) Set(snap *model.Snapshot) { s.current.Store(snap) }

// Get returns the current snapshot or nil.
func (s *Snapshots) Get() *model.Snapshot { return s.current.Load() }

// Renumber publishes a copy of the current snapshot with these channels moved. The
// store is where a channel's number lives and it has just been written, so the guide
// says so at once rather than serving the old numbers until the next refresh rebuilds
// it. The refresh that follows produces the same thing from scratch.
func (s *Snapshots) Renumber(numbers map[string]int) {
	cur := s.Get()
	if cur == nil || len(numbers) == 0 {
		return
	}
	next := *cur
	next.Channels = slices.Clone(cur.Channels)
	for i := range next.Channels {
		if n, ok := numbers[next.Channels[i].Key]; ok {
			next.Channels[i].Number, next.Channels[i].ByUser = n, true
		}
	}
	next.SortChannels()
	s.Set(&next)
}

func (s *Snapshots) render(generator string) (xmlOut, m3uOut []byte, runID int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read the current snapshot under the lock so a request that raced a newer Set
	// cannot overwrite the cache with an older run.
	snap := s.Get()
	if snap == nil {
		return nil, nil, 0, false
	}
	tags := s.GuideTags != nil && s.GuideTags()
	if s.runID == snap.RunID && s.tags == tags && s.xml != nil {
		return s.xml, s.m3u, s.runID, true
	}
	var xb, mb bytes.Buffer
	if err := xmltv.Write(&xb, snap, generator); err != nil {
		return nil, nil, 0, false
	}
	if err := m3u.Write(&mb, snap, m3u.WriteOptions{GuideTags: tags, Now: time.Now()}); err != nil {
		return nil, nil, 0, false
	}
	if snap.RunID < s.runID {
		return xb.Bytes(), mb.Bytes(), snap.RunID, true // serve, but never move the cache backwards
	}
	s.runID, s.tags, s.xml, s.m3u = snap.RunID, tags, xb.Bytes(), mb.Bytes()
	return s.xml, s.m3u, s.runID, true
}

func (s *Server) handleXMLTV(w http.ResponseWriter, r *http.Request) {
	xb, _, runID, ok := s.Snapshots.render("epg3r " + s.Version)
	if !ok {
		http.Error(w, "no guide generated yet; the first refresh has not completed", http.StatusServiceUnavailable)
		return
	}
	serveOutput(w, r, xb, runID, "application/xml; charset=utf-8", "epg3r.xml")
}

func (s *Server) handleM3U(w http.ResponseWriter, r *http.Request) {
	_, mb, runID, ok := s.Snapshots.render("epg3r " + s.Version)
	if !ok {
		http.Error(w, "no playlist generated yet; the first refresh has not completed", http.StatusServiceUnavailable)
		return
	}
	serveOutput(w, r, mb, runID, "audio/x-mpegurl; charset=utf-8", "epg3r.m3u")
}

func serveOutput(w http.ResponseWriter, r *http.Request, body []byte, runID int64, contentType, filename string) {
	etag := fmt.Sprintf(`"run-%d"`, runID)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
