// Package pipeline turns sources into a Snapshot: fetch, parse, classify, resolve
// schedules from every signal, project games onto every channel that carries them,
// and persist the result.
package pipeline

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/m3u"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/titleparse"
	"github.com/jonmaddox/epg3r/internal/xmltv"
)

// Runner runs refreshes.
type Runner struct {
	Store   *store.Store
	Catalog *catalog.Catalog
	Fetcher *Fetcher
	Now     func() time.Time
	Log     *slog.Logger
	Phase   func(string) // optional progress callback for the UI

	// Parsed guides, by source. A provider's XMLTV is the most expensive thing a run
	// reads, and most refreshes get a 304 back, so the same bytes would otherwise be
	// parsed again every hour. Runs do not overlap, but the scheduler and a manual
	// refresh reach this from different goroutines.
	mu     sync.Mutex
	guides map[int64]*xmltv.Guide
}

// Report summarises a run.
type Report struct {
	RunID    int64
	Status   store.RunStatus
	Counts   store.RunCounts
	Problems []string
	Duration time.Duration
}

// runConfig is the settings and filters a run works from.
type runConfig struct {
	threshold float64
	forget    time.Duration // how long a channel gone from its playlist keeps its number
	emitIdle  bool
	keepRuns  int
	loc       *time.Location
	catalog   *catalog.Catalog // with the user's league overrides applied
}

// entry is one playlist line's outcome: always a row for the run history, and a
// channel when it belongs in the guide.
type entry struct {
	row     store.RunChannel
	key     string // this playlist line's identity: the hash of its URL
	ch      *model.Channel
	league  *catalog.League
	event   *model.Event // slot channels: the game parsed from the title
	teamKey string       // team channels: roster key
}

// sourceData is what one source yielded.
type sourceData struct {
	entries []m3u.Entry
	guide   *xmltv.Guide
	status  store.FetchStatus
	problem string
}

// Run performs one refresh. The run row is always finalized: an early failure marks
// it failed rather than leaving it "running" forever.
func (r *Runner) Run(ctx context.Context, trigger store.Trigger) (snap *model.Snapshot, rep *Report, err error) {
	started := r.now()
	log := r.log()

	cfg, err := r.loadConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	runID, err := r.Store.StartRun(ctx, trigger)
	if err != nil {
		return nil, nil, err
	}
	rep = &Report{RunID: runID}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		// The caller's context may be the reason we are here; use a fresh one.
		fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		msg := "interrupted"
		if err != nil {
			msg = err.Error()
		}
		if ferr := r.Store.FinishRun(fctx, runID, store.RunFailed, msg, nil, nil, cfg.keepRuns); ferr != nil {
			log.Error("could not finalize failed run", "run", runID, "err", ferr)
		}
	}()

	sources, err := r.Store.ListSources(ctx)
	if err != nil {
		return nil, nil, err
	}

	var (
		entries []*entry
		ix      = newEventIndex()
		nums    = &numbering{by: map[string]*entry{}}
		guided  = map[int64]bool{} // sources whose guide is worth keeping parsed
		anyData bool
	)
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		r.phase("fetching " + src.Name)
		data := r.fetchSource(ctx, src)
		if data.problem != "" {
			rep.Problems = append(rep.Problems, data.problem)
			log.Warn("source", "source", src.Name, "problem", data.problem)
		}
		_ = r.Store.RecordSourceFetch(ctx, src.ID, store.SourceFetchResult{Status: data.status, Error: data.problem, ChannelCount: len(data.entries)})
		if len(data.entries) == 0 {
			continue
		}
		anyData = true

		if data.guide != nil {
			guided[src.ID] = true
		}
		read, err := r.source(ctx, cfg, src, data, ix, nums)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, read...)
	}
	r.forgetGuides(guided)
	if err := r.number(ctx, nums); err != nil {
		return nil, nil, err
	}
	// Channels no playlist has carried for a while give their numbers back. Without
	// this a provider that changes its stream URLs would fill a league's block with
	// channels that no longer exist, and new ones would stop being numbered at all.
	if anyData && cfg.forget > 0 {
		if n, err := r.Store.ForgetChannels(ctx, r.now().Add(-cfg.forget)); err != nil {
			log.Warn("could not forget channels", "err", err)
		} else if n > 0 {
			log.Info("forgot channels gone from every playlist", "channels", n)
		}
	}

	r.phase("assembling guide")
	ix.finalize()
	now := r.now()
	snap = &model.Snapshot{RunID: runID, GeneratedAt: now.UTC(), Channels: assemble(cfg, entries, ix, now)}

	rows := make([]store.RunChannel, len(entries))
	for i, en := range entries {
		rows[i] = en.row
	}
	rep.Counts = store.CountOutcomes(rows)
	switch {
	case !anyData:
		rep.Status = store.RunFailed
	case len(rep.Problems) > 0:
		rep.Status = store.RunPartial
	default:
		rep.Status = store.RunOK
	}
	rep.Duration = r.now().Sub(started)

	if err := r.Store.FinishRun(ctx, runID, rep.Status, strings.Join(rep.Problems, "; "), rows, snap, cfg.keepRuns); err != nil {
		return nil, nil, fmt.Errorf("persist run: %w", err)
	}
	finalized = true
	log.Info("run finished", "run", runID, "status", rep.Status, "channels", len(snap.Channels),
		"exported", rep.Counts[store.OutcomeExported], "idle", rep.Counts[store.OutcomeIdle],
		"duplicates", rep.Counts[store.OutcomeDuplicate], "dur", rep.Duration.Round(time.Millisecond))
	if !anyData {
		return snap, rep, fmt.Errorf("no source produced any channels")
	}
	return snap, rep, nil
}

// guide parses a source's XMLTV, or hands back the parse from last time when the
// provider answered that nothing has changed. The guide is read-only once parsed.
func (r *Runner) guide(sourceID int64, res FetchResult) (*xmltv.Guide, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.Status != store.FetchFresh {
		if g, ok := r.guides[sourceID]; ok {
			return g, nil
		}
	}
	g, err := xmltv.Read(bytes.NewReader(res.Body))
	if err != nil {
		return nil, err
	}
	if r.guides == nil {
		r.guides = map[int64]*xmltv.Guide{}
	}
	r.guides[sourceID] = g
	return g, nil
}

// forgetGuides drops the guides of sources this run did not read, so a source that is
// deleted, or that loses its XMLTV URL, does not keep its programmes in memory forever.
func (r *Runner) forgetGuides(keep map[int64]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	maps.DeleteFunc(r.guides, func(id int64, _ *xmltv.Guide) bool { return !keep[id] })
}

// Probe fetches a playlist URL without caching and reports how many channels it holds,
// for testing a source before saving it.
func (r *Runner) Probe(ctx context.Context, url string) (int, error) {
	f := &Fetcher{Client: r.Fetcher.Client, MaxBytes: r.Fetcher.MaxBytes}
	res, err := f.Fetch(ctx, url, "probe")
	if err != nil {
		return 0, err
	}
	entries, err := m3u.Parse(bytes.NewReader(res.Body))
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("the response is not an M3U playlist (no channels found)")
	}
	return len(entries), nil
}

func (r *Runner) loadConfig(ctx context.Context) (runConfig, error) {
	s, err := r.Store.Settings(ctx)
	if err != nil {
		return runConfig{}, err
	}
	overrides, err := r.Store.LeagueOverrides(ctx)
	if err != nil {
		return runConfig{}, err
	}
	return runConfig{
		threshold: s.Float(store.SettingConfidenceThreshold),
		forget:    time.Duration(s.Int(store.SettingForgetChannelsAfter)) * 24 * time.Hour,
		emitIdle:  s.Bool(store.SettingEmitPlaceholderProg),
		keepRuns:  s.Int(store.SettingKeepRuns),
		loc:       s.Location(),
		catalog:   r.Catalog.WithOverrides(overrides),
	}, nil
}

// fetchSource downloads and parses a source's playlist and its optional guide, the two
// in parallel. A missing playlist is a problem; a missing guide is only a warning.
func (r *Runner) fetchSource(ctx context.Context, src store.Source) sourceData {
	var (
		data    sourceData
		m3uRes  FetchResult
		m3uErr  error
		xmlRes  FetchResult
		xmlErr  error
		wg      sync.WaitGroup
		problem []string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		m3uRes, m3uErr = r.Fetcher.Fetch(ctx, src.URL, fmt.Sprintf("source-%d.m3u", src.ID))
	}()
	if src.XMLTVURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			xmlRes, xmlErr = r.Fetcher.Fetch(ctx, src.XMLTVURL, fmt.Sprintf("source-%d.xml", src.ID))
		}()
	}
	wg.Wait()

	if m3uErr != nil {
		return sourceData{status: store.FetchFailed, problem: m3uErr.Error()}
	}
	data.status = m3uRes.Status
	if m3uRes.Warning != nil {
		problem = append(problem, m3uRes.Warning.Error())
	}
	entries, err := m3u.Parse(bytes.NewReader(m3uRes.Body))
	if err != nil {
		return sourceData{status: store.FetchFailed, problem: fmt.Sprintf("%s: %v", src.Name, err)}
	}
	data.entries = entries

	if src.XMLTVURL != "" {
		if xmlErr != nil {
			problem = append(problem, xmlErr.Error())
		} else {
			if xmlRes.Warning != nil {
				problem = append(problem, xmlRes.Warning.Error())
			}
			if g, err := r.guide(src.ID, xmlRes); err != nil {
				problem = append(problem, fmt.Sprintf("%s guide: %v", src.Name, err))
			} else {
				data.guide = g
			}
		}
	}
	data.problem = strings.Join(problem, "; ")
	return data
}

// sourceRun is one source being worked through: what it is, how to read its times, the
// identities the store already holds for it, and what this run has made of its lines so
// far. claimed holds both the ids granted and the ids merely asked for, because the
// family allocator only wants a hint about what is spoken for.
type sourceRun struct {
	src     store.Source
	loc     *time.Location
	alloc   *allocator
	nums    *numbering
	known   map[string]store.Channel
	claimed map[string]bool
	seen    map[string]*entry
	filler  map[string]*model.Event // "Next game" airings already read, by league and title
}

// family asks which provider style a slot belongs to, answering the allocator's
// question about what this run has already spoken for.
func (run *sourceRun) family(ctx context.Context, lg *catalog.League, res titleparse.Result, title string) (int, error) {
	return run.alloc.family(ctx, lg, res, title, func(f int) bool { return run.claimed[run.slotID(lg, f, res.Slot)] })
}

// slotID is how this source's slot channels are named. One spelling, so that what the
// allocator is told is spoken for is the same string a channel would be published under.
func (run *sourceRun) slotID(lg *catalog.League, family, slot int) string {
	return run.withPrefix(lg.ChannelID(family, slot))
}

// withPrefix qualifies an id when a source names its channels alongside another's.
func (run *sourceRun) withPrefix(id string) string {
	if run.src.IDPrefix != "" {
		return run.src.IDPrefix + " " + id
	}
	return id
}

// numbering collects the channels a run has recognised but never numbered, so every
// source is placed in one pass and the store sees the whole demand at once.
type numbering struct {
	want []store.Assignment
	by   map[string]*entry
}

// propose takes the identity a channel already has, or joins it to the queue for one.
// Nothing here can move a channel that has been published before.
func (run *sourceRun) propose(en *entry, want store.Assignment) {
	if c, ok := run.known[en.key]; ok && c.ChannelID != "" {
		en.settle(c.ChannelID, c.Number)
		en.ch.ByUser = c.ByUser
		run.claimed[c.ChannelID] = true
		return
	}
	want.Key = en.key
	run.claimed[want.PreferredID] = true
	run.nums.want = append(run.nums.want, want)
	run.nums.by[en.key] = en
}

// settle records the identity a channel is published under.
func (en *entry) settle(id string, number int) {
	en.ch.ID, en.ch.Name, en.ch.Number = id, id, number
	en.row.ChannelID, en.row.ChannelNumber = id, number
}

// source works through one playlist: what each line already is, what this run makes of
// it, and which games it carries.
func (r *Runner) source(ctx context.Context, cfg runConfig, src store.Source, data sourceData, ix *eventIndex, nums *numbering) ([]*entry, error) {
	// A channel is a URL. Record every line this source is serving and read back what
	// each one already is; a line seen before keeps the id and number it has.
	urls := make([]string, len(data.entries))
	for i, e := range data.entries {
		urls[i] = e.URL
	}
	known, err := r.Store.SeeChannels(ctx, src.ID, urls)
	if err != nil {
		return nil, fmt.Errorf("record channels: %w", err)
	}
	run := &sourceRun{src: src, loc: cfg.loc, alloc: newAllocator(r.Store, src.ID), nums: nums,
		known: known, claimed: map[string]bool{}, seen: map[string]*entry{},
		filler: map[string]*model.Event{}}
	if src.Timezone != "" {
		if l, err := titleparse.LoadLocation(src.Timezone); err == nil {
			run.loc = l
		}
	}

	r.phase(fmt.Sprintf("parsing %d channels from %s", len(data.entries), src.Name))
	read := make([]*entry, 0, len(data.entries))
	for _, e := range data.entries {
		en, err := r.classify(ctx, cfg, run, e, ix)
		if err != nil {
			return nil, err
		}
		// A team channel's games may also come from the provider's own guide.
		if en.ch != nil && en.ch.Kind == model.KindTeam && data.guide != nil {
			progs := data.guide.Programmes[e.Attr("tvg-id")]
			if len(progs) == 0 {
				progs = data.guide.Programmes[e.Attr("tvg-name")]
			}
			for _, ev := range eventsFromGuide(en.league, cfg.catalog.Teams(en.league, false), progs, run.loc, run.filler) {
				ix.add(ev)
			}
		}
		read = append(read, en)
	}
	return read, nil
}

// classify decides what one playlist entry is. Which channel it is was decided by its
// URL; this works out everything else, and proposes an id and a number for a line the
// store has never numbered.
func (r *Runner) classify(ctx context.Context, cfg runConfig, run *sourceRun, e m3u.Entry, ix *eventIndex) (*entry, error) {
	en := &entry{key: store.ChannelKey(run.src.ID, e.URL), row: store.RunChannel{
		SourceID: run.src.ID, Group: e.Group(), RawTitle: e.Title, TvgID: e.Attr("tvg-id"), TvgName: e.Attr("tvg-name"),
		TvgLogo: e.Attr("tvg-logo"), StreamURL: e.URL,
	}}
	// The same URL twice in one playlist is one channel, listed twice.
	if prev, dup := run.seen[en.key]; dup {
		en.row.Status, en.row.Reason = store.OutcomeDuplicate, "the same stream is already listed as "+prev.row.RawTitle
		return en, nil
	}
	run.seen[en.key] = en
	lg, ok := cfg.catalog.MatchLeague(e.Group(), e.Title)
	if !ok {
		en.row.Status = store.OutcomeUnmatched
		return en, nil
	}
	en.league = lg
	en.row.LeagueKey = lg.Key

	res := titleparse.Parse(titleparse.Context{Now: r.now(), Loc: lg.Location(run.loc), League: lg, Catalog: cfg.catalog}, e.Title)
	en.row.NormalizedTitle, en.row.Kind, en.row.Confidence = res.Normalized, string(res.Kind), res.Confidence

	channel := func(kind model.ChannelKind) *model.Channel {
		return &model.Channel{Key: en.key, Kind: kind, LeagueKey: lg.Key,
			LogoURL: e.Attr("tvg-logo"), StreamURL: e.URL, SourceID: run.src.ID}
	}

	switch res.Kind {
	case model.KindNetwork:
		en.row.Status, en.row.Reason = store.OutcomeNetwork, "not an event channel"

	case model.KindPlaceholder, model.KindSlot:
		// The family and the slot only say where this channel would like to sit. If the
		// league has no room for another provider style, or the slot is beyond the
		// league's span, it simply takes the next free number instead of being dropped.
		family, err := run.family(ctx, lg, res, e.Title)
		if err != nil {
			return nil, err
		}
		en.ch = channel(res.Kind)
		en.ch.LogoURL = cmp.Or(en.ch.LogoURL, lg.Logo)
		from, to := lg.SlotRange()
		run.propose(en, store.Assignment{
			PreferredID:     run.slotID(lg, family, res.Slot),
			PreferredNumber: lg.SlotChannelNumber(family, res.Slot),
			Base:            from, Limit: to})
		switch {
		case res.Kind == model.KindPlaceholder:
			en.row.Status = store.OutcomeIdle
		case (res.TeamARaw == "" && res.EventTitle == "") || res.Confidence < cfg.threshold:
			en.row.Status = store.OutcomeLowConf
			en.row.Reason = fmt.Sprintf("confidence %.2f below %.2f", res.Confidence, cfg.threshold)
			en.ch.Kind = model.KindPlaceholder
		default:
			ev := eventFromTitle(lg, res)
			en.event = ix.add(ev)
			en.row.Status, en.row.Matchup = store.OutcomeExported, ev.SubTitle
			en.row.Team1, en.row.Team2 = ev.SideName(0), ev.SideName(1)
			st, sp := ev.Start, ev.Stop
			en.row.StartAt, en.row.StopAt = &st, &sp
		}

	case model.KindTeam:
		ref := teamRef(lg, res.Team)
		en.ch = channel(model.KindTeam)
		en.ch.Team, en.ch.FeedNote = &ref, res.Team.Name+" broadcast"
		en.teamKey = res.Team.Key
		en.row.Team1, en.row.Status = res.Team.Name, store.OutcomeExported
		// Team channels carry no number of their own, so they take the next free one in
		// their league's team block.
		from, to := lg.TeamRange()
		run.propose(en, store.Assignment{
			PreferredID: run.withPrefix(lg.LabelPrefix + " " + cfg.catalog.Teams(lg, false).ShortName(res.Team)),
			Base:        from, Limit: to})
	}
	return en, nil
}

// number gives an identity to every channel a run recognised but has never numbered.
// One pass for all sources, so the store places them against the whole demand at once.
// A channel that cannot be given a number, because its league's block is full, stays
// out of the guide and says so.
func (r *Runner) number(ctx context.Context, nums *numbering) error {
	got, err := r.Store.AssignNumbers(ctx, nums.want)
	if err != nil {
		return fmt.Errorf("assign channel numbers: %w", err)
	}
	for key, en := range nums.by {
		c, ok := got[key]
		if !ok {
			en.ch = nil
			en.row.Status, en.row.Reason = store.OutcomeNoNumber, "this league's block has no free number"
			continue
		}
		en.settle(c.ChannelID, c.Number)
	}
	return nil
}

// assemble turns entries into the exported channel list, placing every game on every
// channel that carries it.
func assemble(cfg runConfig, entries []*entry, ix *eventIndex, now time.Time) []model.Channel {
	var channels []model.Channel
	for _, en := range entries {
		if en.ch == nil {
			continue
		}
		ch := *en.ch
		switch {
		case en.event != nil:
			ch.Programmes = append(ch.Programmes, model.Programme{Event: *en.event})
		case en.teamKey != "":
			for _, ev := range ix.forTeam(en.league.Key, en.teamKey) {
				ch.Programmes = append(ch.Programmes, model.Programme{Event: *ev, Note: ch.FeedNote})
			}
		}
		if len(ch.Programmes) == 0 {
			if cfg.emitIdle {
				ch.Programmes = append(ch.Programmes, idleProgramme(en.league, now))
			}
			if en.row.Status == store.OutcomeExported {
				en.row.Status = store.OutcomeIdle
			}
		}
		channels = append(channels, ch)
	}
	snap := model.Snapshot{Channels: channels}
	snap.SortChannels()
	return snap.Channels
}

func idleProgramme(lg *catalog.League, now time.Time) model.Programme {
	start := now.UTC().Truncate(time.Hour)
	return model.Programme{Idle: true, Event: model.Event{
		SeriesID: lg.SeriesID, LeagueKey: lg.Key, Title: lg.Name + ": No Event Scheduled", Start: start, Stop: start.Add(24 * time.Hour),
		Genre: lg.Genre, Categories: lg.Categories,
	}}
}

// log and phase answer for the optional fields, so callers need not check them.
func (r *Runner) log() *slog.Logger { return cmp.Or(r.Log, slog.Default()) }

func (r *Runner) phase(s string) {
	if r.Phase != nil {
		r.Phase(s)
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
