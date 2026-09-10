package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/jonmaddox/epg3r/internal/m3u"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/xmltv"
)

// Snapshots holds the latest run output for the output endpoints. Rendering is done on
// demand and cached per collection and base URL, so Channels DVR polling costs nothing
// after the first request.
type Snapshots struct {
	current   atomic.Pointer[model.Snapshot]
	GuideTags func() bool // reads the m3u_tvc_guide_tags setting

	mu    sync.Mutex
	cache map[outputKey]rendered
}

// outputKey names one rendering: a collection, empty for the whole guide, as addressed
// under one base URL. The base belongs in the key rather than in a single slot per
// collection, because there is normally more than one: the media server polls by one name
// while the preview page renders under whatever name the browser used. Sharing a slot
// would have those two evict each other on every request and neither would ever be cached.
type outputKey struct {
	slug string
	base string
}

// maxRenderings caps the cache. A base can come from a request header, so without a cap
// anything that can reach the endpoint could grow this map without bound. A deployment is
// reached by a handful of names at most, so overflow means something is inventing them:
// drop the lot rather than track which was used least, since re-rendering is what an
// unrecognised name costs anyway.
const maxRenderings = 8

// renderState is everything about a rendering that can go out of date. Comparing it is
// how a cached entry is known to be still good.
type renderState struct {
	// snap is the snapshot itself, not its run id. Set always publishes a fresh pointer,
	// so this is exact; a run id is only a proxy, and Renumber republishes with the same
	// one, which a proxy would read as unchanged.
	snap    *model.Snapshot
	tags    bool   // guide-tags flag the cached m3u was rendered with
	changed string // the collection's own stamp; empty for the whole guide
}

// rendered is one output pair, kept until anything it was built from changes.
type rendered struct {
	renderState
	runID          int64 // ordering only, so a late request cannot rewind the cache
	xml, m3u       []byte
	xmlTag, m3uTag string // strong validators, computed once per render rather than per request
}

// Set publishes a snapshot.
func (s *Snapshots) Set(snap *model.Snapshot) { s.current.Store(snap) }

// Get returns the current snapshot or nil.
func (s *Snapshots) Get() *model.Snapshot { return s.current.Load() }

// Renumber publishes a copy of the current snapshot with these channels moved. The store is
// where a channel's number lives and it has just been written, so the guide says so at once
// rather than serving the old numbers until the next refresh rebuilds it. The refresh that
// follows produces the same thing from scratch.
//
// byUser says whether these numbers were chosen by a person or derived from a league's
// start; a league being re-homed moves channels without anyone picking where they land.
func (s *Snapshots) Renumber(numbers map[string]int, byUser bool) {
	cur := s.Get()
	if cur == nil || len(numbers) == 0 {
		return
	}
	next := *cur
	next.Channels = slices.Clone(cur.Channels)
	for i := range next.Channels {
		if n, ok := numbers[next.Channels[i].Key]; ok {
			next.Channels[i].Number, next.Channels[i].ByUser = n, byUser
		}
	}
	next.SortChannels()
	s.Set(&next)
}

// render builds the outputs for a collection, or for the whole guide when c is nil, with
// every link inside addressed under base. Each is cached and kept until anything it was
// built from changes, so a consumer polling costs nothing after the first request. ok is
// false when there is no guide yet.
func (s *Snapshots) render(generator, base string, c *store.Collection, members map[string]bool) (rendered, bool) {
	key, changed := outputKey{base: base}, ""
	if c != nil {
		key.slug, changed = c.Slug, c.Updated
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read the current snapshot under the lock so a request that raced a newer Set
	// cannot overwrite the cache with an older run.
	snap := s.Get()
	if snap == nil {
		return rendered{}, false
	}
	state := renderState{snap: snap, tags: s.GuideTags != nil && s.GuideTags(), changed: changed}
	if was, hit := s.cache[key]; hit && was.renderState == state {
		return was, true
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
	if err := xmltv.Write(&xb, snap, xmltv.WriteOptions{Generator: generator, BaseURL: base}); err != nil {
		return rendered{}, false
	}
	if err := m3u.Write(&mb, snap, m3u.WriteOptions{GuideTags: state.tags, BaseURL: base}); err != nil {
		return rendered{}, false
	}
	// Clip: a bytes.Buffer grows by doubling, so it can end a render nearly half empty,
	// and what is kept here is kept for as long as the entry is.
	out := rendered{renderState: state, runID: snap.RunID,
		xml: slices.Clip(xb.Bytes()), m3u: slices.Clip(mb.Bytes())}
	out.xmlTag, out.m3uTag = etag(out.xml), etag(out.m3u)
	if was := s.cache[key]; snap.RunID < was.runID {
		return out, true // serve, but never move the cache backwards
	}
	if s.cache == nil {
		s.cache = map[outputKey]rendered{}
	}
	if len(s.cache) >= maxRenderings {
		clear(s.cache)
	}
	s.cache[key] = out
	return out, true
}

// etag names the content, not the run that produced it. Naming the run gets both halves
// wrong: the guide-tags setting, a collection's membership and the base URL all change the
// body while the run stays put, so a client revalidating would keep bytes that moved; and a
// refresh that finds nothing new rebuilds the same bytes under a new run, so every consumer
// would download the whole guide again for nothing. Hashing the body is right on both counts,
// and which run is being served is a question for the dashboard.
func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
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
	out, built := s.Snapshots.render("epg3r "+s.Version, s.baseURL(r), c, members)
	if !built {
		http.Error(w, "no "+what+" generated yet; the first refresh has not completed", http.StatusServiceUnavailable)
		return
	}
	name := "epg3r"
	if c != nil {
		name = c.Slug
	}
	if what == "guide" {
		serveOutput(w, r, out.xml, out.xmlTag, "application/xml; charset=utf-8", name+".xml")
		return
	}
	serveOutput(w, r, out.m3u, out.m3uTag, "audio/x-mpegurl; charset=utf-8", name+".m3u")
}

func serveOutput(w http.ResponseWriter, r *http.Request, body []byte, tag, contentType, filename string) {
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
