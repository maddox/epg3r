// Package config loads epg3r's process configuration from environment variables.
//
// Only bootstrap concerns live here: where the data directory is, what to listen on,
// and logging. Anything a user would tune while the app runs is a setting in the
// database; environment variables can seed those on first boot but never own them.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/jonmaddox/epg3r/internal/store"
)

// Config is the process-level configuration.
type Config struct {
	DataDir   string     // EPG3R_DATA_DIR: SQLite database and caches live here
	Listen    string     // EPG3R_LISTEN: address for the HTTP server, e.g. ":8080"
	LogLevel  slog.Level // EPG3R_LOG_LEVEL: debug | info | warn | error
	LogFormat string     // EPG3R_LOG_FORMAT: text | json

	// SeedM3UURL (EPG3R_M3U_URL) creates a source on first boot if none has this URL,
	// with SeedXMLTVURL (EPG3R_XMLTV_URL) as its optional provider guide.
	SeedM3UURL   string
	SeedXMLTVURL string
	// SeedSettings holds raw values for settings seeded from the environment, keyed by
	// setting key. They apply only to settings that have never been written, so a bare
	// `docker run -e ...` works without touching the UI and the UI always wins afterwards.
	SeedSettings map[string]string
}

// Lookup mirrors os.LookupEnv so tests can supply their own environment.
type Lookup func(key string) (string, bool)

// FromEnv builds a Config from the environment, applying defaults and validating.
func FromEnv(lookup Lookup) (Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}

	cfg := Config{
		DataDir:      get("EPG3R_DATA_DIR", "/data"),
		Listen:       get("EPG3R_LISTEN", ":8080"),
		LogFormat:    strings.ToLower(get("EPG3R_LOG_FORMAT", "text")),
		SeedM3UURL:   get("EPG3R_M3U_URL", ""),
		SeedXMLTVURL: get("EPG3R_XMLTV_URL", ""),
		SeedSettings: map[string]string{},
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(get("EPG3R_LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("EPG3R_LOG_LEVEL: %w", err)
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("EPG3R_LOG_FORMAT must be text or json, got %q", cfg.LogFormat)
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return Config{}, fmt.Errorf("EPG3R_LISTEN must be host:port, got %q: %w", cfg.Listen, err)
	}

	// Settings declare their own seed variable; values are validated when applied.
	for _, d := range store.SettingDefs {
		if d.Env == "" {
			continue
		}
		if v := get(d.Env, ""); v != "" {
			cfg.SeedSettings[d.Key] = v
		}
	}
	return cfg, nil
}

// LoopbackAddr is the address a local client uses to reach the server: the listen
// address with an unspecified host replaced by loopback.
func (c Config) LoopbackAddr() string {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
