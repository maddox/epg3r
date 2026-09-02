package store

import (
	"context"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir()+"/nested")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v1 < 1 {
		t.Fatalf("expected schema version >= 1, got %d", v1)
	}
	s.Close()

	// Reopening must not re-run migrations or fail on existing tables.
	s2, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	v2, err := s2.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v2 != v1 {
		t.Errorf("schema version changed on reopen: %d -> %d", v1, v2)
	}

	var n int
	if err := s2.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != v1 {
		t.Errorf("expected %d migration rows, got %d", v1, n)
	}
}

func TestMigrationsAreWellFormed(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migrations must be contiguous from 1; index %d has version %d (%s)", i, m.version, m.name)
		}
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	v, err := s.Setting(ctx, SettingRefreshIntervalMinutes)
	if err != nil || v != "60" {
		t.Fatalf("default: %q %v", v, err)
	}

	if err := s.SetSetting(ctx, SettingRefreshIntervalMinutes, "15"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, SettingRefreshIntervalMinutes, " 30 "); err != nil {
		t.Fatal("upsert:", err)
	}
	d, err := s.RefreshInterval(ctx)
	if err != nil || d != 30*time.Minute {
		t.Errorf("RefreshInterval = %s %v", d, err)
	}

	if _, err := s.Setting(ctx, "nope"); err == nil {
		t.Error("unknown setting should error")
	}
	if err := s.SetSetting(ctx, "nope", "x"); err == nil {
		t.Error("unknown setting should not be writable")
	}

	all, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(SettingDefs) {
		t.Errorf("Settings should return every key: %d vs %d", len(all), len(SettingDefs))
	}
	if all[SettingRefreshIntervalMinutes] != "30" || all[SettingDefaultTimezone] != "America/New_York" {
		t.Errorf("merge wrong: %v", all)
	}
}

func TestSetSettingIfUnset(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	wrote, err := s.SetSettingIfUnset(ctx, SettingRefreshIntervalMinutes, "45")
	if err != nil || !wrote {
		t.Fatalf("first write: %v %v", wrote, err)
	}
	wrote, err = s.SetSettingIfUnset(ctx, SettingRefreshIntervalMinutes, "5")
	if err != nil || wrote {
		t.Fatalf("second write should be a no-op: %v %v", wrote, err)
	}
	if v, _ := s.Setting(ctx, SettingRefreshIntervalMinutes); v != "45" {
		t.Errorf("value overwritten: %q", v)
	}
	if _, err := s.SetSettingIfUnset(ctx, SettingRefreshIntervalMinutes, "0"); err == nil {
		t.Error("invalid value should be rejected even when unset")
	}
	if wrote, err := s.SetSettingIfUnset(ctx, SettingKeepRuns, "20"); err != nil || wrote {
		t.Errorf("a seed equal to the default should not be written: %v %v", wrote, err)
	}
	all, _ := s.Settings(ctx)
	if all.RefreshInterval() != 45*time.Minute || all.Int(SettingKeepRuns) != 20 || !all.Bool(SettingRefreshOnStart) || all.Location().String() != "America/New_York" {
		t.Errorf("typed accessors wrong: %v", all)
	}
}

func TestSettingValidation(t *testing.T) {
	ok := map[string][2]string{
		SettingRefreshOnStart:      {"YES", "1"},
		SettingExportIdleChannels:  {"off", "0"},
		SettingConfidenceThreshold: {"0.75", "0.75"},
		SettingKeepRuns:            {"007", "7"},
		SettingChannelIDStyle:      {"slug", "slug"},
		SettingDefaultTimezone:     {"Europe/London", "Europe/London"},
		SettingPublicBaseURL:       {"  https://x.example ", "https://x.example"},
	}
	for key, io := range ok {
		got, err := settingDefs[key].Normalize(io[0])
		if err != nil || got != io[1] {
			t.Errorf("%s: Normalize(%q) = %q, %v; want %q", key, io[0], got, err, io[1])
		}
	}

	bad := map[string]string{
		SettingRefreshIntervalMinutes: "0",
		SettingRefreshOnStart:         "maybe",
		SettingConfidenceThreshold:    "1.5",
		SettingKeepRuns:               "ten",
		SettingChannelIDStyle:         "fancy",
		SettingDefaultTimezone:        "Mars/Olympus",
	}
	for key, in := range bad {
		if _, err := settingDefs[key].Normalize(in); err == nil {
			t.Errorf("%s: Normalize(%q) should fail", key, in)
		}
	}

	if len(settingDefs) != len(SettingDefs) {
		t.Errorf("duplicate setting keys in SettingDefs")
	}
}

func TestSources(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	fixed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })

	exists, err := s.SourceExistsByURL(ctx, "http://p.example/list.m3u")
	if err != nil || exists {
		t.Fatalf("exists before insert: %v %v", exists, err)
	}

	id, err := s.CreateSource(ctx, NewSource{Name: "Provider", URL: "http://p.example/list.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Errorf("first id should be 1, got %d", id)
	}
	if exists, _ := s.SourceExistsByURL(ctx, "http://p.example/list.m3u"); !exists {
		t.Error("SourceExistsByURL should find the new source")
	}

	list, err := s.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 source, got %d", len(list))
	}
	got := list[0]
	if !got.Enabled || got.DateOrder != DefaultDateOrder || got.VsOrder != DefaultVsOrder || got.URL != "http://p.example/list.m3u" {
		t.Errorf("unexpected source: %+v", got)
	}
	if !got.CreatedAt.Equal(fixed) {
		t.Errorf("created_at should use the store clock, got %s", got.CreatedAt)
	}
	if got.LastFetchedAt != nil || got.LastChannelCount != nil {
		t.Errorf("derived fields should be nil before any run: %+v", got)
	}
}
