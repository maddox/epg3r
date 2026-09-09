package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

// Collection is a set of channels the user has picked out, exported at its own URLs.
type Collection struct {
	ID       int64
	Name     string
	Slug     string
	Updated  string // moves on every change; what a cache of its output keys on
	Channels int    // how many channels are in it
}

const collectionColumns = `id, name, slug, updated_at`

var slugRunes = regexp.MustCompile(`[^a-z0-9]+`)

// Slug is how a collection's name is addressed in a URL.
func Slug(name string) string {
	return strings.Trim(slugRunes.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Collections lists every collection with its size, by name.
func (s *Store) Collections(ctx context.Context) ([]Collection, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT c.id, c.name, c.slug, c.updated_at, COUNT(cc.channel_key)
		FROM collections c LEFT JOIN collection_channels cc ON cc.collection_id = c.id
		GROUP BY c.id ORDER BY c.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Updated, &c.Channels); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CollectionBySlug finds the collection a URL is asking for.
func (s *Store) CollectionBySlug(ctx context.Context, slug string) (Collection, bool, error) {
	return s.collection(ctx, "slug", slug)
}

// CollectionByID finds a collection by its id.
func (s *Store) CollectionByID(ctx context.Context, id int64) (Collection, bool, error) {
	return s.collection(ctx, "id", id)
}

// collection reads one collection. The column comes from this file, never from a caller.
func (s *Store) collection(ctx context.Context, column string, v any) (Collection, bool, error) {
	var c Collection
	err := s.r.QueryRowContext(ctx, `SELECT `+collectionColumns+` FROM collections WHERE `+column+` = ?`, v).
		Scan(&c.ID, &c.Name, &c.Slug, &c.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// CreateCollection makes a collection and returns it. Two collections cannot share a
// slug, since the slug is what their URLs are.
func (s *Store) CreateCollection(ctx context.Context, name string) (Collection, error) {
	c, err := namedCollection(name)
	if err != nil {
		return c, err
	}
	now := s.preciseStamp()
	res, err := s.w.ExecContext(ctx, `INSERT INTO collections (name, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, c.Name, c.Slug, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return c, &ValidationError{Msg: "there is already a collection called " + c.Name}
		}
		return c, err
	}
	c.ID, err = res.LastInsertId()
	return c, err
}

// namedCollection reads a name the user typed, and the slug its URLs will use.
func namedCollection(name string) (Collection, error) {
	c := Collection{Name: strings.TrimSpace(name), Slug: Slug(name)}
	if c.Name == "" || c.Slug == "" {
		return c, &ValidationError{Msg: "a collection needs a name with letters or numbers in it"}
	}
	return c, nil
}

// RenameCollection changes a collection's name, and with it the URLs it serves at.
func (s *Store) RenameCollection(ctx context.Context, id int64, name string) error {
	c, err := namedCollection(name)
	if err != nil {
		return err
	}
	res, err := s.w.ExecContext(ctx, `UPDATE collections SET name = ?, slug = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Slug, s.preciseStamp(), id)
	if err != nil {
		if isUniqueViolation(err) {
			return &ValidationError{Msg: "there is already a collection called " + c.Name}
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCollection removes a collection. Its channels are untouched: a collection is a
// view of them, not where they live.
func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddToCollection puts channels in a collection, ignoring the ones already there, and
// reports how many were new.
func (s *Store) AddToCollection(ctx context.Context, id int64, keys []string) (added int, err error) {
	if len(keys) == 0 {
		return 0, nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := collectionExists(ctx, tx, id); err != nil {
		return 0, err
	}
	now := s.preciseStamp()
	ins, err := tx.PrepareContext(ctx, `INSERT INTO collection_channels (collection_id, channel_key, added_at)
		VALUES (?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	for _, k := range keys {
		res, err := ins.ExecContext(ctx, id, k, now)
		if err != nil {
			return 0, err
		}
		if rows, _ := res.RowsAffected(); rows > 0 {
			added++
		}
	}
	if err := touchCollection(ctx, tx, id, now); err != nil {
		return 0, err
	}
	return added, tx.Commit()
}

// RemoveFromCollection takes channels out of a collection, and reports how many were in
// it to begin with.
func (s *Store) RemoveFromCollection(ctx context.Context, id int64, keys []string) (removed int, err error) {
	if len(keys) == 0 {
		return 0, nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := collectionExists(ctx, tx, id); err != nil {
		return 0, err
	}
	args := []any{id}
	for _, k := range keys {
		args = append(args, k)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM collection_channels WHERE collection_id = ?
		AND channel_key IN (?`+strings.Repeat(",?", len(keys)-1)+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := touchCollection(ctx, tx, id, s.preciseStamp()); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

func collectionExists(ctx context.Context, tx *sql.Tx, id int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// touchCollection records that a collection changed, which is what anything caching its
// output keys on. It rides the same transaction as the change itself.
func touchCollection(ctx context.Context, tx *sql.Tx, id int64, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at = ? WHERE id = ?`, now, id)
	return err
}

// CollectionMembers returns the channel keys in a collection, empty rather than nil
// when it holds none.
func (s *Store) CollectionMembers(ctx context.Context, id int64) (map[string]bool, error) {
	return scanSet[string](ctx, s.r, `SELECT channel_key FROM collection_channels WHERE collection_id = ?`, id)
}
