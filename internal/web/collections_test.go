package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jonmaddox/epg3r/internal/store"
)

// A collection is the user's own set of channels, exported at its own URLs. Nothing
// else in the app decides what a consumer sees.
func TestCollections(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	st.CreateSource(ctx, store.NewSource{Name: "Provider", URL: "http://p/1"})
	urls := []string{"http://x/1", "http://x/2", "http://x/3", "http://x/4", "http://x/5"}
	st.SeeChannels(ctx, 1, urls) //nolint:errcheck
	snap := lineupSnapshot()
	for i := range snap.Channels {
		snap.Channels[i].Key = store.ChannelKey(1, urls[i])
	}
	s.Snapshots.Set(snap)
	key := func(i int) string { return store.ChannelKey(1, urls[i]) }

	if body := do(h, http.MethodGet, "/collections", nil, false).Body.String(); !strings.Contains(body, "No collections yet") {
		t.Error("empty state missing")
	}

	// Made from the Lineup, out of a selection, naming it there.
	rec := do(h, http.MethodPost, "/lineup/collect/new", url.Values{"key": {key(0), key(3)}, "new": {"My Favourites"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("collect: %d %s", rec.Code, rec.Body.String())
	}
	all, _ := st.Collections(ctx)
	if len(all) != 1 || all[0].Channels != 2 || all[0].Slug != "my-favourites" {
		t.Fatalf("collections: %+v", all)
	}
	id := strconv.FormatInt(all[0].ID, 10)

	// The page names the URLs it answers at, so they can be pasted into a consumer.
	body := do(h, http.MethodGet, "/collections", nil, false).Body.String()
	for _, want := range []string{"My Favourites", "/m3u/my-favourites", "/xmltv/my-favourites", "2 channels"} {
		if !strings.Contains(body, want) {
			t.Errorf("collections page missing %q", want)
		}
	}

	// Those URLs serve the collection's channels and no others.
	m3u := do(h, http.MethodGet, "/m3u/my-favourites", nil, false)
	if n := strings.Count(m3u.Body.String(), "#EXTINF"); n != 2 {
		t.Errorf("collection playlist has %d channels, want 2", n)
	}
	if n := strings.Count(do(h, http.MethodGet, "/m3u", nil, false).Body.String(), "#EXTINF"); n != 5 {
		t.Errorf("the whole guide is untouched: %d", n)
	}
	// A collection that is not there is not there, rather than "come back later".
	if rec := do(h, http.MethodGet, "/m3u/nothing-by-that-name", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown collection: %d", rec.Code)
	}

	// Adding to an existing one, and adding what is already there.
	do(h, http.MethodPost, "/lineup/collect", url.Values{"key": {key(1)}, "into": {id}}, true)
	do(h, http.MethodPost, "/lineup/collect", url.Values{"key": {key(1)}, "into": {id}}, true)
	if all, _ = st.Collections(ctx); all[0].Channels != 3 {
		t.Errorf("adding a duplicate should change nothing: %d", all[0].Channels)
	}
	if n := strings.Count(do(h, http.MethodGet, "/m3u/my-favourites", nil, false).Body.String(), "#EXTINF"); n != 3 {
		t.Errorf("the collection's playlist should follow its membership at once: %d", n)
	}

	// Looking at one narrows the Lineup to what is in it, and offers to take channels out.
	body = do(h, http.MethodGet, "/lineup?collection="+id, nil, false).Body.String()
	if got := strings.Count(body, `href="/lineup/`); got != 3 {
		t.Errorf("viewing a collection should show its 3 channels, got %d", got)
	}
	if !strings.Contains(body, "Remove from My Favourites") {
		t.Error("viewing a collection should offer to remove from it")
	}
	do(h, http.MethodPost, "/lineup/uncollect", url.Values{"key": {key(1)}, "from": {id}}, true)
	if all, _ = st.Collections(ctx); all[0].Channels != 2 {
		t.Errorf("after removing: %d", all[0].Channels)
	}

	// Renaming moves the URLs; deleting takes the collection and leaves the channels.
	do(h, http.MethodPut, "/collections/"+id, url.Values{"name": {"Washington"}}, true)
	if rec := do(h, http.MethodGet, "/m3u/washington", nil, false); rec.Code != http.StatusOK {
		t.Errorf("renamed collection should answer at its new URL: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/collections", url.Values{"name": {"washington"}}, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("two collections cannot share a URL: %d", rec.Code)
	}
	do(h, http.MethodDelete, "/collections/"+id, nil, true)
	if all, _ = st.Collections(ctx); len(all) != 0 {
		t.Errorf("after delete: %+v", all)
	}
	if n := strings.Count(do(h, http.MethodGet, "/m3u", nil, false).Body.String(), "#EXTINF"); n != 5 {
		t.Errorf("the channels themselves stay: %d", n)
	}
}
