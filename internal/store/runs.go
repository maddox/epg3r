package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Run is one refresh.
type Run struct {
	ID         int64
	Trigger    Trigger
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     RunStatus
	Error      string
	Counts     RunCounts
}

// RunCounts is how many entries had each outcome.
type RunCounts map[Outcome]int

// Seen is the total number of entries.
func (c RunCounts) Seen() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// CountOutcomes tallies the rows of a run.
func CountOutcomes(rows []RunChannel) RunCounts {
	c := RunCounts{}
	for _, r := range rows {
		c[r.Status]++
	}
	return c
}

// RunChannel is one playlist entry's outcome in a run.
type RunChannel struct {
	SourceID        int64
	Group           string
	RawTitle        string
	NormalizedTitle string
	TvgID, TvgName  string
	TvgLogo         string
	StreamURL       string
	Status          Outcome
	LeagueKey       string
	Kind            string // model.ChannelKind
	ChannelID       string
	ChannelNumber   int
	Matchup         string
	Team1, Team2    string
	StartAt, StopAt *time.Time
	Reason          string
	Confidence      float64
}

// Scored reports whether this row's confidence is worth showing: a channel nothing
// recognised, or one that is not an event channel at all, was never scored.
func (c RunChannel) Scored() bool {
	return c.Confidence > 0 && c.Status != OutcomeNetwork && c.Status != OutcomeUnmatched
}

// StartRun inserts a running run and returns its id.
func (s *Store) StartRun(ctx context.Context, trigger Trigger) (int64, error) {
	res, err := s.w.ExecContext(ctx, `INSERT INTO runs (trigger, started_at, status) VALUES (?, ?, ?)`, string(trigger), s.stamp(), string(RunRunning))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun records the outcome, the per-channel rows, and the snapshot in one
// transaction, then prunes runs beyond keep. Counts are derived from the rows.
func (s *Store) FinishRun(ctx context.Context, id int64, status RunStatus, errText string, channels []RunChannel, snapshot any, keep int) error {
	snap, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	counts, err := json.Marshal(CountOutcomes(channels))
	if err != nil {
		return err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE runs SET finished_at = ?, status = ?, error = ?, counts_json = ?, snapshot_json = ? WHERE id = ?`,
		s.stamp(), string(status), errText, string(counts), snap, id); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO run_channels (run_id, source_id, group_name, raw_title, normalized_title, tvg_id, tvg_name,
		tvg_logo, stream_url, status, league_key, kind, channel_id, channel_number, matchup, team1, team2, start_at, stop_at, reason, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range channels {
		if _, err := stmt.ExecContext(ctx, id, c.SourceID, c.Group, c.RawTitle, c.NormalizedTitle, c.TvgID, c.TvgName, c.TvgLogo, c.StreamURL,
			string(c.Status), nullStr(c.LeagueKey), nullStr(c.Kind), nullStr(c.ChannelID), nullInt(c.ChannelNumber), nullStr(c.Matchup),
			nullStr(c.Team1), nullStr(c.Team2), nullTime(c.StartAt), nullTime(c.StopAt), nullStr(c.Reason), c.Confidence); err != nil {
			return err
		}
	}

	if keep > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id NOT IN (SELECT id FROM runs ORDER BY id DESC LIMIT ?)`, keep); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FailStaleRuns marks runs still "running" as failed. Called at startup: a run that
// was in flight when the previous process died can never finish.
func (s *Store) FailStaleRuns(ctx context.Context) (int64, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, error = 'interrupted by shutdown' WHERE status = ?`,
		string(RunFailed), s.stamp(), string(RunRunning))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LatestSnapshot returns the most recent successful run's snapshot, decoded into dst.
// ok is false when no run has completed yet.
func (s *Store) LatestSnapshot(ctx context.Context, dst any) (runID int64, ok bool, err error) {
	var blob []byte
	err = s.r.QueryRowContext(ctx, `SELECT id, snapshot_json FROM runs WHERE status IN (?, ?) AND snapshot_json IS NOT NULL
		ORDER BY id DESC LIMIT 1`, string(RunOK), string(RunPartial)).Scan(&runID, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return runID, true, json.Unmarshal(blob, dst)
}

// ListRuns returns recent runs, newest first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+runColumns+` FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunChannelFilter narrows RunChannels.
type RunChannelFilter struct {
	Status Outcome // "" for all
	League string  // league key, "" for all
	Query  string  // substring of the raw title, channel id, or matchup
	Limit  int     // 0 for no limit
}

const runColumns = `id, trigger, started_at, finished_at, status, error, counts_json`

func scanRun(row scanner) (Run, error) {
	var (
		r                         Run
		started, trigger, status  string
		finished, errText, counts sql.NullString
	)
	if err := row.Scan(&r.ID, &trigger, &started, &finished, &status, &errText, &counts); err != nil {
		return Run{}, err
	}
	r.Trigger, r.Status = Trigger(trigger), RunStatus(status)
	r.StartedAt = parseStamp(started)
	r.FinishedAt = parseNullStamp(finished)
	r.Error = errText.String
	r.Counts = RunCounts{}
	if counts.Valid {
		_ = json.Unmarshal([]byte(counts.String), &r.Counts)
	}
	return r, nil
}

// GetRun returns one run.
func (s *Store) GetRun(ctx context.Context, id int64) (Run, bool, error) {
	r, err := scanRun(s.r.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	return r, err == nil, err
}

// RunChannels returns the per-channel rows of a run matching the filter.
func (s *Store) RunChannels(ctx context.Context, runID int64, f RunChannelFilter) ([]RunChannel, error) {
	q := `SELECT source_id, group_name, raw_title, normalized_title, tvg_id, tvg_name, tvg_logo, stream_url, status, league_key, kind,
		channel_id, channel_number, matchup, team1, team2, start_at, stop_at, reason, confidence FROM run_channels WHERE run_id = ?`
	args := []any{runID}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.League != "" {
		q += ` AND league_key = ?`
		args = append(args, f.League)
	}
	if f.Query != "" {
		like := "%" + strings.ToLower(f.Query) + "%"
		q += ` AND (lower(raw_title) LIKE ? OR lower(coalesce(channel_id, '')) LIKE ? OR lower(coalesce(matchup, '')) LIKE ?)`
		args = append(args, like, like, like)
	}
	q += ` ORDER BY channel_number IS NULL, channel_number, id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunChannel
	for rows.Next() {
		var (
			c                                                        RunChannel
			sourceID                                                 sql.NullInt64
			status                                                   string
			league, kind, chID, matchup, t1, t2, start, stop, reason sql.NullString
			number                                                   sql.NullInt64
			conf                                                     sql.NullFloat64
		)
		if err := rows.Scan(&sourceID, &c.Group, &c.RawTitle, &c.NormalizedTitle, &c.TvgID, &c.TvgName, &c.TvgLogo, &c.StreamURL, &status,
			&league, &kind, &chID, &number, &matchup, &t1, &t2, &start, &stop, &reason, &conf); err != nil {
			return nil, err
		}
		c.SourceID, c.Status, c.Confidence = sourceID.Int64, Outcome(status), conf.Float64
		c.LeagueKey, c.Kind, c.ChannelID, c.Matchup, c.Team1, c.Team2, c.Reason = league.String, kind.String, chID.String, matchup.String, t1.String, t2.String, reason.String
		c.ChannelNumber = int(number.Int64)
		c.StartAt, c.StopAt = parseNullStamp(start), parseNullStamp(stop)
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatStamp(*t)
}
