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

	"github.com/jonmaddox/epg3r/internal/art"
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
		if ch.Kind == model.KindSlot && strings.HasPrefix(ch.ID, "NFL 04") && len(ch.Programs) == 1 &&
			strings.Contains(ch.Programs[0].Event.SubTitle, "Buffalo Bills") && strings.Contains(ch.Programs[0].Event.SubTitle, "Houston Texans") {
			nfl04 = ch
		}
	}
	if nfl04 == nil {
		t.Fatal("no NFL 04 channel carrying Bills vs Texans")
	}
	if nfl04.Number%100 != 4 || nfl04.Number < 10000 || nfl04.Number >= 10800 {
		t.Errorf("NFL 04 number %d should be slot 4 of a family in the NFL block", nfl04.Number)
	}
	if first := findChannel(snap, "NFL 04"); first == nil || first.Number != 10004 {
		t.Errorf("first NFL family must use the plain numbering: %+v", first)
	}
	game := nfl04.Programs[0].Event
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
	if bills.Number < 10800 || bills.Number >= 11000 || !strings.HasPrefix(bills.ID, "NFL Bills") {
		t.Errorf("Bills channel identity: %s %d", bills.ID, bills.Number)
	}
	var shared bool
	for _, p := range bills.Programs {
		if p.Event.ID == game.ID {
			shared = true
			if p.Event.Source != model.OriginXMLTV {
				t.Errorf("guide should win for timing, got %s", p.Event.Source)
			}
			if p.Note == "" {
				t.Error("team channel program should carry a feed note")
			}
		}
	}
	if !shared {
		t.Errorf("Bills channel does not carry the Bills vs Texans game; has %d programs", len(bills.Programs))
	}
	// The Texans channel gets it too, projected from the same event.
	for i := range snap.Channels {
		ch := &snap.Channels[i]
		if ch.Kind == model.KindTeam && ch.Team != nil && ch.Team.Name == "Houston Texans" {
			var ok bool
			for _, p := range ch.Programs {
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
		if len(ch.Programs) == 0 {
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
	dups, _ := st.RunChannels(ctx, runs[0].ID, store.RunChannelFilter{Status: store.OutcomeDuplicate})
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
	ids := map[string]bool{}
	nums := map[int]int{}
	for _, ch := range snap.Channels {
		if ids[ch.ID] {
			t.Errorf("channel id %q exported twice", ch.ID)
		}
		ids[ch.ID] = true
		nums[ch.Number]++
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
	// Two lines with the same title but different URLs are two channels: the URL is
	// what makes a channel, and the title is only what it is called.
	if rep.Counts[store.OutcomeDuplicate] != 0 {
		t.Errorf("duplicates = %d, want 0", rep.Counts[store.OutcomeDuplicate])
	}
	if len(snap.Channels) != 6 {
		t.Errorf("channels = %d, want 6", len(snap.Channels))
	}
	if !ids["NFL 04"] || !ids["NFL 04 2"] {
		t.Errorf("both slot channels should be published, under distinct ids: %v", ids)
	}
}

// The same URL twice in one playlist is one channel, listed twice.
func TestRepeatedURLIsOneChannel(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/1\n")
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: url})
	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Channels) != 1 {
		t.Errorf("channels = %d, want 1", len(snap.Channels))
	}
	if rep.Counts[store.OutcomeDuplicate] != 1 {
		t.Errorf("duplicates = %d, want 1", rep.Counts[store.OutcomeDuplicate])
	}
}

func TestInterruptedRunIsMarkedFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, st := newRunner(t)
	// The playlist server cancels the run's context as soon as it is asked, so every
	// store call after the fetch fails with a canceled context.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cancel()
		w.Write([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"))
	}))
	defer srv.Close()
	st.CreateSource(context.Background(), store.NewSource{Name: "p", URL: srv.URL})

	if _, _, err := r.Run(ctx, store.TriggerManual); err == nil {
		t.Fatal("expected the canceled run to fail")
	}
	runs, err := st.ListRuns(context.Background(), 5)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs[0].Status != store.RunFailed || runs[0].FinishedAt == nil || runs[0].Error == "" {
		t.Errorf("run row should be finalized as failed: %+v", runs[0])
	}
}

func TestProbe(t *testing.T) {
	srv := fixtureServer(t)
	r, _ := newRunner(t)
	n, err := r.Probe(context.Background(), srv.URL+"/list.m3u")
	if err != nil || n != 1338 {
		t.Errorf("probe: %d %v", n, err)
	}
	if _, err := r.Probe(context.Background(), srv.URL+"/broken.m3u"); err == nil {
		t.Error("probe of a failing URL should error")
	}
	if _, err := r.Probe(context.Background(), srv.URL+"/guide.xml"); err == nil || !strings.Contains(err.Error(), "not an M3U") {
		t.Errorf("probe of a non-playlist should say so: %v", err)
	}
}

// Everything epg3r recognizes goes into the guide. There is no way to keep a channel
// out: what a consumer sees will be curated with collections instead.
func TestEverythingRecognizedIsExported(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/2\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/3\n"+
		"#EXTINF:-1 group-title=\"NBA\",NBA 01: Lakers vs Celtics (10.22 7:30PM ET)\nhttp://x/4\n"+
		"#EXTINF:-1 group-title=\"MLB\",MLB 03: Yankees vs Red Sox (09.13 7:05PM ET)\nhttp://x/5\n")
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: url})

	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, ch := range snap.Channels {
		ids[ch.ID] = true
	}
	for _, want := range []string{"NFL 04", "NFL 05", "NFL Bills", "NBA 01", "MLB 03"} {
		if !ids[want] {
			t.Errorf("%s should be in the guide: %v", want, ids)
		}
	}
	if len(ids) != 5 {
		t.Errorf("exported ids: %v", ids)
	}

	// A league override changes how airings read, never whether they are exported.
	title := "Pro Basketball"
	st.SetLeagueOverride(ctx, "nba", catalog.Override{AiringTitle: &title})
	snap, rep, err = r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Channels) != 5 {
		t.Errorf("channels after an override: %d, want 5", len(snap.Channels))
	}
	nba := findChannel(snap, "NBA 01")
	if nba == nil || len(nba.Programs) == 0 || nba.Programs[0].Event.Title != "Pro Basketball" {
		t.Errorf("the override should reach the airing: %+v", nba)
	}
	if rep.Counts[store.OutcomeExported] != 5 {
		t.Errorf("exported count %d, want 5: %v", rep.Counts[store.OutcomeExported], rep.Counts)
	}
}

// The point of keying a channel on its URL: next week every title on the playlist is
// different, and not one channel moves.
func TestNumbersSurviveTotalTitleChurn(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	week1 := "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/a\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/b\n" +
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/c\n" +
		"#EXTINF:-1 group-title=\"MLB\",MLB 03: Yankees vs Red Sox (09.13 7:05PM ET)\nhttp://x/d\n"
	// A week later: different games, different slots, a renamed team feed, a different
	// league on one line, and the lines in a different order.
	week2 := "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"NFL\",(NFL) Buffalo Bills (FHD)\nhttp://x/c\n" +
		"#EXTINF:-1 group-title=\"MLB\",MLB 11: Cubs vs Brewers (09.20 2:20PM ET)\nhttp://x/d\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 09: Rams vs 49ers (09.20 4:25PM ET)\nhttp://x/a\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 12: Chiefs vs Broncos (09.21 8:20PM ET)\nhttp://x/b\n"
	body := week1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: srv.URL + "/list.m3u"})

	byURL := func(snap *model.Snapshot) map[string][2]any {
		out := map[string][2]any{}
		for _, ch := range snap.Channels {
			out[ch.StreamURL] = [2]any{ch.ID, ch.Number}
		}
		return out
	}
	first, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	before := byURL(first)
	if len(before) != 4 {
		t.Fatalf("first run: %v", before)
	}

	body = week2
	second, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	after := byURL(second)
	if len(after) != 4 {
		t.Fatalf("second run: %v", after)
	}
	for url, was := range before {
		if now, ok := after[url]; !ok || now != was {
			t.Errorf("%s was %v, is now %v", url, was, now)
		}
	}
}

// A channel that leaves the playlist keeps its number for a while, and takes it back
// when it returns. It is only forgotten, and its number freed, once it has been gone
// long enough that nothing is coming back for it.
func TestANumberIsHeldWhileAChannelIsAway(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	full := "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/a\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/b\n"
	gone := "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/b\n"
	body := full
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: srv.URL + "/list.m3u"})

	first, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	var was model.Channel
	for _, ch := range first.Channels {
		if ch.StreamURL == "http://x/a" {
			was = ch
		}
	}
	if was.Number == 0 {
		t.Fatal("the channel should have been numbered")
	}

	// Off the playlist: gone from the guide, but its number is not handed to anyone.
	body = gone
	if _, _, err = r.Run(ctx, store.TriggerManual); err != nil {
		t.Fatal(err)
	}
	body = full
	third, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range third.Channels {
		if ch.StreamURL == "http://x/a" && (ch.Number != was.Number || ch.ID != was.ID) {
			t.Errorf("came back as %s/%d, was %s/%d", ch.ID, ch.Number, was.ID, was.Number)
		}
	}
}

// A slot number beyond the league's span is a preference the store cannot honour, not a
// reason to drop the channel: what the label says has no bearing on whether a URL is a
// channel.
func TestAnAwkwardSlotLabelStillGetsAChannel(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 104: Bears vs Packers (09.14 8:15PM ET)\nhttp://x/2\n")
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: url})
	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Channels) != 2 {
		t.Fatalf("both lines should be channels, got %d", len(snap.Channels))
	}
	nums := map[int]bool{}
	for _, ch := range snap.Channels {
		if ch.Number == 0 || ch.ID == "" || ch.Key == "" {
			t.Errorf("channel published without an identity: %+v", ch)
		}
		if nums[ch.Number] {
			t.Errorf("two channels on number %d", ch.Number)
		}
		nums[ch.Number] = true
	}
}

// A provider's XMLTV is the most expensive thing a run reads, and most refreshes are
// told nothing has changed. The parse from last time answers those.
func TestGuideIsParsedOncePerChange(t *testing.T) {
	r, _ := newRunner(t)
	body := []byte(`<tv><programme start="20260913164500 +0000" stop="20260913194500 +0000" channel="c1"><title>NFL Football</title></programme></tv>`)

	first, err := r.guide(1, FetchResult{Body: body, Status: store.FetchFresh})
	if err != nil || first == nil {
		t.Fatalf("first parse: %v", err)
	}
	// Nothing changed: the same guide comes back, and the bytes are not looked at.
	again, err := r.guide(1, FetchResult{Body: []byte("not xml at all"), Status: store.FetchNotModified})
	if err != nil || again != first {
		t.Errorf("an unchanged guide should not be parsed again: %v", err)
	}
	// A different source keeps its own.
	other, err := r.guide(2, FetchResult{Body: body, Status: store.FetchFresh})
	if err != nil || other == first {
		t.Errorf("guides are cached per source: %v", err)
	}
	// Fresh bytes are always parsed.
	changed, err := r.guide(1, FetchResult{Body: body, Status: store.FetchFresh})
	if err != nil || changed == first {
		t.Errorf("a fresh guide should be parsed: %v", err)
	}
	// A first sighting that is already unchanged still has to be parsed.
	cold, err := r.guide(3, FetchResult{Body: body, Status: store.FetchNotModified})
	if err != nil || cold == nil {
		t.Errorf("nothing cached yet, so parse it: %v", err)
	}

	// A source that no longer has a guide does not keep its programs in memory.
	r.forgetGuides(map[int64]bool{1: true})
	if len(r.guides) != 1 || r.guides[1] == nil {
		t.Errorf("only the guides still in use should be kept: %v", r.guides)
	}
}

// Numbers are reclaimed, because nothing can map a provider's new stream URLs onto the
// channels they replaced: without this, one URL change would leave a league's block
// full of channels that no longer exist and new ones would go unnumbered.
func TestForgottenChannelsGiveTheirNumbersBack(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	first := "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://old.example/1\n" +
		"#EXTINF:-1 group-title=\"NFL\",NFL 05: Bears vs Packers (09.14 8:15PM ET)\nhttp://old.example/2\n"
	// The provider moves house: the same channels, entirely new URLs, so entirely new
	// channels as far as anything here can tell.
	moved := strings.ReplaceAll(first, "http://old.example", "http://new.example")
	body := first
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	st.CreateSource(ctx, store.NewSource{Name: "p", URL: srv.URL + "/list.m3u"})
	clock := time.Now()
	st.SetClock(func() time.Time { return clock })
	r.Now = func() time.Time { return clock }

	before, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	was := map[string]int{}
	for _, ch := range before.Channels {
		was[ch.ID] = ch.Number
	}
	if len(was) != 2 {
		t.Fatalf("first run: %v", was)
	}

	// Straight after the move both sets are on the books, so the new channels take
	// numbers beside the old rather than on top of them.
	body = moved
	if _, _, err = r.Run(ctx, store.TriggerManual); err != nil {
		t.Fatal(err)
	}
	all, _ := st.Channels(ctx, 1)
	if len(all) != 4 {
		t.Fatalf("old and new channels should both be held: %d", len(all))
	}

	// A fortnight on, the ones that moved away are past their grace, so the run forgets
	// them and their numbers are free for whoever comes next.
	clock = clock.Add(15 * 24 * time.Hour)
	if _, _, err = r.Run(ctx, store.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if all, _ = st.Channels(ctx, 1); len(all) != 2 {
		t.Errorf("only the channels that exist should be left: %d rows", len(all))
	}
	// A third set of URLs now takes the numbers the first set gave up.
	body = strings.ReplaceAll(first, "http://old.example", "http://newer.example")
	clock = clock.Add(20 * 24 * time.Hour)
	if _, _, err = r.Run(ctx, store.TriggerManual); err != nil {
		t.Fatal(err)
	}
	final, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	free := map[int]bool{}
	for _, ch := range final.Channels {
		free[ch.Number] = true
	}
	for id, num := range was {
		if !free[num] {
			t.Errorf("%s's number %d was never handed out again: %v", id, num, free)
		}
	}
}

// Every channel and every airing points at a picture, and which one is decided here rather
// than by whatever the provider happened to send.
func TestArtIsPointedAt(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		// A provider logo on the slot channel: ours wins, but the run still records theirs.
		"#EXTINF:-1 tvg-logo=\"http://provider/nfl04.png\" tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/2\n"+
		// Nothing scheduled, and a matchup whose second side is not a team we know.
		"#EXTINF:-1 tvg-name=\"NFL 07\" group-title=\"NFL\",NFL 07: No Event Scheduled\nhttp://x/3\n"+
		"#EXTINF:-1 tvg-name=\"NFL 08\" group-title=\"NFL\",NFL 08: Bills vs Sheffield Steelers (09.13 1:00PM ET)\nhttp://x/4\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.SettingEmitPlaceholderProg, "1"); err != nil {
		t.Fatal(err)
	}
	snap, rep, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	slot := findChannel(snap, "NFL 04")
	if slot == nil {
		t.Fatal("no slot channel")
	}
	if slot.LogoURL != art.LeagueLogoPath("nfl") {
		t.Errorf("slot logo = %q, want the league's", slot.LogoURL)
	}
	// Both sides resolved, so the airing wears the matchup, in the order the title read.
	if got, want := slot.Programs[0].Event.PlacardURL,
		art.MatchupPlacardPath("nfl", "buffalo-bills", "houston-texans"); got != want {
		t.Errorf("matchup art = %q, want %q", got, want)
	}

	// A team channel wears its own team, not its league.
	var team *model.Channel
	for i := range snap.Channels {
		if snap.Channels[i].Kind == model.KindTeam {
			team = &snap.Channels[i]
		}
	}
	if team == nil {
		t.Fatal("no team channel")
	}
	if got, want := team.LogoURL, art.TeamLogoPath("nfl", "buffalo-bills"); got != want {
		t.Errorf("team logo = %q, want %q", got, want)
	}

	// One side unresolved, and a channel carrying nothing at all, both fall to the league.
	for _, id := range []string{"NFL 07", "NFL 08"} {
		ch := findChannel(snap, id)
		if ch == nil {
			t.Fatalf("no channel %s", id)
		}
		if len(ch.Programs) == 0 {
			t.Fatalf("%s carries nothing", id)
		}
		if got := ch.Programs[0].Event.PlacardURL; got != art.LeaguePlacardPath("nfl") {
			t.Errorf("%s art = %q, want the league's", id, got)
		}
	}

	// The provider's logo is not thrown away, it is just not what goes out.
	rows, err := st.RunChannels(ctx, rep.RunID, store.RunChannelFilter{Query: "NFL 04"})
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	for _, row := range rows {
		if row.ChannelID == "NFL 04" {
			sent = row.TvgLogo
		}
	}
	if sent != "http://provider/nfl04.png" {
		t.Errorf("the run should still record what the provider sent, got %q", sent)
	}
}

// The league art fields are overrides over what epg3r draws, and each replaces only what it
// stands in for: a league logo is not every team's logo, and a league placard is not a game.
func TestLeagueArtOverrides(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/2\n"+
		"#EXTINF:-1 tvg-name=\"NFL 07\" group-title=\"NFL\",NFL 07: No Event Scheduled\nhttp://x/3\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.SettingEmitPlaceholderProg, "1"); err != nil {
		t.Fatal(err)
	}
	logo, placard := "http://mine/logo.png", "http://mine/placard.png"
	if err := st.SetLeagueOverride(ctx, "nfl", catalog.Override{Logo: &logo, Placard: &placard}); err != nil {
		t.Fatal(err)
	}
	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	if got := findChannel(snap, "NFL 04").LogoURL; got != logo {
		t.Errorf("slot logo = %q, want the override", got)
	}
	for i := range snap.Channels {
		if snap.Channels[i].Kind == model.KindTeam && snap.Channels[i].LogoURL != art.TeamLogoPath("nfl", "buffalo-bills") {
			t.Errorf("a league logo override reached a team channel: %q", snap.Channels[i].LogoURL)
		}
	}
	if got := findChannel(snap, "NFL 07").Programs[0].Event.PlacardURL; got != placard {
		t.Errorf("unresolved airing art = %q, want the override", got)
	}
	if got, want := findChannel(snap, "NFL 04").Programs[0].Event.PlacardURL,
		art.MatchupPlacardPath("nfl", "buffalo-bills", "houston-texans"); got != want {
		t.Errorf("a placard override beat a matchup: %q", got)
	}
}

// The playlist's guide attributes are what a consumer falls back to when it has no guide at
// all, and it repeats them over every hour it invents for the channel. So they describe the
// channel, never a game: a matchup here would claim one fixture is on all day, every day.
func TestGuideTagsDescribeTheChannel(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/2\n"+
		"#EXTINF:-1 tvg-name=\"NFL 07\" group-title=\"NFL\",NFL 07: No Event Scheduled\nhttp://x/3\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	slot := findChannel(snap, "NFL 04")
	if slot.GuideTitle != "NFL Football" || slot.GuideText != "Live NFL games." {
		t.Errorf("slot channel says %q / %q", slot.GuideTitle, slot.GuideText)
	}
	if len(slot.Programs) == 0 || strings.Contains(slot.GuideText, slot.Programs[0].Event.SubTitle) {
		t.Errorf("the channel's own text names the game it happens to be carrying: %q", slot.GuideText)
	}

	// A team channel is a team all the time, so it may say so.
	var team *model.Channel
	for i := range snap.Channels {
		if snap.Channels[i].Kind == model.KindTeam {
			team = &snap.Channels[i]
		}
	}
	if team == nil {
		t.Fatal("no team channel")
	}
	if team.GuideText != "Live Buffalo Bills games." {
		t.Errorf("team channel says %q", team.GuideText)
	}

	if slot.GuideArt != art.LeaguePlacardPath("nfl") {
		t.Errorf("channel art = %q, want the league's", slot.GuideArt)
	}

	// A channel with nothing scheduled still says what it is: that is the whole point.
	if idle := findChannel(snap, "NFL 07"); idle.GuideTitle == "" {
		t.Error("a channel carrying nothing should still describe itself")
	}
}

// The one start the user picks decides every channel number. Nothing in the pipeline knows
// about the setting: numbers are derived from each league's base, and the base is the start
// plus the league's place on the shelf.
func TestChannelsStartWhereTheUserSaid(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 tvg-name=\"NFL 04\" group-title=\"NFL\",NFL 04: Bills vs Texans (09.13 1:00PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 tvg-name=\"NFL 09\" group-title=\"NFL\",NFL 09: Jets vs Bears (09.13 1:00PM ET)\nhttp://x/2\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL Buffalo Bills (HD)\nhttp://x/3\n"+
		"#EXTINF:-1 tvg-name=\"MLB 02\" group-title=\"MLB\",MLB 02: Reds vs Cubs (09.13 1:00PM ET)\nhttp://x/4\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.SettingChannelStart, "3000"); err != nil {
		t.Fatal(err)
	}
	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	// The slot is the number: NFL 04 is start+4 and NFL 09 is start+9, with the numbers
	// between them left for the slots that own them.
	for id, want := range map[string]int{"NFL 04": 3004, "NFL 09": 3009} {
		ch := findChannel(snap, id)
		if ch == nil {
			t.Fatalf("no channel %s", id)
		}
		if ch.Number != want {
			t.Errorf("%s = %d, want %d", id, ch.Number, want)
		}
	}
	for _, n := range []int{3005, 3006, 3007, 3008} {
		for _, ch := range snap.Channels {
			if ch.Number == n {
				t.Errorf("%d should be a hole, but %s has it", n, ch.ID)
			}
		}
	}

	// Team channels keep their own band, 800 above the start.
	for _, ch := range snap.Channels {
		if ch.Kind == model.KindTeam && (ch.Number < 3800 || ch.Number >= 4000) {
			t.Errorf("team channel %s at %d, want the 3800 band", ch.ID, ch.Number)
		}
	}

	// Every other league moves with it, keeping its own block one along the shelf.
	if mlb := findChannel(snap, "MLB 02"); mlb == nil || mlb.Number != 4002 {
		t.Errorf("mlb should be in the second block: %+v", mlb)
	}
}

// A slot that was missing when the league was numbered gets its own number the moment it
// turns up. This is the whole point of deriving numbers rather than handing them out in
// order: nothing has to move to make room.
func TestMissingSlotsLeaveHoles(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	line := func(slot string) string {
		return "#EXTINF:-1 tvg-name=\"NFL " + slot + "\" group-title=\"NFL\",NFL " + slot +
			": Bills vs Texans (09.13 1:00PM ET)\nhttp://x/" + slot + "\n"
	}
	// The same source, serving a different playlist the second time round.
	body := "#EXTM3U\n" + line("04") + line("09")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: srv.URL + "/list.m3u"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.SettingChannelStart, "3000"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(ctx, store.TriggerManual); err != nil {
		t.Fatal(err)
	}

	// NFL 06 shows up later and takes 3006, which was waiting for it.
	body = "#EXTM3U\n" + line("04") + line("06") + line("09")
	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if ch := findChannel(snap, "NFL 06"); ch == nil || ch.Number != 3006 {
		t.Fatalf("NFL 06 should land on 3006: %+v", ch)
	}
	// And the ones that were already there did not move to make room.
	for id, want := range map[string]int{"NFL 04": 3004, "NFL 09": 3009} {
		if ch := findChannel(snap, id); ch == nil || ch.Number != want {
			t.Errorf("%s moved: %+v", id, ch)
		}
	}
}

// A provider leaves a channel named after a game long after it has finished. Reading the
// time on such a channel as "today" puts last night's game on tonight, which is what this
// guards against: the day has to come from a channel that stated one.
//
// Taken from a real playlist. Three channels carry the same game; only two say which day.
func TestATimeWithNoDayIsPlacedByAnotherChannel(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 02: Rams vs 49ers (09.10 8:35PM ET)\nhttp://x/1\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 01: San Francisco 49ers @ Los Angeles Rams | 8:35 PM\nhttp://x/2\n"+
		"#EXTINF:-1 group-title=\"NFL\",US NFL San Francisco 49ers (HD)\nhttp://x/3\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	// The morning after the game: the dateless channel would otherwise read as tonight.
	r.Now = func() time.Time { return time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC) }

	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	var kickoffs []time.Time
	ids := map[string]bool{}
	for _, ch := range snap.Channels {
		for _, p := range ch.Programs {
			if p.Event.Teams[0] == nil {
				continue
			}
			kickoffs = append(kickoffs, p.Event.Kickoff)
			ids[p.Event.ID] = true
		}
	}
	if len(kickoffs) == 0 {
		t.Fatal("the game went missing entirely")
	}
	// One game, not two: the dateless channel joined the dated one rather than inventing a
	// second game a day later.
	if len(ids) != 1 {
		t.Errorf("want one game, got %d: %v", len(ids), ids)
	}
	for _, k := range kickoffs {
		if got := k.UTC().Format("2006-01-02"); got != "2026-09-11" {
			t.Errorf("kickoff on %s; the game is 2026-09-10 20:35 ET, which is 09-11 in UTC", got)
		}
		if k.UTC().Day() == 12 {
			t.Error("the game was placed a day late, which is the bug this guards")
		}
	}
	// Every channel carrying it got it, including the one whose name had no day.
	carrying := 0
	for _, ch := range snap.Channels {
		if len(ch.Programs) > 0 && ch.Programs[0].Event.Teams[0] != nil {
			carrying++
		}
	}
	if carrying < 3 {
		t.Errorf("only %d channels carry the game; the dateless one should have been placed", carrying)
	}
}

// With nothing else carrying the matchup there is no day to borrow, so the channel stays
// unscheduled rather than being guessed onto today.
func TestATimeWithNoDayAndNoOtherChannelIsNotGuessed(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	url := serveM3U(t, "#EXTM3U\n"+
		"#EXTINF:-1 group-title=\"NFL\",NFL 01: San Francisco 49ers @ Los Angeles Rams | 8:35 PM\nhttp://x/1\n")
	if _, err := st.CreateSource(ctx, store.NewSource{Name: "p", URL: url}); err != nil {
		t.Fatal(err)
	}
	r.Now = func() time.Time { return time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC) }

	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range snap.Channels {
		for _, p := range ch.Programs {
			if p.Event.Teams[0] != nil {
				t.Errorf("a game was invented from a title with no day: %s at %s",
					p.Event.SubTitle, p.Event.Kickoff)
			}
		}
	}
}

// Which side is home decides which way round the placard reads, and a channel name using
// "vs" does not say. A provider's guide often does, and whichever source knows settles it
// for every channel carrying the game.
//
// Taken from a real playlist: the slot channel says "Panthers vs Bears", the team channel's
// filler says "Chicago Bears at Carolina Panthers".
func TestWhoIsHomeDecidesTheMatchupOrder(t *testing.T) {
	ctx := context.Background()
	r, st := newRunner(t)
	guide := `<?xml version="1.0"?><tv>
      <channel id="US NFL Carolina Panthers (HD)"><display-name>Panthers</display-name></channel>
      <programme start="20260911050000 +0000" stop="20260911110000 +0000" channel="US NFL Carolina Panthers (HD)">
        <title>Next game: Chicago Bears at Carolina Panthers at 09/13/2026 01:00 PM (US/Eastern)</title>
      </programme>
    </tv>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, ".xml") {
			w.Write([]byte(guide))
			return
		}
		w.Write([]byte("#EXTM3U\n" +
			"#EXTINF:-1 group-title=\"NFL\",NFL 03: Panthers vs Bears (09.13 12:45PM ET)\nhttp://x/1\n" +
			"#EXTINF:-1 tvg-id=\"US NFL Carolina Panthers (HD)\" group-title=\"NFL\",US NFL Carolina Panthers (HD)\nhttp://x/2\n"))
	}))
	t.Cleanup(srv.Close)
	if _, err := st.CreateSource(ctx, store.NewSource{
		Name: "p", URL: srv.URL + "/list.m3u", XMLTVURL: srv.URL + "/guide.xml"}); err != nil {
		t.Fatal(err)
	}

	snap, _, err := r.Run(ctx, store.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range snap.Channels {
		for _, p := range ch.Programs {
			ev := p.Event
			if ev.Home == nil || ev.Away == nil {
				continue
			}
			found = true
			if ev.Away.Name != "Chicago Bears" || ev.Home.Name != "Carolina Panthers" {
				t.Errorf("%s: away %q, home %q", ch.ID, ev.Away.Name, ev.Home.Name)
			}
			// The name and the picture have to agree about which side is home, or the
			// mismatch is the first thing anyone notices.
			if ev.SubTitle != "Chicago Bears at Carolina Panthers" {
				t.Errorf("%s: sub-title %q", ch.ID, ev.SubTitle)
			}
			if want := "/chicago-bears/carolina-panthers.png"; !strings.HasSuffix(ev.PlacardURL, want) {
				t.Errorf("%s: placard %q, want it to end %q", ch.ID, ev.PlacardURL, want)
			}
			// One order, read by everything: the sides themselves are in it, so the team
			// ids the guide emits cannot disagree with the name or the picture either.
			if ev.Teams[0].Name != "Chicago Bears" || ev.Teams[1].Name != "Carolina Panthers" {
				t.Errorf("%s: sides are %q then %q", ch.ID, ev.Teams[0].Name, ev.Teams[1].Name)
			}
		}
	}
	if !found {
		t.Fatal("no game came out with a home side")
	}
}
