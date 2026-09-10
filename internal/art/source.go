package art

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // some marks are served as jpeg
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// maxMark is the largest body worth treating as a crest. The real ones are tens of
// kilobytes; anything this size is an error page or a redirect to one.
const maxMark = 4 << 20

// marks fetches team crests and keeps them on disk. It is deliberately not the pipeline's
// Fetcher: that one cannot tell a 404 from a network failure, so it could never record
// "there is nothing here" and would re-ask on every render, forever.
type marks struct {
	base   string
	client *http.Client
	dir    string
	log    *slog.Logger
	now    func() time.Time
	posTTL time.Duration
	negTTL time.Duration

	sem    chan struct{} // bounds how many fetches are in flight at once
	flight singleflight.Group
	warn   sync.Once
}

// meta is what is known about one URL, written beside the body it describes. A status that
// is not 200 with no body beside it is the negative cache: the source has told us there is
// nothing to draw, and that answer is worth keeping.
type meta struct {
	URL     string    `json:"url"`
	ETag    string    `json:"etag,omitempty"`
	Fetched time.Time `json:"fetched"`
	Status  int       `json:"status"`
}

type mark struct {
	img  image.Image
	body []byte // as it arrived, so a channel logo can be served without redrawing it
	etag string // what the source called this version, for the art's own validator
}

// get returns the crest at url, or ok false when there is nothing to draw and the caller
// should letter the name instead. It never returns an error: every failure here has the
// same answer, which is to draw something rather than to serve nothing.
func (m *marks) get(ctx context.Context, url string) (mark, bool) {
	v, _, _ := m.flight.Do(url, func() (any, error) {
		got, ok := m.fetch(ctx, url)
		if !ok {
			return nil, nil
		}
		return got, nil
	})
	got, ok := v.(mark)
	return got, ok
}

func (m *marks) fetch(ctx context.Context, url string) (mark, bool) {
	body, md, cached := m.cached(url)
	fresh := cached && m.now().Sub(md.Fetched) < m.ttl(md.Status)
	switch {
	case fresh && md.Status != http.StatusOK:
		return mark{}, false // the source has said there is nothing here, recently enough
	case fresh:
		return decode(body, md.ETag)
	}

	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return mark{}, false
	}
	req.Header.Set("User-Agent", "epg3r")
	if cached && md.ETag != "" {
		req.Header.Set("If-None-Match", md.ETag)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		// Serve what we have rather than nothing, and do not come back for an hour.
		return m.stale(url, body, md, cached, err.Error())
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified && cached:
		md.Fetched = m.now()
		m.store(url, nil, md)
		return decode(body, md.ETag)
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		m.store(url, nil, meta{URL: url, Fetched: m.now(), Status: resp.StatusCode})
		return mark{}, false
	case resp.StatusCode != http.StatusOK:
		return m.stale(url, body, md, cached, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}

	next, err := io.ReadAll(io.LimitReader(resp.Body, maxMark+1))
	if err != nil || len(next) > maxMark {
		return m.stale(url, body, md, cached, "body unreadable or too large")
	}
	got, ok := decode(next, resp.Header.Get("ETag"))
	if !ok {
		// Not an image. Remember that, or every render asks again for the same non-picture.
		m.store(url, nil, meta{URL: url, Fetched: m.now(), Status: http.StatusUnsupportedMediaType})
		return mark{}, false
	}
	m.store(url, next, meta{URL: url, ETag: got.etag, Fetched: m.now(), Status: http.StatusOK})
	return got, true
}

// stale keeps whatever was already on disk usable and pushes the next attempt out an hour,
// so an unreachable source costs one request rather than one per render.
func (m *marks) stale(url string, body []byte, md meta, cached bool, why string) (mark, bool) {
	m.log.Warn("art: could not fetch mark", "url", url, "err", why, "cached", cached)
	if !cached {
		return mark{}, false
	}
	md.Fetched = m.now().Add(-m.ttl(md.Status)).Add(time.Hour)
	m.store(url, nil, md)
	if md.Status != http.StatusOK {
		return mark{}, false
	}
	return decode(body, md.ETag)
}

func (m *marks) ttl(status int) time.Duration {
	if status == http.StatusOK {
		return m.posTTL
	}
	return m.negTTL
}

func decode(body []byte, etag string) (mark, bool) {
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return mark{}, false
	}
	if etag == "" {
		sum := sha256.Sum256(body)
		etag = hex.EncodeToString(sum[:8])
	}
	return mark{img: img, body: body, etag: etag}, true
}

// Content addressing, so a cache key can never be anything but hex and there is no path to
// sanitize. Sharded one level, because a few thousand files in one directory is unkind.
func (m *marks) paths(url string) (body, side string) {
	sum := sha256.Sum256([]byte(url))
	name := hex.EncodeToString(sum[:])
	dir := filepath.Join(m.dir, name[:2])
	return filepath.Join(dir, name), filepath.Join(dir, name+".meta")
}

func (m *marks) cached(url string) ([]byte, meta, bool) {
	body, side := m.paths(url)
	raw, err := os.ReadFile(side)
	if err != nil {
		return nil, meta{}, false
	}
	var md meta
	if json.Unmarshal(raw, &md) != nil {
		return nil, meta{}, false
	}
	if md.Status != http.StatusOK {
		return nil, md, true // negative entries have no body by design
	}
	b, err := os.ReadFile(body)
	if err != nil {
		return nil, meta{}, false
	}
	return b, md, true
}

// store writes the sidecar, and the body when there is a new one. Every write is best
// effort: a data directory that cannot be written to should slow the app down, not stop it.
func (m *marks) store(url string, body []byte, md meta) {
	bodyPath, side := m.paths(url)
	if err := os.MkdirAll(filepath.Dir(side), 0o755); err != nil {
		m.cannotWrite(err)
		return
	}
	if body != nil {
		if err := writeFile(bodyPath, body); err != nil {
			m.cannotWrite(err)
			return
		}
	}
	raw, err := json.Marshal(md)
	if err != nil {
		return
	}
	if err := writeFile(side, raw); err != nil {
		m.cannotWrite(err)
	}
}

func (m *marks) cannotWrite(err error) {
	m.warn.Do(func() { m.log.Warn("art: cache directory is not writable; running from memory", "err", err) })
}

// writeFile replaces a file atomically, so a reader never sees half of one.
func writeFile(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}
