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
}

// Report summarises a run.
type Report struct {
	RunID    int64
	Status   store.RunStatus
	Counts   store.RunCounts
	Problems []string
	Duration time.Duration
}

// runConfig is the settings snapshot a run works from.
type runConfig struct {
	threshold  float64
	exportIdle bool
	emitIdle   bool
	keepRuns   int
	loc        *time.Location
}

// entry is one playlist line's outcome: always a row for the run history, and a
// channel when it belongs in the guide.
type entry struct {
	row      store.RunChannel
	ch       *model.Channel
	league   *catalog.League
	eventKey string // slot channels: the game parsed from the title
	teamKey  string // team channels: roster key
}

// sourceData is what one source yielded.
type sourceData struct {
	entries []m3u.Entry
	guide   *xmltv.Guide
	status  store.FetchStatus
	problem string
}

// Run performs one refresh.
func (r *Runner) Run(ctx context.Context, trigger store.Trigger) (*model.Snapshot, *Report, error) {
	started := r.now()
	log := cmp.Or(r.Log, slog.Default())
	phase := r.Phase
	if phase == nil {
		phase = func(string) {}
	}

	cfg, err := r.loadConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	runID, err := r.Store.StartRun(ctx, trigger)
	if err != nil {
		return nil, nil, err
	}
	rep := &Report{RunID: runID}

	sources, err := r.Store.ListSources(ctx)
	if err != nil {
		return nil, nil, err
	}

	var (
		entries []*entry
		byID    = map[string]*entry{}
		ix      = newEventIndex()
		anyData bool
	)
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		phase("fetching " + src.Name)
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

		alloc, err := newAllocator(ctx, r.Store, src.ID, r.Catalog.Leagues)
		if err != nil {
			return nil, nil, err
		}
		loc := cfg.loc
		if src.Timezone != "" {
			if l, err := titleparse.LoadLocation(src.Timezone); err == nil {
				loc = l
			}
		}

		phase(fmt.Sprintf("parsing %d channels from %s", len(data.entries), src.Name))
		groupCount := map[string]int{}
		groupLeague := map[string]string{}
		for _, e := range data.entries {
			groupCount[e.Group()]++
			en, err := r.classify(cfg, src, e, loc, alloc, ix, byID)
			if err != nil {
				return nil, nil, err
			}
			if en.league != nil {
				groupLeague[e.Group()] = en.league.Key
				if en.ch != nil && en.ch.Kind == model.KindTeam && data.guide != nil {
					progs := data.guide.Programmes[e.Attr("tvg-id")]
					if len(progs) == 0 {
						progs = data.guide.Programmes[e.Attr("tvg-name")]
					}
					for _, ev := range eventsFromGuide(en.league, r.Catalog.Teams(en.league, false), progs, loc) {
						ix.add(ev)
					}
				}
			}
			entries = append(entries, en)
		}
		for g, n := range groupCount {
			_ = r.Store.UpsertGroup(ctx, src.ID, g, n, groupLeague[g])
		}
	}

	phase("assembling guide")
	ix.finalize()
	now := r.now()
	snap := &model.Snapshot{RunID: runID, GeneratedAt: now.UTC(), Channels: r.assemble(cfg, entries, ix, now)}

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
	log.Info("run finished", "run", runID, "status", rep.Status, "channels", len(snap.Channels),
		"exported", rep.Counts[store.OutcomeExported], "idle", rep.Counts[store.OutcomeIdle],
		"duplicates", rep.Counts[store.OutcomeDuplicate], "dur", rep.Duration.Round(time.Millisecond))
	if !anyData {
		return snap, rep, fmt.Errorf("no source produced any channels")
	}
	return snap, rep, nil
}

func (r *Runner) loadConfig(ctx context.Context) (runConfig, error) {
	s, err := r.Store.Settings(ctx)
	if err != nil {
		return runConfig{}, err
	}
	return runConfig{
		threshold:  s.Float(store.SettingConfidenceThreshold),
		exportIdle: s.Bool(store.SettingExportIdleChannels),
		emitIdle:   s.Bool(store.SettingEmitPlaceholderProg),
		keepRuns:   s.Int(store.SettingKeepRuns),
		loc:        s.Location(),
	}, nil
}

// fetchSource downloads and parses a source's playlist and optional guide, the two
// in parallel. A missing playlist is a problem; a missing guide is a warning.
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
		switch {
		case xmlErr != nil:
			problem = append(problem, xmlErr.Error())
		default:
			if xmlRes.Warning != nil {
				problem = append(problem, xmlRes.Warning.Error())
			}
			if g, err := xmltv.Read(bytes.NewReader(xmlRes.Body)); err != nil {
				problem = append(problem, fmt.Sprintf("%s guide: %v", src.Name, err))
			} else {
				data.guide = g
			}
		}
	}
	data.problem = strings.Join(problem, "; ")
	return data
}

// classify decides what one playlist entry is and, for channels, claims its identity.
func (r *Runner) classify(cfg runConfig, src store.Source, e m3u.Entry, loc *time.Location, alloc *allocator, ix *eventIndex, byID map[string]*entry) (*entry, error) {
	en := &entry{row: store.RunChannel{
		SourceID: src.ID, Group: e.Group(), RawTitle: e.Title, TvgID: e.Attr("tvg-id"), TvgName: e.Attr("tvg-name"),
		TvgLogo: e.Attr("tvg-logo"), StreamURL: e.URL,
	}}
	lg, ok := r.Catalog.MatchLeague(e.Group(), e.Title)
	if !ok {
		en.row.Status = store.OutcomeUnmatched
		return en, nil
	}
	en.league = lg
	en.row.LeagueKey = lg.Key

	res := titleparse.Parse(titleparse.Context{Now: r.now(), Loc: lg.Location(loc), League: lg, Catalog: r.Catalog}, e.Title)
	en.row.NormalizedTitle, en.row.Kind, en.row.Confidence = res.Normalized, string(res.Kind), res.Confidence

	withPrefix := func(id string) string {
		if src.IDPrefix != "" {
			return src.IDPrefix + " " + id
		}
		return id
	}
	channel := func(id string, number int, kind model.ChannelKind) *model.Channel {
		return &model.Channel{ID: id, Number: number, Name: id, Kind: kind, LeagueKey: lg.Key,
			LogoURL: e.Attr("tvg-logo"), StreamURL: e.URL, SourceID: src.ID}
	}

	switch res.Kind {
	case model.KindNetwork:
		en.row.Status, en.row.Reason = store.OutcomeNetwork, "not an event channel"

	case model.KindPlaceholder, model.KindSlot:
		if res.Slot >= lg.SlotSpan {
			en.row.Status = store.OutcomeUnparsed
			en.row.Reason = fmt.Sprintf("slot %d beyond this league's span of %d", res.Slot, lg.SlotSpan)
			return en, nil
		}
		family, ok, err := alloc.family(lg, res, e.Title, func(id string) bool { _, used := byID[withPrefix(id)]; return used })
		if err != nil {
			return nil, err
		}
		if !ok {
			en.row.Status = store.OutcomeUnparsed
			en.row.Reason = fmt.Sprintf("this league already has %d provider styles", lg.MaxFamilies())
			return en, nil
		}
		id := withPrefix(lg.ChannelID(family, res.Slot))
		en.ch = channel(id, lg.SlotChannelNumber(family, res.Slot), res.Kind)
		en.ch.LogoURL = cmp.Or(en.ch.LogoURL, lg.Logo)
		en.row.ChannelID, en.row.ChannelNumber = id, en.ch.Number
		switch {
		case res.Kind == model.KindPlaceholder:
			en.row.Status = store.OutcomeIdle
		case (res.TeamARaw == "" && res.EventTitle == "") || res.Confidence < cfg.threshold:
			en.row.Status = store.OutcomeLowConf
			en.row.Reason = fmt.Sprintf("confidence %.2f below %.2f", res.Confidence, cfg.threshold)
			en.ch.Kind = model.KindPlaceholder
		default:
			ev := eventFromTitle(lg, res)
			en.eventKey = eventKey(ix.add(ev))
			en.row.Status, en.row.Matchup = store.OutcomeExported, ev.SubTitle
			en.row.Team1, en.row.Team2 = ev.SideName(0), ev.SideName(1)
			st, sp := ev.Start, ev.Stop
			en.row.StartAt, en.row.StopAt = &st, &sp
		}
		claim(en, byID)

	case model.KindTeam:
		preferred := withPrefix(lg.LabelPrefix + " " + r.Catalog.Teams(lg, false).ShortName(res.Team))
		// A feed's sticky identity is whatever the provider keeps stable for it. The
		// title is the most reliable; tvg-name is optional and sometimes shared.
		feedKey := cmp.Or(e.Title, e.Attr("tvg-name"), e.Attr("tvg-id"), e.URL)
		al, err := alloc.team(lg, feedKey, preferred)
		if err != nil {
			return nil, fmt.Errorf("allocate team channel: %w", err)
		}
		ref := teamRef(lg, res.Team)
		en.ch = channel(al.ChannelID, al.Number, model.KindTeam)
		en.ch.Team, en.ch.FeedNote = &ref, res.Team.Name+" broadcast"
		en.teamKey = res.Team.Key
		en.row.ChannelID, en.row.ChannelNumber, en.row.Team1, en.row.Status = al.ChannelID, al.Number, res.Team.Name, store.OutcomeExported
		claim(en, byID)
	}
	return en, nil
}

// claim registers a channel id. A channel id should appear once per run; if it
// repeats, an entry with a game beats one without, otherwise the first one keeps the
// id and the other is recorded as a duplicate.
func claim(en *entry, byID map[string]*entry) {
	prev, dup := byID[en.ch.ID]
	if !dup {
		byID[en.ch.ID] = en
		return
	}
	if prev.eventKey == "" && en.eventKey != "" {
		prev.row.Status, prev.row.Reason = store.OutcomeDuplicate, "channel id also carried by "+en.row.RawTitle
		prev.ch = nil
		byID[en.ch.ID] = en
		return
	}
	en.row.Status, en.row.Reason = store.OutcomeDuplicate, "channel id already used by "+prev.row.RawTitle
	en.ch = nil
}

// assemble turns entries into the exported channel list, placing every game on every
// channel that carries it.
func (r *Runner) assemble(cfg runConfig, entries []*entry, ix *eventIndex, now time.Time) []model.Channel {
	var channels []model.Channel
	for _, en := range entries {
		if en.ch == nil {
			continue
		}
		ch := *en.ch
		switch {
		case en.eventKey != "":
			ch.Programmes = append(ch.Programmes, model.Programme{Event: *ix.byKey[en.eventKey]})
		case en.teamKey != "":
			for _, ev := range ix.forTeam(en.league.Key, en.teamKey) {
				ch.Programmes = append(ch.Programmes, model.Programme{Event: *ev, Note: ch.FeedNote})
			}
		}
		if len(ch.Programmes) == 0 {
			if !cfg.exportIdle {
				en.row.Status, en.row.Reason = store.OutcomeIdle, "idle channels not exported"
				continue
			}
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

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
