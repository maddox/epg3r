package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCollections(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	src, _ := s.CreateSource(ctx, NewSource{Name: "p", URL: "http://p/1"})
	urls := []string{"http://x/1", "http://x/2", "http://x/3"}
	if _, err := s.SeeChannels(ctx, src, urls); err != nil {
		t.Fatal(err)
	}
	key := func(i int) string { return ChannelKey(src, urls[i]) }

	// A name is what the reader types; the slug is what its URLs are.
	fav, err := s.CreateCollection(ctx, "  My Favourite Teams ")
	if err != nil || fav.Name != "My Favourite Teams" || fav.Slug != "my-favourite-teams" {
		t.Fatalf("create: %+v %v", fav, err)
	}
	if _, err := s.CreateCollection(ctx, "my favourite teams"); err == nil {
		t.Error("two collections cannot share a slug: it is their URL")
	}
	var verr *ValidationError
	if _, err := s.CreateCollection(ctx, "   "); !errors.As(err, &verr) {
		t.Errorf("a nameless collection: %v", err)
	}

	// Adding is idempotent, so adding a selection that overlaps one already in it says
	// how many were actually new.
	if n, err := s.AddToCollection(ctx, fav.ID, []string{key(0), key(1)}); err != nil || n != 2 {
		t.Fatalf("add: %d %v", n, err)
	}
	if n, err := s.AddToCollection(ctx, fav.ID, []string{key(1), key(2)}); err != nil || n != 1 {
		t.Errorf("adding one already there: %d %v", n, err)
	}
	members, _ := s.CollectionMembers(ctx, fav.ID)
	if len(members) != 3 {
		t.Errorf("members: %v", members)
	}
	if n, err := s.RemoveFromCollection(ctx, fav.ID, []string{key(2), "never-there"}); err != nil || n != 1 {
		t.Errorf("remove: %d %v", n, err)
	}
	if _, err := s.AddToCollection(ctx, 999, []string{key(0)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("adding to a collection that is not there: %v", err)
	}

	all, err := s.Collections(ctx)
	if err != nil || len(all) != 1 || all[0].Channels != 2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	got, ok, _ := s.CollectionBySlug(ctx, "my-favourite-teams")
	if !ok || got.ID != fav.ID {
		t.Errorf("by slug: %+v %v", got, ok)
	}
	if _, ok, _ = s.CollectionBySlug(ctx, "nope"); ok {
		t.Error("a slug nothing answers to")
	}

	// Renaming moves the URLs with the name.
	if err := s.RenameCollection(ctx, fav.ID, "Washington"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = s.CollectionBySlug(ctx, "washington"); !ok {
		t.Error("renamed collection should answer to its new slug")
	}
	if err := s.RenameCollection(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming one that is not there: %v", err)
	}

	// A channel that is forgotten leaves the collections it was in.
	if _, err := s.ForgetChannels(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if members, _ = s.CollectionMembers(ctx, fav.ID); len(members) != 0 {
		t.Errorf("membership should follow the channel: %v", members)
	}
	if all, _ = s.Collections(ctx); len(all) != 1 || all[0].Channels != 0 {
		t.Errorf("the collection itself stays: %+v", all)
	}

	// Deleting takes the collection and its membership, and nothing else.
	if err := s.DeleteCollection(ctx, fav.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.Collections(ctx); len(all) != 0 {
		t.Errorf("after delete: %+v", all)
	}
	if err := s.DeleteCollection(ctx, fav.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}
