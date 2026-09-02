package pipeline

import (
	"context"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/titleparse"
)

// allocator hands out sticky channel identities for one source during a run. It
// loads what the store already knows once, answers from memory, and only goes back
// to the store for genuinely new provider styles or team feeds.
type allocator struct {
	ctx      context.Context
	store    *store.Store
	sourceID int64
	families map[string][]store.Family // by league key
	allocs   map[[2]string]store.ChannelAlloc
}

func newAllocator(ctx context.Context, st *store.Store, sourceID int64, leagues []catalog.League) (*allocator, error) {
	a := &allocator{ctx: ctx, store: st, sourceID: sourceID, families: map[string][]store.Family{}}
	var err error
	if a.allocs, err = st.ChannelAllocs(ctx, sourceID); err != nil {
		return nil, err
	}
	for i := range leagues {
		if a.families[leagues[i].Key], err = st.Families(ctx, sourceID, leagues[i].Key); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// family picks the provider family for a slot entry. Titles with a schedule carry a
// full fingerprint and get a sticky family. Placeholders only carry the label style, so
// they join the first family with that label whose slot is still free in this run.
func (a *allocator) family(lg *catalog.League, res titleparse.Result, title string, taken func(channelID string) bool) (int, bool, error) {
	fams := a.families[lg.Key]
	if res.SchedShape != "" {
		for _, f := range fams {
			if f.LabelShape == res.LabelShape && f.SchedShape == res.SchedShape {
				return f.Index, true, nil
			}
		}
		return a.newFamily(lg, res.LabelShape, res.SchedShape, title)
	}
	first := -1
	for _, f := range fams {
		if f.LabelShape != res.LabelShape {
			continue
		}
		if first < 0 {
			first = f.Index
		}
		if !taken(lg.ChannelID(f.Index, res.Slot)) {
			return f.Index, true, nil
		}
	}
	if first >= 0 {
		return first, true, nil // every family of this style has the slot; caller records a duplicate
	}
	return a.newFamily(lg, res.LabelShape, "", title)
}

func (a *allocator) newFamily(lg *catalog.League, labelShape, schedShape, title string) (int, bool, error) {
	idx, ok, err := a.store.FamilyFor(a.ctx, a.sourceID, lg.Key, labelShape, schedShape, title, lg.MaxFamilies())
	if err != nil || !ok {
		return 0, ok, err
	}
	// The store may have adopted a placeholder-only family; reload to stay exact.
	fams, err := a.store.Families(a.ctx, a.sourceID, lg.Key)
	if err != nil {
		return 0, false, err
	}
	a.families[lg.Key] = fams
	return idx, true, nil
}

// team returns the sticky identity for a team channel feed.
func (a *allocator) team(lg *catalog.League, tvgName, preferredID string) (store.ChannelAlloc, error) {
	k := [2]string{lg.Key, tvgName}
	if al, ok := a.allocs[k]; ok {
		return al, nil
	}
	al, err := a.store.AllocateChannel(a.ctx, a.sourceID, lg.Key, tvgName, preferredID, lg.TeamChannelBase())
	if err != nil {
		return al, err
	}
	a.allocs[k] = al
	return al, nil
}
