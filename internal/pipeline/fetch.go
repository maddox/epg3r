package pipeline

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/store"
)

// Fetcher downloads source files with ETag revalidation and a disk cache, so an
// unchanged file costs one small request and a provider outage still leaves the last
// good copy to work from. The validator is kept beside the cached body, so every URL
// revalidates without the caller tracking anything.
type Fetcher struct {
	Client   *http.Client
	CacheDir string
	MaxBytes int64
}

// FetchResult is a usable body and how it was obtained.
type FetchResult struct {
	Body    []byte
	Status  store.FetchStatus // Fresh, NotModified, or Stale
	Warning error             // set with Stale: why the live request failed
}

// Fetch gets url. It returns an error only when nothing usable could be produced.
func (f *Fetcher) Fetch(ctx context.Context, url, cacheKey string) (FetchResult, error) {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	bodyPath := filepath.Join(f.CacheDir, cacheKey)
	etagPath := bodyPath + ".etag"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("User-Agent", "epg3r")
	req.Header.Set("Accept-Encoding", "gzip")
	if etag, err := os.ReadFile(etagPath); err == nil {
		if _, err := os.Stat(bodyPath); err == nil {
			req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
		}
	}

	stale := func(cause error) (FetchResult, error) {
		cached, err := os.ReadFile(bodyPath)
		if err != nil {
			return FetchResult{}, cause
		}
		return FetchResult{Body: cached, Status: store.FetchStale, Warning: cause}, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return stale(fmt.Errorf("fetch %s: %w", url, err))
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		cached, err := os.ReadFile(bodyPath)
		if err != nil {
			return FetchResult{}, fmt.Errorf("fetch %s: 304 but no cached copy", url)
		}
		return FetchResult{Body: cached, Status: store.FetchNotModified}, nil
	case resp.StatusCode != http.StatusOK:
		return stale(fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode))
	}

	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return FetchResult{}, fmt.Errorf("fetch %s: bad gzip: %w", url, err)
		}
		defer gz.Close()
		body = gz
	}
	limit := f.MaxBytes
	if limit <= 0 {
		limit = 200 << 20
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return stale(fmt.Errorf("fetch %s: %w", url, err))
	}
	if int64(len(data)) > limit {
		return FetchResult{}, fmt.Errorf("fetch %s: body exceeds %d bytes", url, limit)
	}

	if f.CacheDir != "" {
		if err := os.MkdirAll(f.CacheDir, 0o755); err == nil {
			tmp := bodyPath + ".tmp"
			if err := os.WriteFile(tmp, data, 0o644); err == nil {
				_ = os.Rename(tmp, bodyPath)
			}
			if etag := resp.Header.Get("ETag"); etag != "" {
				_ = os.WriteFile(etagPath, []byte(etag), 0o644)
			} else {
				_ = os.Remove(etagPath)
			}
		}
	}
	return FetchResult{Body: data, Status: store.FetchFresh}, nil
}
