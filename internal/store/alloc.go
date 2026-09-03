package store

import (
	"context"
	"database/sql"
	"errors"
)

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
