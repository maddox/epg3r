package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ChannelAlloc is a sticky identity for a channel without a slot number.
type ChannelAlloc struct {
	ChannelID string
	Number    int
}

// AllocateChannel returns the id and number already assigned to this feed, or assigns
// new ones: the first free number at or above base, and the preferred id with a
// numeric suffix if another feed already holds it ("NFL Bills", "NFL Bills 2").
func (s *Store) AllocateChannel(ctx context.Context, sourceID int64, leagueKey, tvgName, preferredID string, base int) (ChannelAlloc, error) {
	var a ChannelAlloc
	err := s.r.QueryRowContext(ctx, `SELECT channel_id, number FROM channel_alloc WHERE source_id = ? AND league_key = ? AND tvg_name = ?`,
		sourceID, leagueKey, tvgName).Scan(&a.ChannelID, &a.Number)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}

	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()

	// Next free number in this league's team block.
	var maxNum sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM channel_alloc WHERE league_key = ? AND number >= ?`, leagueKey, base).Scan(&maxNum); err != nil {
		return a, err
	}
	a.Number = base
	if maxNum.Valid {
		a.Number = int(maxNum.Int64) + 1
	}

	// Preferred id, suffixed until unique.
	a.ChannelID = preferredID
	for n := 2; ; n++ {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_alloc WHERE channel_id = ?`, a.ChannelID).Scan(&taken); err != nil {
			return a, err
		}
		if taken == 0 {
			break
		}
		a.ChannelID = fmt.Sprintf("%s %d", preferredID, n)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO channel_alloc (source_id, league_key, tvg_name, channel_id, number, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sourceID, leagueKey, tvgName, a.ChannelID, a.Number, s.stamp()); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

// ChannelAllocs returns every allocation for a source keyed by league key and tvg-name,
// so a run can resolve known feeds without a query per entry.
func (s *Store) ChannelAllocs(ctx context.Context, sourceID int64) (map[[2]string]ChannelAlloc, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT league_key, tvg_name, channel_id, number FROM channel_alloc WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]ChannelAlloc{}
	for rows.Next() {
		var league, name string
		var a ChannelAlloc
		if err := rows.Scan(&league, &name, &a.ChannelID, &a.Number); err != nil {
			return nil, err
		}
		out[[2]string{league, name}] = a
	}
	return out, rows.Err()
}

// Family is one provider style within a league.
type Family struct {
	Index      int
	LabelShape string
	SchedShape string
	Example    string
}

// Families lists a league's provider families for a source, in index order.
func (s *Store) Families(ctx context.Context, sourceID int64, leagueKey string) ([]Family, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT family, label_shape, sched_shape, example FROM slot_families
		WHERE source_id = ? AND league_key = ? ORDER BY family`, sourceID, leagueKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Family
	for rows.Next() {
		var f Family
		if err := rows.Scan(&f.Index, &f.LabelShape, &f.SchedShape, &f.Example); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FamilyFor returns the sticky family for a provider style. A style is the label shape
// plus the schedule shape. When a game arrives for a label shape that so far only had
// placeholders (sched_shape ""), that family is adopted rather than a new one created,
// so an off-season lineup keeps its ids when the season starts. ok is false when the
// league already holds maxFamilies styles.
func (s *Store) FamilyFor(ctx context.Context, sourceID int64, leagueKey, labelShape, schedShape, example string, maxFamilies int) (family int, ok bool, err error) {
	q := `SELECT family FROM slot_families WHERE source_id = ? AND league_key = ? AND label_shape = ? AND sched_shape = ?`
	err = s.r.QueryRowContext(ctx, q, sourceID, leagueKey, labelShape, schedShape).Scan(&family)
	if err == nil {
		return family, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}

	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	if schedShape != "" {
		res, err := tx.ExecContext(ctx, `UPDATE slot_families SET sched_shape = ?, example = ? WHERE source_id = ? AND league_key = ?
			AND label_shape = ? AND sched_shape = ''`, schedShape, example, sourceID, leagueKey, labelShape)
		if err != nil {
			return 0, false, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if err := tx.QueryRowContext(ctx, q, sourceID, leagueKey, labelShape, schedShape).Scan(&family); err != nil {
				return 0, false, err
			}
			return family, true, tx.Commit()
		}
	}

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slot_families WHERE source_id = ? AND league_key = ?`, sourceID, leagueKey).Scan(&n); err != nil {
		return 0, false, err
	}
	if n >= maxFamilies {
		return 0, false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO slot_families (source_id, league_key, label_shape, sched_shape, family, example, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sourceID, leagueKey, labelShape, schedShape, n, example, s.stamp()); err != nil {
		return 0, false, err
	}
	return n, true, tx.Commit()
}
