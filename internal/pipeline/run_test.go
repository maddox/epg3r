package pipeline

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/store/storetest"
)

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(path, file, etag string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			http.ServeFile(w, r, file)
		})
	}
	serve("/list.m3u", "../../testdata/m3u/all-sports.m3u", `"m3u-v1"`)
	serve("/guide.xml", "../../testdata/xmltv/all-sports.xmltv", `"xml-v1"`)
	mux.HandleFunc("/broken.m3u", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newRunner(t *testing.T) (*Runner, *store.Store) {
	t.Helper()
	st := storetest.Open(t)
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, ny)
	return &Runner{
		Store: st, Catalog: cat,
		Fetcher: &Fetcher{CacheDir: filepath.Join(t.TempDir(), "cache")},
		Now:     func() time.Time { return now },
		Log:     slog.New(slog.DiscardHandler),
	}, st
}

func findChannel(snap *model.Snapshot, id string) *model.Channel {
	for i := range snap.Channels {
		if snap.Channels[i].ID == id {
			return &snap.Channels[i]
		}
	}
	return nil
}

func TestRunAgainstRealFixtures(t *testing.T) {
	ctx := context.Background()
	srv := fixtureServer(t)
	r, st := newRunner(t)
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "Aggregate", URL: srv.URL + "/list.m3u", XMLTVURL: srv.URL + "/guide.xml"}); err != nil {
		t.Fatal(err)
	}

	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatalf("run: %v (problems: %v)", err, rep.Problems)
	}
	if rep.Status != store.RunOK || len(rep.Problems) != 0 {
		t.Errorf("status %s problems %v", rep.Status, rep.Problems)
	}
	if rep.Counts.Seen() != 1338 {
		t.Errorf("seen %d", rep.Counts.Seen())
	}
	if rep.Counts[store.OutcomeUnmatched] != 10 {
		t.Errorf("unmatched %d, want 10 (the G League slots)", rep.Counts[store.OutcomeUnmatched])
	}
	if rep.Counts[store.OutcomeNetwork] == 0 || rep.Counts[store.OutcomeExported] == 0 || rep.Counts[store.OutcomeIdle] == 0 {
		t.Errorf("counts look wrong: %+v", rep.Counts)
	}
	t.Logf("counts: %+v, channels exported: %d", rep.Counts, len(snap.Channels))

	// The slot channel whose title is "USA | NFL 04ⓧ: Buffalo Bills vs Houston Texans ...".
	// Its provider family depends on playlist order, so find it by content.
	var nfl04 *model.Channel
	for i := range snap.Channels {
		ch := &snap.Channels[i]
		if ch.Kind == model.KindSlot && strings.HasPrefix(ch.ID, "NFL 04") && len(ch.Programmes) == 1 &&
			strings.Contains(ch.Programmes[0].Event.SubTitle, "Buffalo Bills") && strings.Contains(ch.Programmes[0].Event.SubTitle, "Houston Texans") {
			nfl04 = ch
		}
	}
	if nfl04 == nil {
		t.Fatal("no NFL 04 channel carrying Bills vs Texans")
	}
	if nfl04.Number%100 != 4 || nfl04.Number < 8500 || nfl04.Number >= 9300 {
		t.Errorf("NFL 04 number %d should be slot 4 of a family in the NFL block", nfl04.Number)
	}
	if first := findChannel(snap, "NFL 04"); first == nil || first.Number != 8504 {
		t.Errorf("first NFL family must use the plain numbering: %+v", first)
	}
	game := nfl04.Programmes[0].Event
	if game.Teams[0] == nil || game.Teams[1] == nil || game.Teams[0].TMSBrandID != "34" || game.Teams[1].TMSBrandID != "43" {
		t.Errorf("NFL 04 game: %+v", game)
	}
	if !game.Kickoff.Equal(time.Date(2026, 9, 13, 13, 0, 0, 0, game.Kickoff.Location())) {
		t.Errorf("kickoff %s", game.Kickoff)
	}
	if !strings.HasPrefix(game.ID, "191277-") {
		t.Errorf("episode id should carry the NFL series id: %s", game.ID)
	}

	// The Bills team channel carries the same game, from the provider guide, with the same id.
	var bills *model.Channel
	for i := range snap.Channels {
		ch := &snap.Channels[i]
		if ch.Kind == model.KindTeam && ch.Team != nil && ch.Team.Name == "Buffalo Bills" && ch.LeagueKey == "nfl" {
			bills = ch
			break
		}
	}
	if bills == nil {
		t.Fatal("no Bills team channel")
	}
	if bills.Number < 9300 || bills.Number >= 9500 || !strings.HasPrefix(bills.ID, "NFL Bills") {
		t.Errorf("Bills channel identity: %s %d", bills.ID, bills.Number)
	}
	var shared bool
	for _, p := range bills.Programmes {
		if p.Event.ID == game.ID {
			shared = true
			if p.Event.Source != model.OriginXMLTV {
				t.Errorf("guide should win for timing, got %s", p.Event.Source)
			}
			if p.Note == "" {
				t.Error("team channel programme should carry a feed note")
			}
		}
	}
	if !shared {
		t.Errorf("Bills channel does not carry the Bills vs Texans game; has %d programmes", len(bills.Programmes))
	}
	// The Texans channel gets it too, projected from the same event.
	for i := range snap.Channels {
		ch := &snap.Channels[i]
		if ch.Kind == model.KindTeam && ch.Team != nil && ch.Team.Name == "Houston Texans" {
			var ok bool
			for _, p := range ch.Programmes {
				ok = ok || p.Event.ID == game.ID
			}
			if !ok {
				t.Error("Texans channel missing the shared game")
			}
		}
	}

	// Placeholders are exported as idle channels so the lineup stays stable.
	var idle int
	for _, ch := range snap.Channels {
		if len(ch.Programmes) == 0 {
			idle++
		}
	}
	if idle == 0 {
		t.Error("expected idle channels in the snapshot")
	}
	// Channel numbers are unique.
	seen := map[int]string{}
	for _, ch := range snap.Channels {
		if other, dup := seen[ch.Number]; dup {
			t.Errorf("channel number %d used by %q and %q", ch.Number, other, ch.ID)
		}
		seen[ch.Number] = ch.ID
	}

	// Persisted: the run, its rows, and the snapshot round trip.
	runs, _ := st.ListRuns(ctx, 5)
	if len(runs) != 1 || runs[0].Status != store.RunOK || runs[0].Counts.Seen() != 1338 || runs[0].Counts[store.OutcomeNetwork] != rep.Counts[store.OutcomeNetwork] {
		t.Errorf("persisted run: %+v", runs)
	}
	var loaded model.Snapshot
	if _, ok, err := st.LatestSnapshot(ctx, &loaded); err != nil || !ok || len(loaded.Channels) != len(snap.Channels) {
		t.Errorf("snapshot round trip: ok=%v err=%v n=%d", ok, err, len(loaded.Channels))
	}
	dups, _ := st.RunChannels(ctx, runs[0].ID, store.OutcomeDuplicate)
	t.Logf("%d duplicate slot entries recorded", len(dups))

	// Second run: the playlist is unchanged, so the ETag path is used and the output is identical.
	snap2, rep2, err := r.Run(ctx, store.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	srcs, _ := st.ListSources(ctx)
	if srcs[0].LastStatus != store.FetchNotModified {
		t.Errorf("second fetch status %q, want not-modified", srcs[0].LastStatus)
	}
	if len(snap2.Channels) != len(snap.Channels) || !maps.Equal(rep2.Counts, rep.Counts) {
		t.Errorf("second run differs: %d vs %d channels, %+v vs %+v", len(snap2.Channels), len(snap.Channels), rep2.Counts, rep.Counts)
	}
	if b2 := findChannel(snap2, bills.ID); b2 == nil || b2.Number != bills.Number {
		t.Error("team channel identity must be stable across runs")
	}
}

func TestRunWithFailingSourceIsPartial(t *testing.T) {
	ctx := context.Background()
	srv := fixtureServer(t)
	r, st := newRunner(t)
	st.CreateSource(ctx, store.NewSource{Name: "Good", URL: srv.URL + "/list.m3u"})
	st.CreateSource(ctx, store.NewSource{Name: "Bad", URL: srv.URL + "/broken.m3u"})

	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != store.RunPartial || len(rep.Problems) != 1 || len(snap.Channels) == 0 {
		t.Errorf("status=%s problems=%v channels=%d", rep.Status, rep.Problems, len(snap.Channels))
	}
	srcs, _ := st.ListSources(ctx)
	if srcs[1].LastStatus != store.FetchFailed || srcs[1].LastError == "" {
		t.Errorf("bad source not recorded: %+v", srcs[1])
	}
}

func TestRunWithNoDataFails(t *testing.T) {
	ctx := context.Background()
	srv := fixtureServer(t)
	r, st := newRunner(t)
	st.CreateSource(ctx, store.NewSource{Name: "Bad", URL: srv.URL + "/broken.m3u"})
	_, rep, err := r.Run(ctx, store.TriggerManual)
	if err == nil || rep.Status != store.RunFailed {
		t.Errorf("expected failure, got %v %+v", err, rep)
	}
}

func TestFetcherCacheFallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var down bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("#EXTM3U\n"))
	}))
	defer srv.Close()
	f := &Fetcher{CacheDir: dir}

	first, err := f.Fetch(ctx, srv.URL, "k.m3u")
	if err != nil || string(first.Body) != "#EXTM3U\n" || first.Status != store.FetchFresh {
		t.Fatalf("first: %+v %v", first, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "k.m3u")); err != nil {
		t.Fatal("body not cached")
	}
	if etag, _ := os.ReadFile(filepath.Join(dir, "k.m3u.etag")); string(etag) != `"v1"` {
		t.Errorf("etag sidecar = %q", etag)
	}
	down = true
	second, err := f.Fetch(ctx, srv.URL, "k.m3u")
	if err != nil || second.Status != store.FetchStale || second.Warning == nil || string(second.Body) != "#EXTM3U\n" {
		t.Errorf("expected the cached copy with a warning: %+v %v", second, err)
	}
}

// serveM3U serves an inline playlist and returns its URL.
func serveM3U(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	return srv.URL + "/list.m3u"
}

func TestTeamChannelsWithoutTvgNameStayDistinct(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Houston Texans (HD)\nhttp://x/2\n"+
		"#EXTINF:-1 tvg-name=\"NFL Team\" group-title=\"NFL\",(NFL) Pittsburgh Steelers (P)\nhttp://x/3\n"+
		"#EXTINF:-1 tvg-name=\"NFL Team\" group-title=\"NFL\",(NFL) Chicago Bears (P)\nhttp://x/4\n"+
		"#EXTINF:-1 tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/5\n"+
		"#EXTINF:-1 tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/6\n")
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: url})
	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	nums := map[int]int{}
	for _, ch := range snap.Channels {
		ids[ch.ID]++
		nums[ch.Number]++
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("channel id %q exported %d times", id, n)
		}
	}
	for num, n := range nums {
		if n > 1 {
			t.Errorf("channel number %d exported %d times", num, n)
		}
	}
	teams := 0
	for _, ch := range snap.Channels {
		if ch.Kind == model.KindTeam {
			teams++
		}
	}
	if teams != 4 {
		t.Errorf("expected 4 distinct team channels, got %d", teams)
	}
	// The repeated slot entry is a duplicate, not a second channel.
	if rep.Counts[store.OutcomeDuplicate] != 1 {
		t.Errorf("duplicates = %d, want 1", rep.Counts[store.OutcomeDuplicate])
	}
}
