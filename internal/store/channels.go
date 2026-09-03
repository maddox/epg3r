package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Channel is what identity hangs on: one line of one source's playlist, found by its
// URL and nothing else. Everything a run works out about it — league, type, teams,
// airings — is an attribute of this row, not part of what makes it this channel.
type Channel struct {
	Key       string
	SourceID  int64
	URL       string
	ChannelID string // the id given to consumers, assigned with the number
	Number    int    // 0 until a league resolves and a number is assigned
	ByUser    bool   // the number was set by hand, so nothing assigns over it
}

// ChannelKey identifies a playlist line. The URL is taken whole: its shape is the
// provider's business, and picking it apart would mean assuming things about it.
func ChannelKey(sourceID int64, url string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(sourceID, 10) + "\x00" + url))
	return hex.EncodeToString(sum[:])[:20]
}

// Channels returns a source's channels by key.
func (s *Store) Channels(ctx context.Context, sourceID int64) (map[string]Channel, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key, source_id, url, COALESCE(channel_id, ''), COALESCE(number, 0), by_user
		FROM channels WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Key, &c.SourceID, &c.URL, &c.ChannelID, &c.Number, &c.ByUser); err != nil {
			return nil, err
		}
		out[c.Key] = c
	}
	return out, rows.Err()
}

// SeeChannels records the lines a source is currently serving and returns every channel
// it has, including the ones this playlist no longer carries: a channel that has gone
// keeps its row, and with it its number, so it comes back on the same number when the
// provider brings it back. A playlist of channels already known writes nothing.
func (s *Store) SeeChannels(ctx context.Context, sourceID int64, urls []string) (map[string]Channel, error) {
	all, err := s.Channels(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	type line struct{ key, url string }
	var fresh []line
	for _, u := range urls {
		key := ChannelKey(sourceID, u)
		if _, known := all[key]; !known {
			fresh = append(fresh, line{key, u})
			all[key] = Channel{Key: key, SourceID: sourceID, URL: u}
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.stamp()
	// Mark everything this playlist still carries, so a channel that has gone can be
	// told apart from one that is simply quiet.
	for chunk := range slices.Chunk(urls, 400) {
		q := `UPDATE channels SET last_seen = ? WHERE source_id = ? AND key IN (?` + strings.Repeat(",?", len(chunk)-1) + `)`
		args := []any{now, sourceID}
		for _, u := range chunk {
			args = append(args, ChannelKey(sourceID, u))
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return nil, err
		}
	}
	if len(fresh) == 0 {
		return all, tx.Commit()
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO channels (key, source_id, url, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING`)
	if err != nil {
		return nil, err
	}
	defer ins.Close()
	for _, l := range fresh {
		if _, err := ins.ExecContext(ctx, l.key, sourceID, l.url, now, now); err != nil {
			return nil, err
		}
	}
	return all, tx.Commit()
}

// Assignment asks for an id and number for a channel that has neither. Preferred is
// where it would like to sit, so a lineup reads the way the provider labels it; Base
// and Limit bound the fallback search when that number is taken.
type Assignment struct {
	Key             string
	PreferredID     string
	PreferredNumber int
	Base, Limit     int
}

// AssignNumbers gives an id and a number to channels that have none, and returns the
// identity each one was given — Key, ChannelID and Number only, not the whole row. A
// number is never taken from another channel, and a channel that already has an
// identity is left alone and left out of the result.
func (s *Store) AssignNumbers(ctx context.Context, as []Assignment) (map[string]Channel, error) {
	out := map[string]Channel{}
	if len(as) == 0 {
		return out, nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	taken, err := scanSet[int](ctx, tx, `SELECT number FROM channels WHERE number IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	usedIDs, err := scanSet[string](ctx, tx, `SELECT channel_id FROM channels WHERE channel_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	upd, err := tx.PrepareContext(ctx, `UPDATE channels SET channel_id = ?, number = ? WHERE key = ? AND channel_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer upd.Close()

	next := map[int]int{} // per block, where the search for a free number got to
	for _, a := range as {
		n := a.PreferredNumber
		if n < a.Base || n >= a.Limit || taken[n] {
			n = 0
			i := max(next[a.Base], a.Base)
			for ; i < a.Limit; i++ {
				if !taken[i] {
					n = i
					break
				}
			}
			next[a.Base] = i
		}
		if n == 0 {
			continue // this league's block is full; the channel waits for a free number
		}
		id := a.PreferredID
		for i := 2; usedIDs[id]; i++ {
			id = fmt.Sprintf("%s %d", a.PreferredID, i)
		}
		res, err := upd.ExecContext(ctx, id, n, a.Key)
		if err != nil {
			return nil, err
		}
		// Report what the row holds, not what was asked for. If another writer gave this
		// channel an identity first, that identity is the answer, so a key missing from
		// the result means one thing only: there was no free number for it.
		if rows, _ := res.RowsAffected(); rows == 0 {
			var c Channel
			err := tx.QueryRowContext(ctx, `SELECT COALESCE(channel_id, ''), COALESCE(number, 0) FROM channels WHERE key = ?`, a.Key).Scan(&c.ChannelID, &c.Number)
			if err == nil && c.ChannelID != "" {
				c.Key = a.Key
				out[a.Key] = c
			}
			continue
		}
		taken[n], usedIDs[id] = true, true
		out[a.Key] = Channel{Key: a.Key, ChannelID: id, Number: n}
	}
	return out, tx.Commit()
}

// ForgetChannels drops channels no playlist has carried since before, freeing their
// numbers. A provider that changes its stream URLs produces entirely new channels, and
// nothing can map the old ones onto them, so numbers are reclaimed rather than held
// forever: a league only has so many. It reports how many were forgotten.
func (s *Store) ForgetChannels(ctx context.Context, before time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM channels WHERE last_seen < ?`, formatStamp(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SetChannelNumbers moves channels the user has asked to move, in one go. Every number
// asked for is vacated first, so a block can be shifted onto itself — moving 200..231 to
// 201..232 is one channel taking the number of the one before it, which would collide
// applied one at a time. A number held by a channel outside the move is a conflict, and
// nothing is written.
//
// Only a channel with an identity can be moved: one with no number has never been in the
// guide, and giving it one by hand would leave it numbered but unnamed. From here on
// nothing assigns over these channels.
func (s *Store) SetChannelNumbers(ctx context.Context, numbers map[string]int) error {
	if len(numbers) == 0 {
		return nil
	}
	for _, n := range numbers {
		if n <= 0 {
			return &ValidationError{Msg: "a channel number must be positive"}
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keys := slices.Sorted(maps.Keys(numbers)) // a stable order, so an error names the same channel every time
	free, err := tx.PrepareContext(ctx, `UPDATE channels SET number = NULL WHERE key = ? AND channel_id IS NOT NULL`)
	if err != nil {
		return err
	}
	defer free.Close()
	for _, k := range keys {
		res, err := free.ExecContext(ctx, k)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	set, err := tx.PrepareContext(ctx, `UPDATE channels SET number = ?, by_user = 1 WHERE key = ?`)
	if err != nil {
		return err
	}
	defer set.Close()
	for _, k := range keys {
		if _, err := set.ExecContext(ctx, numbers[k], k); err != nil {
			if isUniqueViolation(err) {
				return &ValidationError{Msg: fmt.Sprintf("channel number %d is already taken by another channel", numbers[k])}
			}
			return err
		}
	}
	return tx.Commit()
}

// isUniqueViolation reports whether an error is SQLite refusing a duplicate. The driver
// gives no typed error for it, so the message is all there is to go on.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func scanSet[T comparable](ctx context.Context, tx *sql.Tx, q string) (map[T]bool, error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[T]bool{}
	for rows.Next() {
		var v T
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}
