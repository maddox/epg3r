package config

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/jonmaddox/epg3r/internal/store"
)

func env(pairs ...string) Lookup {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	cfg, err := FromEnv(env())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/data" || cfg.Listen != ":8080" || cfg.LogFormat != "text" || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.SeedM3UURL != "" || len(cfg.SeedSettings) != 0 {
		t.Errorf("seeds should be empty by default: %+v", cfg)
	}
	if got := cfg.LoopbackAddr(); got != "127.0.0.1:8080" {
		t.Errorf("LoopbackAddr = %q", got)
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := FromEnv(env(
		"EPG3R_DATA_DIR", "/tmp/x",
		"EPG3R_LISTEN", "0.0.0.0:9000",
		"EPG3R_LOG_LEVEL", "DEBUG",
		"EPG3R_LOG_FORMAT", "json",
		"EPG3R_M3U_URL", "http://p.example/list.m3u",
		"EPG3R_REFRESH_INTERVAL", "90",
		"EPG3R_PUBLIC_URL", "https://epg.example.com",
	))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "json" {
		t.Errorf("logging not parsed: %+v", cfg)
	}
	if cfg.SeedM3UURL != "http://p.example/list.m3u" {
		t.Errorf("SeedM3UURL = %q", cfg.SeedM3UURL)
	}
	want := map[string]string{
		store.SettingRefreshIntervalMinutes: "90",
		store.SettingPublicBaseURL:          "https://epg.example.com",
	}
	if len(cfg.SeedSettings) != len(want) {
		t.Fatalf("SeedSettings = %v", cfg.SeedSettings)
	}
	for k, v := range want {
		if cfg.SeedSettings[k] != v {
			t.Errorf("SeedSettings[%s] = %q, want %q", k, cfg.SeedSettings[k], v)
		}
	}
	if got := cfg.LoopbackAddr(); got != "127.0.0.1:9000" {
		t.Errorf("LoopbackAddr = %q", got)
	}
}

func TestLoopbackAddrKeepsExplicitHost(t *testing.T) {
	cfg := Config{Listen: "10.0.0.5:8080"}
	if got := cfg.LoopbackAddr(); got != "10.0.0.5:8080" {
		t.Errorf("LoopbackAddr = %q", got)
	}
	cfg = Config{Listen: "[::]:8080"}
	if got := cfg.LoopbackAddr(); got != "127.0.0.1:8080" {
		t.Errorf("LoopbackAddr = %q", got)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string][]string{
		"EPG3R_LOG_LEVEL":  {"EPG3R_LOG_LEVEL", "loud"},
		"EPG3R_LOG_FORMAT": {"EPG3R_LOG_FORMAT", "xml"},
		"EPG3R_LISTEN":     {"EPG3R_LISTEN", "8080"},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := FromEnv(env(pair...))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("expected error mentioning %s, got %v", name, err)
			}
		})
	}
}
