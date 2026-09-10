package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Setting keys.
const (
	SettingRefreshIntervalMinutes = "refresh_interval_minutes"
	SettingDefaultTimezone        = "default_timezone"
	SettingPublicBaseURL          = "public_base_url"
	SettingConfidenceThreshold    = "confidence_threshold"
	SettingEmitPlaceholderProg    = "emit_placeholder_program"
	SettingM3UTvcGuideTags        = "m3u_tvc_guide_tags"
	SettingChannelStart           = "channel_start"
)

// Kind is a setting's value type. The UI renders controls from it and the store
// validates writes with it, so every writer (env seed, UI form, CLI) shares one rule.
type Kind int

const (
	KindString Kind = iota
	KindBool        // stored as "1" or "0"
	KindInt
	KindFloat
)

// SettingDef describes one setting: its default, type, constraints, and the
// environment variable that may seed it on first boot.
type SettingDef struct {
	Key     string
	Label   string // shown in the UI
	Help    string // one sentence under the control
	Default string
	Kind    Kind
	Min     *float64 // numeric kinds
	Max     *float64
	Choices []string           // string kind: allowed values
	Check   func(string) error // string kind: extra validation
}

func f(v float64) *float64 { return &v }

// SettingDefs is the registry of settings, in display order.
var SettingDefs = []SettingDef{
	{Key: SettingRefreshIntervalMinutes, Label: "Refresh every (minutes)", Help: "How often sources are fetched and the guide rebuilt.",
		Default: "60", Kind: KindInt, Min: f(1)},
	{Key: SettingDefaultTimezone, Label: "Default time zone", Help: "Zone for game times that do not name one. Providers almost always mean Eastern.",
		Default: "America/New_York", Kind: KindString, Choices: Zones},
	{Key: SettingPublicBaseURL, Label: "Public URL", Help: "How Channels DVR reaches this app. Every logo and every piece of airing art is fetched from here, so set it when more than one hostname reaches the app; leave it empty to derive from each request.",
		Default: "", Kind: KindString},
	{Key: SettingConfidenceThreshold, Label: "Confidence threshold", Help: "Parsed games below this confidence (0 to 1) are kept out of the guide and listed as low confidence.",
		Default: "0.5", Kind: KindFloat, Min: f(0), Max: f(1)},
	{Key: SettingEmitPlaceholderProg, Label: "Placeholder program on idle channels", Help: "Give idle channels a 24 hour \"No Event Scheduled\" program instead of an empty guide.",
		Default: "0", Kind: KindBool},
	{Key: SettingM3UTvcGuideTags, Label: "Guide tags in the M3U", Help: "Add Channels DVR tvc-guide attributes to the playlist for setups that load it without the XMLTV.",
		Default: "0", Kind: KindBool},
	{Key: SettingChannelStart, Label: "Channel numbers start at", Help: "The first channel number epg3r uses. Each league gets a thousand numbers from here, in the order the Leagues page lists them, so moving this moves every sports channel together. Pick a range your other providers leave alone.",
		Default: "10000", Kind: KindInt, Min: f(1), Max: f(900000)},
}

var settingDefs = func() map[string]SettingDef {
	m := make(map[string]SettingDef, len(SettingDefs))
	for _, d := range SettingDefs {
		m[d.Key] = d
	}
	return m
}()

// SettingDefault is a setting's shipped default, for a form shown before anything has been
// written.
func SettingDefault(key string) string { return settingDefs[key].Default }

// Normalize validates raw input for this setting and returns its canonical form.
func (d SettingDef) Normalize(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch d.Kind {
	case KindBool:
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return "1", nil
		case "0", "false", "no", "off":
			return "0", nil
		}
		return "", fmt.Errorf("%s must be true or false, got %q", d.Key, raw)
	case KindInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("%s must be a whole number, got %q", d.Key, raw)
		}
		if err := d.checkRange(float64(n)); err != nil {
			return "", err
		}
		return strconv.Itoa(n), nil
	case KindFloat:
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "", fmt.Errorf("%s must be a number, got %q", d.Key, raw)
		}
		if err := d.checkRange(x); err != nil {
			return "", err
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		if len(d.Choices) > 0 {
			for _, c := range d.Choices {
				if v == c {
					return v, nil
				}
			}
			// Naming three modes helps; naming five hundred time zones does not.
			if len(d.Choices) <= 6 {
				return "", fmt.Errorf("%s must be one of %s, got %q", d.Key, strings.Join(d.Choices, ", "), raw)
			}
			return "", fmt.Errorf("%s: %q is not one of the choices", d.Key, raw)
		}
		if d.Check != nil {
			if err := d.Check(v); err != nil {
				return "", fmt.Errorf("%s: %w", d.Key, err)
			}
		}
		return v, nil
	}
}

func (d SettingDef) checkRange(x float64) error {
	if d.Min != nil && x < *d.Min {
		return fmt.Errorf("%s must be at least %v, got %v", d.Key, *d.Min, x)
	}
	if d.Max != nil && x > *d.Max {
		return fmt.Errorf("%s must be at most %v, got %v", d.Key, *d.Max, x)
	}
	return nil
}

func lookupDef(key string) (SettingDef, error) {
	d, ok := settingDefs[key]
	if !ok {
		return SettingDef{}, fmt.Errorf("unknown setting %q", key)
	}
	return d, nil
}

// Setting returns the stored value for key, or its default when unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	d, err := lookupDef(key)
	if err != nil {
		return "", err
	}
	var v string
	err = s.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return d.Default, nil
	}
	return v, err
}

// SetSetting validates and writes one value. Bad input is a ValidationError.
func (s *Store) SetSetting(ctx context.Context, key, raw string) error {
	v, err := s.normalize(key, raw)
	if err != nil {
		return &ValidationError{err.Error()}
	}
	_, err = s.writeSettings(ctx, map[string]string{key: v}, nil)
	return err
}

// Shelf is the span of channel numbers the leagues occupy and how far it is moving. A zero
// Delta is no move.
type Shelf struct{ From, To, Delta int }

// writeSettings stores normalized values in one transaction and drops the zone cache once
// they are committed. Every settings writer ends here.
//
// A shelf move rides along inside the same transaction: where the channel numbers start and
// what the channels are published under are one fact, and a half-applied move would leave the
// guide disagreeing with the page that set it.
func (s *Store) writeSettings(ctx context.Context, values map[string]string, shelf *Shelf) (map[string]int, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.stamp()
	for key, v := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, v, now); err != nil {
			return nil, err
		}
	}
	var moved map[string]int
	if shelf != nil && shelf.Delta != 0 {
		if moved, err = translateBlock(ctx, tx, shelf.From, shelf.To, shelf.Delta); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.loc.Store(nil) // after commit, so a concurrent reader cannot re-cache the old value
	return moved, nil
}

// Location is the configured default zone. The store is the only writer of settings,
// so it caches the parsed zone and drops it whenever settings change; callers may use
// this on hot paths.
func (s *Store) Location(ctx context.Context) *time.Location {
	if loc := s.loc.Load(); loc != nil {
		return loc
	}
	all, err := s.Settings(ctx)
	if err != nil {
		return time.UTC
	}
	loc := all.Location()
	s.loc.Store(loc)
	return loc
}

// SetSettings validates every value and, only if all pass, writes them in one
// transaction. Problems are returned per key; nothing is written when any fail.
func (s *Store) SetSettings(ctx context.Context, values map[string]string) (map[string]string, error) {
	problems, _, err := s.SetSettingsMoving(ctx, values, nil)
	return problems, err
}

// SetSettingsMoving is SetSettings with a shelf move applied in the same transaction, for the
// setting that decides where every channel number starts. It reports the numbers that changed
// so the guide in memory can follow without waiting for a refresh.
func (s *Store) SetSettingsMoving(ctx context.Context, values map[string]string, shelf *Shelf) (map[string]string, map[string]int, error) {
	problems := map[string]string{}
	normalized := map[string]string{}
	for key, raw := range values {
		v, err := s.normalize(key, raw)
		if err != nil {
			problems[key] = err.Error()
			continue
		}
		normalized[key] = v
	}
	if len(problems) > 0 {
		return problems, nil, nil
	}
	moved, err := s.writeSettings(ctx, normalized, shelf)
	return nil, moved, err
}

func (s *Store) normalize(key, raw string) (string, error) {
	d, err := lookupDef(key)
	if err != nil {
		return "", err
	}
	return d.Normalize(raw)
}

// Settings returns every known setting, merging stored values over defaults. Prefer
// this over repeated Setting calls when several values are needed at once.
func (s *Store) Settings(ctx context.Context) (SettingValues, error) {
	out := make(SettingValues, len(SettingDefs))
	for _, d := range SettingDefs {
		out[d.Key] = d.Default
	}
	rows, err := s.r.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if _, known := out[k]; known {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// RefreshInterval is the typed accessor for refresh_interval_minutes.
func (s *Store) RefreshInterval(ctx context.Context) (time.Duration, error) {
	all, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	return all.RefreshInterval(), nil
}

// SettingBool is the typed accessor for boolean settings.
func (s *Store) SettingBool(ctx context.Context, key string) (bool, error) {
	v, err := s.Setting(ctx, key)
	return v == "1", err
}

// SettingValues is a settings snapshot with typed accessors. Values are already
// normalized, so parsing cannot fail.
type SettingValues map[string]string

func (v SettingValues) Bool(key string) bool { return v[key] == "1" }
func (v SettingValues) Int(key string) int   { n, _ := strconv.Atoi(v[key]); return n }
func (v SettingValues) Float(key string) float64 {
	x, _ := strconv.ParseFloat(v[key], 64)
	return x
}
func (v SettingValues) RefreshInterval() time.Duration {
	return time.Duration(v.Int(SettingRefreshIntervalMinutes)) * time.Minute
}

// Location resolves default_timezone.
func (v SettingValues) Location() *time.Location {
	loc, err := time.LoadLocation(v[SettingDefaultTimezone])
	if err != nil {
		return time.UTC
	}
	return loc
}
