package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// ---------- channel rules ----------

// LeagueOverrides returns every stored override keyed by league key.
func (s *Store) LeagueOverrides(ctx context.Context) (map[string]catalog.Override, error) {
	return leagueOverrides(ctx, s.r)
}

func leagueOverrides(ctx context.Context, q querier) (map[string]catalog.Override, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, patch FROM league_overrides`)
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

// SetLeagueOverride validates and stores an override, or removes it when empty. A change that
// also moves a league's channels goes through RehomeLeague instead, so that the setting and
// the numbers it decides cannot be written apart.
func (s *Store) SetLeagueOverride(ctx context.Context, key string, o catalog.Override) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setLeagueOverride(ctx, tx, key, o, nil, s.stamp()); err != nil {
		return err
	}
	return tx.Commit()
}

func setLeagueOverride(ctx context.Context, tx *sql.Tx, key string, o catalog.Override,
	check func(map[string]catalog.Override) error, now string) error {
	if err := o.Validate(); err != nil {
		return &ValidationError{err.Error()}
	}
	if check != nil {
		all, err := leagueOverrides(ctx, tx)
		if err != nil {
			return err
		}
		if o.IsZero() {
			delete(all, key)
		} else {
			all[key] = o
		}
		if err := check(all); err != nil {
			return &ValidationError{err.Error()}
		}
	}
	if o.IsZero() {
		_, err := tx.ExecContext(ctx, `DELETE FROM league_overrides WHERE key = ?`, key)
		return err
	}
	patch, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO league_overrides (key, patch, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET patch = excluded.patch, updated_at = excluded.updated_at`,
		key, string(patch), now)
	return err
}
