// Package store owns the SQLite database: opening it, migrating it, and the queries
// each part of the app needs. It is the only package that speaks SQL.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DBFileName is the database file inside the data directory.
const DBFileName = "epg3r.db"

// Store wraps the database. SQLite in WAL mode allows one writer alongside any
// number of readers, so writes go through a single-connection pool (never a busy
// error between our own writers) while reads use a separate pool and never queue
// behind a long write transaction.
type Store struct {
	w   *sql.DB
	r   *sql.DB
	now func() time.Time
	loc atomic.Pointer[time.Location] // parsed default_timezone, dropped on any settings write
}

// Open opens (creating if needed) the database in dataDir, applies pragmas, and runs
// pending migrations. The directory is created if it does not exist.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	dsn := "file:" + filepath.Join(dataDir, DBFileName) + "?" + q.Encode()

	w, err := openPool(ctx, dsn, 1)
	if err != nil {
		return nil, err
	}
	r, err := openPool(ctx, dsn, 4)
	if err != nil {
		w.Close()
		return nil, err
	}

	s := &Store{w: w, r: r, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func openPool(ctx context.Context, dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(maxConns)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	rerr := s.r.Close()
	if werr := s.w.Close(); werr != nil {
		return werr
	}
	return rerr
}

// SetClock overrides the timestamp source (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Timestamps are stored as RFC 3339 text in UTC.
func (s *Store) stamp() string { return formatStamp(s.now()) }

func formatStamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// preciseStamp is for a value something compares to decide whether its copy is stale.
// Seconds are too coarse for that: two changes in the same second would read as one.
func (s *Store) preciseStamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func parseStamp(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func parseNullStamp(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t := parseStamp(v.String)
	if t.IsZero() {
		return nil
	}
	return &t
}
