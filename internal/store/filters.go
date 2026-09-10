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

// Rehome is a league's numbering moving: the start it now has, and the block its channels
// are leaving.
type Rehome struct {
	Key      string
	Override catalog.Override
	From, To int // the block being left; From >= To skips the move
	Delta    int // how far every derived number in it moves

	// Keys is every channel the league has, as the guide currently knows them. Hand-set
	// numbers among these are discarded wherever they sit: a channel moved out of its
	// league's block by hand is exactly the one a translation cannot find, and leaving it
	// there would strand it outside a league that is now numbered.
	Keys       []string
	DropByUser bool
}

// RehomeLeague stores a league's override and moves its channels to match, in one
// transaction. Where a league starts and what its channels are published under are one fact,
// and a half-applied move would leave the guide disagreeing with the page that set it.
//
// Numbers the user chose are cleared rather than moved: a league handed to epg3r holds no
// hand-set numbers at all, and the next run derives them from the new start. Their ids are
// kept, because an id is what a consumer's recordings hang on.
func (s *Store) RehomeLeague(ctx context.Context, rh Rehome,
	check func(map[string]catalog.Override) error) (moved map[string]int, cleared []string, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	if err := setLeagueOverride(ctx, tx, rh.Key, rh.Override, check, s.stamp()); err != nil {
		return nil, nil, err
	}
	if rh.DropByUser {
		if cleared, err = clearHandSet(ctx, tx, rh.Keys); err != nil {
			return nil, nil, err
		}
	}
	if moved, err = translateBlock(ctx, tx, rh.From, rh.To, rh.Delta); err != nil {
		return nil, nil, err
	}
	return moved, cleared, tx.Commit()
}
