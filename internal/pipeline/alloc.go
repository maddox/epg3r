package pipeline

import (
	"context"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/titleparse"
)

// allocator answers which provider style a slot channel belongs to, so that two
// providers who both publish an "NFL 04" do not propose the same number for it. It is
// a preference, not an identity: what makes a channel is its URL.
type allocator struct {
	store    *store.Store
	sourceID int64
	families map[string][]store.Family // by league key, loaded on first use
	full     map[string]bool           // leagues that already hold every family they can
}

func newAllocator(st *store.Store, sourceID int64) *allocator {
	return &allocator{store: st, sourceID: sourceID,
		families: map[string][]store.Family{}, full: map[string]bool{}}
}

// family picks the provider family for a slot entry. Titles with a schedule carry a
// full fingerprint and get a sticky family. Placeholders only carry the label style, so
// they join the first family with that label whose slot is still free in this run —
// which only the caller can answer, since only the caller knows how ids are spelled.
// Beyond a league's family limit a channel simply proposes the first family's id, and
// the store makes it unique: a style epg3r has no room to name separately is still a
// channel.
func (a *allocator) family(ctx context.Context, lg *catalog.League, res titleparse.Result, title string, taken func(family int) bool) (int, error) {
	fams, err := a.known(ctx, lg.Key)
	if err != nil {
		return 0, err
	}
	if res.SchedShape != "" {
		for _, f := range fams {
			if f.LabelShape == res.LabelShape && f.SchedShape == res.SchedShape {
				return f.Index, nil
			}
		}
		return a.newFamily(ctx, lg, res.LabelShape, res.SchedShape, title)
	}
	first := -1
	for _, f := range fams {
		if f.LabelShape != res.LabelShape {
			continue
		}
		if first < 0 {
			first = f.Index
		}
		if !taken(f.Index) {
			return f.Index, nil
		}
	}
	if first >= 0 {
		return first, nil // every family of this style has the slot; the store picks a free number
	}
	return a.newFamily(ctx, lg, res.LabelShape, "", title)
}

// known returns a league's styles, reading them the first time the league comes up. A
// playlist usually carries a handful of the catalog's leagues, so the rest are never read.
func (a *allocator) known(ctx context.Context, leagueKey string) ([]store.Family, error) {
	if fams, ok := a.families[leagueKey]; ok {
		return fams, nil
	}
	fams, err := a.store.Families(ctx, a.sourceID, leagueKey)
	if err != nil {
		return nil, err
	}
	a.families[leagueKey] = fams
	return fams, nil
}

// newFamily records a style the store has not seen. When the league is already at its
// family limit the answer is family 0 — and it is remembered, so a playlist full of
// unnameable styles does not ask again for every line.
func (a *allocator) newFamily(ctx context.Context, lg *catalog.League, labelShape, schedShape, title string) (int, error) {
	if a.full[lg.Key] {
		return 0, nil
	}
	idx, ok, err := a.store.FamilyFor(ctx, a.sourceID, lg.Key, labelShape, schedShape, title, lg.MaxFamilies())
	if err != nil {
		return 0, err
	}
	if !ok {
		a.full[lg.Key] = true
		return 0, nil
	}
	// The store may have adopted a placeholder-only family; reload to stay exact.
	delete(a.families, lg.Key)
	if _, err := a.known(ctx, lg.Key); err != nil {
		return 0, err
	}
	return idx, nil
}
