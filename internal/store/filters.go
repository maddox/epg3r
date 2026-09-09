package store

import (
	"context"
	"encoding/json"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// ---------- channel rules ----------

// LeagueOverrides returns every stored override keyed by league key.
func (s *Store) LeagueOverrides(ctx context.Context) (map[string]catalog.Override, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key, patch FROM league_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]catalog.Override{}
	for rows.Next() {
		var key, patch string
		if err := rows.Scan(&key, &patch); err != nil {
			return nil, err
		}
		var o catalog.Override
		if err := json.Unmarshal([]byte(patch), &o); err != nil {
			return nil, err
		}
		out[key] = o
	}
	return out, rows.Err()
}

// SetLeagueOverride validates and stores an override, or removes it when empty.
func (s *Store) SetLeagueOverride(ctx context.Context, key string, o catalog.Override) error {
	if err := o.Validate(); err != nil {
		return &ValidationError{err.Error()}
	}
	if o.IsZero() {
		_, err := s.w.ExecContext(ctx, `DELETE FROM league_overrides WHERE key = ?`, key)
		return err
	}
	patch, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO league_overrides (key, patch, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET patch = excluded.patch, updated_at = excluded.updated_at`, key, string(patch), s.stamp())
	return err
}
