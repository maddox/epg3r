package store

import (
	"context"
	"errors"
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

// A setting that has never been written reads as its shipped default, so nothing has to be
// seeded for the app to start with sensible values.
func TestTypedSettingAccessors(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	if err := s.SetSetting(ctx, SettingRefreshIntervalMinutes, "45"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, SettingRefreshIntervalMinutes, "0"); err == nil {
		t.Error("an invalid value should be rejected")
	}
	if v, _ := s.Setting(ctx, SettingRefreshIntervalMinutes); v != "45" {
		t.Errorf("a refused write changed the value: %q", v)
	}
	all, _ := s.Settings(ctx)
	if all.RefreshInterval() != 45*time.Minute || all.Int(SettingChannelStart) != 10000 || all.Bool(SettingEmitPlaceholderProg) || all.Location().String() != "America/New_York" {
		t.Errorf("typed accessors wrong: %v", all)
	}
}

// A zone is the one setting nobody can check for themselves, so the list the picker offers
// and the list the store accepts are the same one, everything in it loads, and none of
// tzdata's alternate spellings are in it — a picker showing Asmara and Asmera, or Kolkata
// and Calcutta, looks broken whichever one you pick.
func TestZonesAreCanonicalAndLoadable(t *testing.T) {
	if len(Zones) < 300 {
		t.Fatalf("only %d zones; the generated list looks wrong", len(Zones))
	}
	if Zones[0] != "UTC" {
		t.Errorf("UTC should lead the list, got %q", Zones[0])
	}
	seen := map[string]bool{}
	for _, z := range Zones {
		if seen[z] {
			t.Errorf("%s appears twice", z)
		}
		seen[z] = true
		if _, err := time.LoadLocation(z); err != nil {
			t.Errorf("%s does not load: %v", z, err)
		}
	}
	for _, alias := range []string{
		"US/Eastern", "Canada/Pacific", "Etc/GMT+5", // legacy groupings
		"Africa/Asmera", "Asia/Calcutta", "America/Buenos_Aires", "America/Shiprock", // older spellings
	} {
		if seen[alias] {
			t.Errorf("%s is an alias and should not be offered", alias)
		}
	}
	for _, want := range []string{
		"America/New_York", "America/Los_Angeles", "America/Chicago", "America/Denver",
		"America/Anchorage", "Pacific/Honolulu", "America/Toronto",
		"Europe/London", "Australia/Sydney", "Asia/Kolkata",
		// Merged into a neighbour upstream, so zone1970.tab alone would drop it; someone
		// in Oslo should not have to know their zone is called Berlin now.
		"Europe/Oslo",
	} {
		if !seen[want] {
			t.Errorf("%s should be offered", want)
		}
	}
}

func TestSettingValidation(t *testing.T) {
	ok := map[string][2]string{
		SettingConfidenceThreshold: {"0.75", "0.75"},
		SettingChannelStart:        {"010000", "10000"},
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
		SettingEmitPlaceholderProg:    "maybe",
		SettingConfidenceThreshold:    "1.5",
		SettingChannelStart:           "ten",
		SettingDefaultTimezone:        "Mars/Olympus",
	}
	// Booleans arrive from a checkbox, an env var or a hand-written config, so every
	// spelling anyone might send has to land on the same two values.
	for in, want := range map[string]string{"YES": "1", "on": "1", "true": "1", "1": "1", "off": "0", "no": "0", "false": "0", "0": "0"} {
		if got, err := settingDefs[SettingEmitPlaceholderProg].Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
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

	id, err := s.CreateSource(ctx, NewSource{Name: "Provider", URL: "http://p.example/list.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Errorf("first id should be 1, got %d", id)
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

func TestLocationFollowsSettingWrites(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if s.Location(ctx).String() != "America/New_York" {
		t.Fatalf("default zone: %s", s.Location(ctx))
	}
	if err := s.SetSetting(ctx, SettingDefaultTimezone, "Europe/London"); err != nil {
		t.Fatal(err)
	}
	if s.Location(ctx).String() != "Europe/London" {
		t.Errorf("zone should follow a write immediately: %s", s.Location(ctx))
	}
	if _, err := s.SetSettings(ctx, map[string]string{SettingDefaultTimezone: "America/Chicago"}); err != nil {
		t.Fatal(err)
	}
	if s.Location(ctx).String() != "America/Chicago" {
		t.Errorf("zone should follow a batch write: %s", s.Location(ctx))
	}
	var verr *ValidationError
	if err := s.SetSetting(ctx, SettingDefaultTimezone, "Mars/Base"); !errors.As(err, &verr) {
		t.Errorf("SetSetting must surface bad input as a ValidationError, got %v", err)
	}
	if s.Location(ctx).String() != "America/Chicago" {
		t.Error("a rejected write must not change the cached zone")
	}
}
