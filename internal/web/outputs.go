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
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/xmltv"
)

// Snapshots holds the latest run output for the output endpoints. Rendering is done
// on demand and cached per run, so Channels DVR polling costs nothing after the first
// request.
type Snapshots struct {
	current   atomic.Pointer[model.Snapshot]
	GuideTags func() bool // reads the m3u_tvc_guide_tags setting

	mu    sync.Mutex
	cache map[string]rendered // by collection slug; "" is the whole guide
}

// rendered is one output pair, kept until the run, the settings, or the collection
// behind it change.
type rendered struct {
	runID   int64
	tags    bool   // guide-tags flag the cached m3u was rendered with
	changed string // the collection's own stamp; empty for the whole guide
	xml     []byte
	m3u     []byte
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

// render builds the outputs for a collection, or for the whole guide when c is nil.
// Each is cached under its slug and kept until the run, the settings or the collection
// itself changes, so a consumer polling costs nothing after the first request. ok is
// false when there is no guide yet.
func (s *Snapshots) render(generator string, c *store.Collection, members map[string]bool) (xmlOut, m3uOut []byte, runID int64, ok bool) {
	slug, changed := "", ""
	if c != nil {
		slug, changed = c.Slug, c.Updated
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read the current snapshot under the lock so a request that raced a newer Set
	// cannot overwrite the cache with an older run.
	snap := s.Get()
	if snap == nil {
		return nil, nil, 0, false
	}
	tags := s.GuideTags != nil && s.GuideTags()
	if was, hit := s.cache[slug]; hit && was.runID == snap.RunID && was.tags == tags && was.changed == changed {
		return was.xml, was.m3u, was.runID, true
	}
	if members != nil {
		picked := *snap
		picked.Channels = nil
		for _, ch := range snap.Channels {
			if members[ch.Key] {
				picked.Channels = append(picked.Channels, ch)
			}
		}
		snap = &picked
	}
	var xb, mb bytes.Buffer
	if err := xmltv.Write(&xb, snap, generator); err != nil {
		return nil, nil, 0, false
	}
	if err := m3u.Write(&mb, snap, m3u.WriteOptions{GuideTags: tags, Now: time.Now()}); err != nil {
		return nil, nil, 0, false
	}
	if was := s.cache[slug]; snap.RunID < was.runID {
		return xb.Bytes(), mb.Bytes(), snap.RunID, true // serve, but never move the cache backwards
	}
	if s.cache == nil {
		s.cache = map[string]rendered{}
	}
	out := rendered{runID: snap.RunID, tags: tags, changed: changed, xml: xb.Bytes(), m3u: mb.Bytes()}
	s.cache[slug] = out
	return out.xml, out.m3u, snap.RunID, true
}

// collected resolves the collection a request is asking for, or nil for the whole
// guide. A URL naming a collection that is not there is a 404 and must be answered
// before anything is rendered: rendering an unresolved slug would cache the whole guide
// under a name nothing will ever ask for again.
func (s *Server) collected(w http.ResponseWriter, r *http.Request) (c *store.Collection, members map[string]bool, ok bool) {
	slug := r.PathValue("collection")
	if slug == "" {
		return nil, nil, true
	}
	found, exists, err := s.Store.CollectionBySlug(r.Context(), slug)
	if err != nil {
		s.fail(w, r, err)
		return nil, nil, false
	}
	if !exists {
		http.Error(w, "no collection called "+slug, http.StatusNotFound)
		return nil, nil, false
	}
	if members, err = s.Store.CollectionMembers(r.Context(), found.ID); err != nil {
		s.fail(w, r, err)
		return nil, nil, false
	}
	return &found, members, true
}

func (s *Server) handleXMLTV(w http.ResponseWriter, r *http.Request) {
	s.serveGuide(w, r, "guide")
}

func (s *Server) handleM3U(w http.ResponseWriter, r *http.Request) {
	s.serveGuide(w, r, "playlist")
}

// serveGuide answers one of the two outputs, for the whole guide or for the collection
// the URL names.
func (s *Server) serveGuide(w http.ResponseWriter, r *http.Request, what string) {
	c, members, ok := s.collected(w, r)
	if !ok {
		return // collected has answered
	}
	xb, mb, runID, built := s.Snapshots.render("epg3r "+s.Version, c, members)
	if !built {
		http.Error(w, "no "+what+" generated yet; the first refresh has not completed", http.StatusServiceUnavailable)
		return
	}
	name := "epg3r"
	if c != nil {
		name = c.Slug
	}
	if what == "guide" {
		serveOutput(w, r, xb, runID, "application/xml; charset=utf-8", name+".xml")
		return
	}
	serveOutput(w, r, mb, runID, "audio/x-mpegurl; charset=utf-8", name+".m3u")
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
