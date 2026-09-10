package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jonmaddox/epg3r/internal/config"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/store/storetest"
)

var quiet = slog.New(slog.DiscardHandler)

func TestSeedIsIdempotentPerKey(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)

	cfg := config.Config{
		SeedM3UURL:   "http://p.example/a.m3u",
		SeedSettings: map[string]string{store.SettingRefreshIntervalMinutes: "45"},
	}
	if err := seed(ctx, st, cfg, quiet); err != nil {
		t.Fatal(err)
	}
	srcs, _ := st.ListSources(ctx)
	if len(srcs) != 1 || srcs[0].URL != cfg.SeedM3UURL {
		t.Fatalf("expected seeded source, got %+v", srcs)
	}
	if v, _ := st.Setting(ctx, store.SettingRefreshIntervalMinutes); v != "45" {
		t.Errorf("interval not seeded: %q", v)
	}

	// A seed equal to the default is not written, so defaults keep flowing on upgrade.
	if wrote, _ := st.SetSettingIfUnset(ctx, store.SettingChannelStart, "10000"); wrote {
		t.Error("seeding the default value should be a no-op")
	}

	// The user edits the interval in the UI, then the container restarts with the same env.
	if err := st.SetSetting(ctx, store.SettingRefreshIntervalMinutes, "10"); err != nil {
		t.Fatal(err)
	}
	if err := seed(ctx, st, cfg, quiet); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Setting(ctx, store.SettingRefreshIntervalMinutes); v != "10" {
		t.Errorf("seed overwrote a user edit: %q", v)
	}
	srcs, _ = st.ListSources(ctx)
	if len(srcs) != 1 {
		t.Errorf("source seeded twice: %+v", srcs)
	}

	// A different URL in the env adds a source rather than fighting the existing one.
	cfg.SeedM3UURL = "http://p.example/b.m3u"
	if err := seed(ctx, st, cfg, quiet); err != nil {
		t.Fatal(err)
	}
	if srcs, _ = st.ListSources(ctx); len(srcs) != 2 {
		t.Errorf("expected second source, got %+v", srcs)
	}
}

func TestSeedRejectsInvalidValues(t *testing.T) {
	st := storetest.Open(t)
	cfg := config.Config{SeedSettings: map[string]string{store.SettingRefreshIntervalMinutes: "soon"}}
	if err := seed(context.Background(), st, cfg, quiet); err == nil {
		t.Error("invalid seed should fail boot loudly")
	}
}

func TestSeedNoopWithoutEnv(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	if err := seed(ctx, st, config.Config{}, quiet); err != nil {
		t.Fatal(err)
	}
	if srcs, _ := st.ListSources(ctx); len(srcs) != 0 {
		t.Errorf("expected no sources, got %d", len(srcs))
	}
}
