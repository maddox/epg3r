package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// NewSource is the user-owned part of a source, the input for Create and Update.
type NewSource struct {
	Name      string
	URL       string
	XMLTVURL  string
	Disabled  bool // zero value is enabled, so a bare literal creates an active source
	Timezone  string
	Provider  string
	DateOrder string
	VsOrder   string
	IDPrefix  string
}

// Input returns a source's user-owned fields, for editing.
func (src Source) Input() NewSource {
	return NewSource{Name: src.Name, URL: src.URL, XMLTVURL: src.XMLTVURL, Disabled: !src.Enabled, Timezone: src.Timezone,
		Provider: src.Provider, DateOrder: src.DateOrder, VsOrder: src.VsOrder, IDPrefix: src.IDPrefix}
}

// Validate checks the input and fills defaults. It is the one rule every writer shares.
func (in *NewSource) Validate() error {
	in.Name, in.URL, in.XMLTVURL = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.TrimSpace(in.XMLTVURL)
	in.Timezone, in.IDPrefix = strings.TrimSpace(in.Timezone), strings.TrimSpace(in.IDPrefix)
	if in.Name == "" {
		in.Name = "Source"
	}
	if in.DateOrder == "" {
		in.DateOrder = DefaultDateOrder
	}
	if in.VsOrder == "" {
		in.VsOrder = DefaultVsOrder
	}
	switch {
	case in.URL == "":
		return &ValidationError{"a playlist URL is required"}
	case !isHTTP(in.URL):
		return &ValidationError{"the playlist URL must start with http:// or https://"}
	case in.XMLTVURL != "" && !isHTTP(in.XMLTVURL):
		return &ValidationError{"the guide URL must start with http:// or https://"}
	}
	if in.Timezone != "" {
		if err := checkTimezone(in.Timezone); err != nil {
			return &ValidationError{fmt.Sprintf("unknown time zone %q", in.Timezone)}
		}
	}
	return nil
}

func isHTTP(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// HasSources reports whether any source exists. The first-run gate asks this on every
// request, so it builds nothing and stops at the first row.
func (s *Store) HasSources(ctx context.Context) (bool, error) {
	var found bool
	err := s.r.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sources)`).Scan(&found)
	return found, err
}

// CreateSource validates and inserts a source, returning its id.
func (s *Store) CreateSource(ctx context.Context, in NewSource) (int64, error) {
	if err := in.Validate(); err != nil {
		return 0, err
	}
	now := s.stamp()
	res, err := s.w.ExecContext(ctx, `INSERT INTO sources
		(name, url, xmltv_url, enabled, provider, timezone, date_order, vs_order, id_prefix, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.URL, in.XMLTVURL, !in.Disabled, in.Provider, in.Timezone, in.DateOrder, in.VsOrder, in.IDPrefix, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const sourceColumns = `id, name, url, xmltv_url, enabled, provider, timezone, date_order, vs_order, id_prefix,
	created_at, updated_at, last_fetched_at, last_status, last_error, last_channel_count`

type scanner interface{ Scan(dest ...any) error }

func scanSource(row scanner) (Source, error) {
	var (
		src                       Source
		createdAt, updatedAt      string
		lastFetched, status, lerr sql.NullString
		lastCount                 sql.NullInt64
	)
	if err := row.Scan(&src.ID, &src.Name, &src.URL, &src.XMLTVURL, &src.Enabled, &src.Provider, &src.Timezone, &src.DateOrder,
		&src.VsOrder, &src.IDPrefix, &createdAt, &updatedAt, &lastFetched, &status, &lerr, &lastCount); err != nil {
		return Source{}, err
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
	return src, nil
}

// ListSources returns all sources ordered by id.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+sourceColumns+` FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// GetSource returns one source.
func (s *Store) GetSource(ctx context.Context, id int64) (Source, bool, error) {
	src, err := scanSource(s.r.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, false, nil
	}
	return src, err == nil, err
}

// UpdateSource validates and rewrites a source's user-owned fields.
func (s *Store) UpdateSource(ctx context.Context, id int64, in NewSource) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.w.ExecContext(ctx, `UPDATE sources SET name = ?, url = ?, xmltv_url = ?, enabled = ?, provider = ?, timezone = ?,
		date_order = ?, vs_order = ?, id_prefix = ?, updated_at = ? WHERE id = ?`,
		in.Name, in.URL, in.XMLTVURL, !in.Disabled, in.Provider, in.Timezone, in.DateOrder, in.VsOrder, in.IDPrefix, s.stamp(), id)
	return err
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

// DeleteSource removes a source and, through foreign keys, its channels and families.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	return err
}
