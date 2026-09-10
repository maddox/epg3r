// Package release knows which version is running and which one is published, so the UI can
// tell someone their container is behind.
//
// The check is deliberately kept off the request path and deliberately quiet: a person
// running this app cares about a new version eventually, never urgently, and GitHub being
// unreachable is not their problem to see.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Repo is where releases are published, as owner/name.
const Repo = "maddox/epg3r"

// DevVersion is what a build carries when nothing stamped it. A build from source is never
// behind: there is no telling what is in it.
const DevVersion = "dev"

// every is how often the latest release is looked up. Releases are cut on a merge, so a
// person is at most this far behind knowing, and 60 unauthenticated requests an hour is
// four times what this needs.
const every = 6 * time.Hour

// stamp matches the version a release carries: a UTC timestamp, minute precision, with
// seconds appended only when two releases landed inside one minute. Fixed width and zero
// padded, so comparing two of them as strings compares them as times.
var stamp = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}\.\d{4}(\d{2})?$`)

// Status is what the UI renders: never an error, because a failed check is not something to
// put in front of anyone.
type Status struct {
	Current string // the running build
	Latest  string // the newest published release, empty until a check succeeds
	Behind  bool
	URL     string // the release to send someone to, empty when there is nothing to link
}

// Checker holds the last answer GitHub gave.
type Checker struct {
	repo    string
	current string
	base    string
	client  *http.Client
	log     *slog.Logger

	mu     sync.RWMutex
	latest string
	url    string
}

// New returns a checker for a repo. It has not looked anything up yet; Start does that.
func New(repo, current string, log *slog.Logger) *Checker {
	return &Checker{
		repo:    repo,
		current: current,
		base:    "https://api.github.com",
		client:  &http.Client{Timeout: 10 * time.Second},
		log:     log,
	}
}

// Start looks the latest release up now and every few hours after, until ctx is canceled.
// A build with no version stamp never asks: there is nothing to compare against.
func (c *Checker) Start(ctx context.Context) {
	if c.current == "" || c.current == DevVersion {
		return
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			c.check(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Status reports what to show. Safe to call from any request.
func (c *Checker) Status() Status {
	c.mu.RLock()
	latest, url := c.latest, c.url
	c.mu.RUnlock()

	s := Status{Current: c.current, Latest: latest, URL: url}
	s.Behind = behind(c.current, latest)
	if !s.Behind {
		// Nothing newer, so the link points at what is running rather than at whatever
		// the last check happened to find.
		s.URL = c.releaseURL(c.current)
	}
	return s
}

// releaseURL is where a version's notes live, or the repo itself for a build from source.
func (c *Checker) releaseURL(version string) string {
	if !stamp.MatchString(version) {
		return "https://github.com/" + c.repo
	}
	return "https://github.com/" + c.repo + "/releases/tag/" + version
}

// check asks GitHub once. A failure keeps whatever the last successful check found and
// waits for the next tick rather than retrying: a GitHub outage should cost one quiet log
// line every few hours, not a request per page view.
func (c *Checker) check(ctx context.Context) {
	latest, url, err := c.fetch(ctx)
	if err != nil {
		c.log.Debug("could not check for a newer release", "err", err)
		return
	}
	c.mu.Lock()
	c.latest, c.url = latest, url
	c.mu.Unlock()
}

func (c *Checker) fetch(ctx context.Context) (tag, url string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/repos/"+c.repo+"/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "epg3r")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s said %s", c.repo, resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	return body.TagName, body.HTMLURL, nil
}

// behind reports whether latest is newer than current. Both have to look like a release
// stamp, so a hand-built container, a tag someone made by hand, or an empty answer from a
// failed check all read as "nothing to say" rather than as an upgrade.
//
// The comparison is greater-than rather than not-equal on purpose: a release deleted or
// rolled back would otherwise tell every running copy to upgrade to something older.
func behind(current, latest string) bool {
	if !stamp.MatchString(current) || !stamp.MatchString(latest) {
		return false
	}
	return latest > current
}
