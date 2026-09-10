// Package storetest opens throwaway databases for tests.
package storetest

import (
	"context"
	"testing"

	"github.com/jonmaddox/epg3r/internal/store"
)

// Open returns a store in a temp directory, closed when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
