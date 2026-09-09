package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jonmaddox/epg3r/internal/store"
)

type collectionsPage struct {
	Collections []collectionRow
	Error       string
}

// collectionRow is one collection and the two URLs it answers at.
type collectionRow struct {
	store.Collection
	M3U   string
	XMLTV string
}

func (s *Server) collections(r *http.Request) (collectionsPage, error) {
	all, err := s.Store.Collections(r.Context())
	if err != nil {
		return collectionsPage{}, err
	}
	base := s.baseURL(r)
	var d collectionsPage
	for _, c := range all {
		d.Collections = append(d.Collections, collectionRow{Collection: c,
			M3U: base + M3UPath + "/" + c.Slug, XMLTV: base + XMLTVPath + "/" + c.Slug})
	}
	return d, nil
}

func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	d, err := s.collections(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "collections", s.view("Collections", "collections", d))
}

// handleSaveCollection creates one, or renames the one addressed.
func (s *Server) handleSaveCollection(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	id := r.PathValue("id")
	if id == "" {
		_, err := s.Store.CreateCollection(r.Context(), name)
		s.collectionsResult(w, r, err, "Saved")
		return
	}
	err := s.editCollection(r, id, func(c store.Collection) error {
		return s.Store.RenameCollection(r.Context(), c.ID, name)
	})
	s.collectionsResult(w, r, err, "Saved")
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	err := s.editCollection(r, r.PathValue("id"), func(c store.Collection) error {
		return s.Store.DeleteCollection(r.Context(), c.ID)
	})
	s.collectionsResult(w, r, err, "Deleted")
}

// editCollection changes the collection an id names, and drops whatever was rendered
// under its old slug. A slug is the collection's URL: renaming frees one for another
// collection to claim, and a stale rendering left behind would answer to it.
func (s *Server) editCollection(r *http.Request, id string, change func(store.Collection) error) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return store.ErrNotFound
	}
	c, ok, err := s.Store.CollectionByID(r.Context(), n)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return change(c)
}

// collectionErr turns a store error into something to tell the reader. fatal means it
// has already been answered as a server error.
func (s *Server) collectionErr(w http.ResponseWriter, r *http.Request, err error) (msg string, fatal bool) {
	if errors.Is(err, store.ErrNotFound) {
		return "That collection is no longer there.", false
	}
	return s.storeErr(w, r, err)
}

// collectionsResult re-renders the page, saying what went wrong if anything did.
func (s *Server) collectionsResult(w http.ResponseWriter, r *http.Request, err error, done string) {
	d, derr := s.collections(r)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	if err != nil {
		msg, fatal := s.collectionErr(w, r, err)
		if fatal {
			return
		}
		d.Error = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
	} else {
		toast(w, "ok", done)
	}
	s.partial(w, r, "collections", "collection_list", d)
}

// handleRemoveFromCollection takes the channels chosen out of the collection being
// looked at. Only reachable from that view, because that is the only place the reader
// can see what is in one.
func (s *Server) handleRemoveFromCollection(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	keys := r.Form["key"]
	id, err := strconv.ParseInt(r.FormValue("from"), 10, 64)
	if err != nil || len(keys) == 0 {
		s.refuseLineup(w, r, "Choose the channels to remove.")
		return
	}
	c, ok, err := s.Store.CollectionByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.refuseLineup(w, r, "That collection is no longer there.")
		return
	}
	removed, err := s.Store.RemoveFromCollection(r.Context(), id, keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	toast(w, "ok", fmt.Sprintf("%s removed from %s", plural(removed, "channel"), c.Name))
	r.Form.Del("key")
	s.showLineup(w, r)
}

// handleAddToCollection puts the channels chosen in the Lineup into a collection the
// reader named from the menu.
func (s *Server) handleAddToCollection(w http.ResponseWriter, r *http.Request) {
	s.collect(w, r, func(ctx context.Context) (store.Collection, error) {
		id, err := strconv.ParseInt(r.FormValue("into"), 10, 64)
		if err != nil {
			return store.Collection{}, store.ErrNotFound
		}
		c, ok, err := s.Store.CollectionByID(ctx, id)
		if err == nil && !ok {
			err = store.ErrNotFound
		}
		return c, err
	})
}

// handleAddToNewCollection makes a collection and puts the chosen channels straight in
// it. It is its own endpoint so that which of the two happened is something the request
// says, rather than something the server infers from which field was left blank.
func (s *Server) handleAddToNewCollection(w http.ResponseWriter, r *http.Request) {
	s.collect(w, r, func(ctx context.Context) (store.Collection, error) {
		return s.Store.CreateCollection(ctx, r.FormValue("new"))
	})
}

// collect adds the chosen channels to whichever collection target names.
func (s *Server) collect(w http.ResponseWriter, r *http.Request, target func(context.Context) (store.Collection, error)) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	keys := r.Form["key"]
	if len(keys) == 0 {
		s.refuseLineup(w, r, "Choose the channels to add.")
		return
	}
	ctx := r.Context()
	c, err := target(ctx)
	if err != nil {
		if msg, fatal := s.collectionErr(w, r, err); !fatal {
			s.refuseLineup(w, r, msg)
		}
		return
	}
	added, err := s.Store.AddToCollection(ctx, c.ID, keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch {
	case added == len(keys):
		toast(w, "ok", fmt.Sprintf("%s added to %s", plural(added, "channel"), c.Name))
	case added == 0:
		toast(w, "ok", "Already in "+c.Name)
	default:
		toast(w, "ok", fmt.Sprintf("%s added to %s; the rest were already in it", plural(added, "channel"), c.Name))
	}
	r.Form.Del("key")
	s.showLineup(w, r)
}
