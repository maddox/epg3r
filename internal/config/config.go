// Package config loads epg3r's process configuration from environment variables.
//
// Only bootstrap concerns live here: where the data directory is, what to listen on,
// and logging — the things that have to be known before there is a database to read.
// Everything a user would tune is a setting, set in the app and owned by it.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
)

// Config is the process-level configuration.
type Config struct {
	DataDir   string     // EPG3R_DATA_DIR: SQLite database and caches live here
	Listen    string     // EPG3R_LISTEN: address for the HTTP server, e.g. ":8080"
	LogLevel  slog.Level // EPG3R_LOG_LEVEL: debug | info | warn | error
	LogFormat string     // EPG3R_LOG_FORMAT: text | json
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
		DataDir:   get("EPG3R_DATA_DIR", "/data"),
		Listen:    get("EPG3R_LISTEN", ":8080"),
		LogFormat: strings.ToLower(get("EPG3R_LOG_FORMAT", "text")),
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
