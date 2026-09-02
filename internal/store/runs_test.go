package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAllocateChannel(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})

	a, err := s.AllocateChannel(ctx, src, "nfl", "US NFL Buffalo Bills (HD)", "NFL Bills", 8550)
	if err != nil || a.ChannelID != "NFL Bills" || a.Number != 8550 {
		t.Fatalf("first alloc: %+v %v", a, err)
	}
	b, err := s.AllocateChannel(ctx, src, "nfl", "(NFL) Buffalo Bills (P)", "NFL Bills", 8550)
	if err != nil || b.ChannelID != "NFL Bills 2" || b.Number != 8551 {
		t.Fatalf("second feed of the same team: %+v %v", b, err)
	}
	c, err := s.AllocateChannel(ctx, src, "nfl", "US NFL Houston Texans (HD)", "NFL Texans", 8550)
	if err != nil || c.ChannelID != "NFL Texans" || c.Number != 8552 {
		t.Fatalf("other team: %+v %v", c, err)
	}
	again, err := s.AllocateChannel(ctx, src, "nfl", "US NFL Buffalo Bills (HD)", "NFL Bills", 8550)
	if err != nil || again != a {
		t.Fatalf("allocation must be sticky: %+v vs %+v", again, a)
	}
	other, err := s.AllocateChannel(ctx, src, "nba", "NBALP: Boston Celtics", "NBA Celtics", 8850)
	if err != nil || other.Number != 8850 {
		t.Fatalf("leagues allocate independently: %+v %v", other, err)
	}
	all, err := s.ChannelAllocs(ctx, src)
	if err != nil || len(all) != 4 || all[[2]string{"nfl", "US NFL Buffalo Bills (HD)"}] != a {
		t.Errorf("ChannelAllocs: %v %v", all, err)
	}
}

func TestRunsLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})

	if _, ok, err := s.LatestSnapshot(ctx, &map[string]any{}); err != nil || ok {
		t.Fatalf("no snapshot expected yet: %v %v", ok, err)
	}

	for i := 1; i <= 3; i++ {
		id, err := s.StartRun(ctx, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
		chans := []RunChannel{
			{SourceID: src, Group: "NFL", RawTitle: "NFL 04: A vs B", Status: OutcomeExported, LeagueKey: "nfl", Kind: "slot", ChannelID: "NFL 04", ChannelNumber: 8504, StartAt: &start, Confidence: 0.95},
			{SourceID: src, Group: "NFL", RawTitle: "NFL 06: Offline", Status: OutcomeIdle, LeagueKey: "nfl", Kind: "placeholder", ChannelID: "NFL 06", ChannelNumber: 8506},
			{SourceID: src, Group: "NFL", RawTitle: "USA: NFL NETWORK", Status: OutcomeNetwork, LeagueKey: "nfl", Kind: "network"},
		}
		snap := map[string]any{"run": i}
		if err := s.FinishRun(ctx, id, RunOK, "", chans, snap, 2); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := s.ListRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("keep=2 should prune to 2 runs, got %d", len(runs))
	}
	if runs[0].Status != RunOK || runs[0].Trigger != TriggerManual || runs[0].FinishedAt == nil {
		t.Errorf("latest run wrong: %+v", runs[0])
	}
	if runs[0].Counts.Seen() != 3 || runs[0].Counts[OutcomeExported] != 1 || runs[0].Counts[OutcomeNetwork] != 1 {
		t.Errorf("counts wrong: %v", runs[0].Counts)
	}

	var got map[string]any
	runID, ok, err := s.LatestSnapshot(ctx, &got)
	if err != nil || !ok || runID != runs[0].ID || got["run"].(float64) != 3 {
		t.Errorf("latest snapshot: id=%d ok=%v got=%v err=%v", runID, ok, got, err)
	}

	all, err := s.RunChannels(ctx, runs[0].ID, RunChannelFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("run channels: %d %v", len(all), err)
	}
	idle, _ := s.RunChannels(ctx, runs[0].ID, RunChannelFilter{Status: OutcomeIdle})
	if len(idle) != 1 || idle[0].ChannelID != "NFL 06" {
		t.Errorf("status filter wrong: %+v", idle)
	}
	if all[0].ChannelID != "NFL 04" || all[0].Kind != "slot" || all[0].Confidence != 0.95 || all[0].StartAt == nil || !all[0].StartAt.Equal(time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("first row wrong: %+v", all[0])
	}

	// Pruned runs cascade to their channel rows.
	var orphans int
	s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_channels WHERE run_id NOT IN (SELECT id FROM runs)`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("%d orphan run_channels rows", orphans)
	}
}

func TestUpsertGroup(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})
	if err := s.UpsertGroup(ctx, src, "NFL", 176, "nfl"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGroup(ctx, src, "NFL", 180, "nfl"); err != nil {
		t.Fatal(err)
	}
	var n, count int
	s.r.QueryRowContext(ctx, `SELECT COUNT(*), MAX(channel_count) FROM groups WHERE source_id = ?`, src).Scan(&n, &count)
	if n != 1 || count != 180 {
		t.Errorf("groups: n=%d count=%d", n, count)
	}
}

func TestSourceFetchRecord(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1", XMLTVURL: "http://p/guide.xml"})
	if err := s.RecordSourceFetch(ctx, src, SourceFetchResult{Status: FetchFresh, ChannelCount: 1338}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSources(ctx)
	got := list[0]
	if got.XMLTVURL != "http://p/guide.xml" || got.LastStatus != FetchFresh || got.LastChannelCount == nil || *got.LastChannelCount != 1338 || got.LastFetchedAt == nil {
		t.Errorf("derived columns wrong: %+v", got)
	}
}

func TestFamilyFor(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})

	// Off-season: only placeholders for this label style.
	f, ok, err := s.FamilyFor(ctx, src, "nfl", "NFL#:", "", "NFL 04: Offline", 4)
	if err != nil || !ok || f != 0 {
		t.Fatalf("placeholder family: %d %v %v", f, ok, err)
	}
	// Season starts: the first game with that label adopts the placeholder family.
	f, ok, _ = s.FamilyFor(ctx, src, "nfl", "NFL#:", "#.##:#AA", "NFL 04: A vs B (09.08 1:00PM ET)", 4)
	if !ok || f != 0 {
		t.Fatalf("adoption: %d %v", f, ok)
	}
	// A different provider with the same label but another schedule style is new.
	f, ok, _ = s.FamilyFor(ctx, src, "nfl", "NFL#:", "A@A|A|#:#AA", "NFL 04: A @ B | Home Stream | 8:05 PM ET", 4)
	if !ok || f != 1 {
		t.Fatalf("second style: %d %v", f, ok)
	}
	// Same styles are sticky.
	if f, _, _ = s.FamilyFor(ctx, src, "nfl", "NFL#:", "#.##:#AA", "", 4); f != 0 {
		t.Errorf("sticky: %d", f)
	}
	// A third label style, then the cap.
	s.FamilyFor(ctx, src, "nfl", "USA|NFL#:", "(#/#)#:#AA", "", 4)
	s.FamilyFor(ctx, src, "nfl", "NFL#|", "A##:#A", "", 4)
	if _, ok, _ := s.FamilyFor(ctx, src, "nfl", "(NFL#)|", "####:#:#", "", 4); ok {
		t.Error("fifth style should be refused at maxFamilies=4")
	}
	fams, _ := s.Families(ctx, src, "nfl")
	if len(fams) != 4 || fams[0].SchedShape != "#.##:#AA" || fams[1].Index != 1 {
		t.Errorf("families: %+v", fams)
	}
	if f, ok, _ := s.FamilyFor(ctx, src, "nba", "NBA#:", "", "", 4); !ok || f != 0 {
		t.Errorf("leagues are independent: %d %v", f, ok)
	}
}

func TestFailStaleRuns(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id, _ := s.StartRun(ctx, TriggerSchedule)
	done, _ := s.StartRun(ctx, TriggerSchedule)
	if err := s.FinishRun(ctx, done, RunOK, "", nil, map[string]any{}, 10); err != nil {
		t.Fatal(err)
	}
	n, err := s.FailStaleRuns(ctx)
	if err != nil || n != 1 {
		t.Fatalf("FailStaleRuns = %d %v, want 1", n, err)
	}
	runs, _ := s.ListRuns(ctx, 5)
	for _, r := range runs {
		switch r.ID {
		case id:
			if r.Status != RunFailed || r.FinishedAt == nil || r.Error == "" {
				t.Errorf("stale run not failed: %+v", r)
			}
		case done:
			if r.Status != RunOK {
				t.Errorf("finished run must be untouched: %+v", r)
			}
		}
	}
}

func TestRunChannelFilterAndSourceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})
	id, _ := s.StartRun(ctx, TriggerManual)
	rows := []RunChannel{
		{SourceID: src, RawTitle: "NFL 04: Bills vs Texans", Status: OutcomeExported, LeagueKey: "nfl", ChannelID: "NFL 04", ChannelNumber: 8504, Matchup: "Buffalo Bills vs Houston Texans"},
		{SourceID: src, RawTitle: "NBA 01: Offline", Status: OutcomeIdle, LeagueKey: "nba", ChannelID: "NBA 01", ChannelNumber: 11501},
		{SourceID: src, RawTitle: "USA: NFL NETWORK", Status: OutcomeNetwork, LeagueKey: "nfl"},
	}
	if err := s.FinishRun(ctx, id, RunOK, "", rows, map[string]any{}, 5); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunChannels(ctx, id, RunChannelFilter{League: "nfl"})
	if len(got) != 2 {
		t.Errorf("league filter: %d", len(got))
	}
	got, _ = s.RunChannels(ctx, id, RunChannelFilter{Query: "texans"})
	if len(got) != 1 || got[0].ChannelID != "NFL 04" {
		t.Errorf("query filter: %+v", got)
	}
	got, _ = s.RunChannels(ctx, id, RunChannelFilter{Limit: 1})
	if len(got) != 1 {
		t.Errorf("limit: %d", len(got))
	}
	if r, ok, _ := s.GetRun(ctx, id); !ok || r.Counts.Seen() != 3 {
		t.Errorf("GetRun: %v %+v", ok, r)
	}
	if _, ok, _ := s.GetRun(ctx, 999); ok {
		t.Error("GetRun should miss")
	}

	if err := s.UpdateSource(ctx, src, NewSource{Name: "renamed", URL: "http://p/2", XMLTVURL: "http://p/g.xml", Timezone: "America/Chicago", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	var verr *ValidationError
	if err := s.UpdateSource(ctx, src, NewSource{URL: "ftp://nope"}); !errors.As(err, &verr) {
		t.Errorf("bad url should be a ValidationError, got %v", err)
	}
	if _, err := s.CreateSource(ctx, NewSource{URL: "http://p/3", Timezone: "Mars/Base"}); !errors.As(err, &verr) {
		t.Errorf("bad zone should be a ValidationError, got %v", err)
	}
	if problems, err := s.SetSettings(ctx, map[string]string{SettingKeepRuns: "0", SettingRefreshOnStart: "yes"}); err != nil || len(problems) != 1 || problems[SettingKeepRuns] == "" {
		t.Errorf("SetSettings validation: %v %v", problems, err)
	}
	if v, _ := s.Setting(ctx, SettingRefreshOnStart); v != "1" {
		t.Error("a failed SetSettings must write nothing")
	}
	if problems, err := s.SetSettings(ctx, map[string]string{SettingKeepRuns: "5", SettingRefreshOnStart: "no"}); err != nil || len(problems) != 0 {
		t.Errorf("SetSettings: %v %v", problems, err)
	}
	if all, _ := s.Settings(ctx); all.Int(SettingKeepRuns) != 5 || all.Bool(SettingRefreshOnStart) {
		t.Errorf("SetSettings not applied: %v", all)
	}
	one, ok, _ := s.GetSource(ctx, src)
	if !ok || one.Name != "renamed" || one.URL != "http://p/2" || one.Enabled || one.Timezone != "America/Chicago" || one.DateOrder != DefaultDateOrder {
		t.Errorf("UpdateSource: %+v", one)
	}
	s.AllocateChannel(ctx, src, "nfl", "US NFL Bills", "NFL Bills", 9300)
	if err := s.DeleteSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetSource(ctx, src); ok {
		t.Error("source should be gone")
	}
	if allocs, _ := s.ChannelAllocs(ctx, src); len(allocs) != 0 {
		t.Error("allocations should cascade on delete")
	}
}
