package store

import (
	"context"
	"database/sql"
	"time"
)

// Column defaults for sources, owned here so the INSERT and the Go zero value agree.
const (
	DefaultDateOrder = "MDY"
	DefaultVsOrder   = "unknown"
)

// Source is an M3U playlist the app ingests.
type Source struct {
	ID        int64
	Name      string
	URL       string
	XMLTVURL  string // optional provider guide
	Enabled   bool
	Provider  string
	Timezone  string // IANA zone; empty means use the default_timezone setting
	DateOrder string // MDY | DMY; reserved for the UI, the parser currently infers order from the values
	VsOrder   string // unknown | away-home | home-away; reserved, orientation currently comes from "@"
	IDPrefix  string
	CreatedAt time.Time
	UpdatedAt time.Time

	LastFetchedAt    *time.Time
	LastStatus       FetchStatus
	LastError        string
	LastChannelCount *int
}

// NewSource is the input for CreateSource.
type NewSource struct {
	Name      string
	URL       string
	XMLTVURL  string
	Timezone  string
	Provider  string
	DateOrder string
	VsOrder   string
	IDPrefix  string
}

// CreateSource inserts a source and returns its id.
func (s *Store) CreateSource(ctx context.Context, in NewSource) (int64, error) {
	if in.DateOrder == "" {
		in.DateOrder = DefaultDateOrder
	}
	if in.VsOrder == "" {
		in.VsOrder = DefaultVsOrder
	}
	now := s.stamp()
	res, err := s.w.ExecContext(ctx, `INSERT INTO sources
		(name, url, xmltv_url, enabled, provider, timezone, date_order, vs_order, id_prefix, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.URL, in.XMLTVURL, in.Provider, in.Timezone, in.DateOrder, in.VsOrder, in.IDPrefix, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SourceExistsByURL reports whether any source has this URL.
func (s *Store) SourceExistsByURL(ctx context.Context, url string) (bool, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE url = ?`, url).Scan(&n)
	return n > 0, err
}

// ListSources returns all sources ordered by id.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, name, url, xmltv_url, enabled, provider, timezone, date_order, vs_order, id_prefix,
		created_at, updated_at, last_fetched_at, last_status, last_error, last_channel_count
		FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var (
			src                       Source
			createdAt, updatedAt      string
			lastFetched, status, lerr sql.NullString
			lastCount                 sql.NullInt64
		)
		if err := rows.Scan(&src.ID, &src.Name, &src.URL, &src.XMLTVURL, &src.Enabled, &src.Provider, &src.Timezone, &src.DateOrder,
			&src.VsOrder, &src.IDPrefix, &createdAt, &updatedAt, &lastFetched, &status, &lerr, &lastCount); err != nil {
			return nil, err
		}
		src.CreatedAt = parseStamp(createdAt)
		src.UpdatedAt = parseStamp(updatedAt)
		src.LastFetchedAt = parseNullStamp(lastFetched)
		src.LastStatus = FetchStatus(status.String)
		src.LastError = lerr.String
		if lastCount.Valid {
			n := int(lastCount.Int64)
			src.LastChannelCount = &n
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// SourceFetchResult records what the last fetch of a source did.
type SourceFetchResult struct {
	Status       FetchStatus
	Error        string
	ChannelCount int
}

// RecordSourceFetch updates a source's derived fetch columns.
func (s *Store) RecordSourceFetch(ctx context.Context, id int64, r SourceFetchResult) error {
	_, err := s.w.ExecContext(ctx, `UPDATE sources SET last_fetched_at = ?, last_status = ?, last_error = ?,
		last_channel_count = ? WHERE id = ?`, s.stamp(), string(r.Status), r.Error, r.ChannelCount, id)
	return err
}
