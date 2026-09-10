package web

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/art"
	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/scheduler"
	"github.com/jonmaddox/epg3r/internal/store"
)

// refreshSoon asks for a refresh so the outputs catch up with a change. A refresh
// already under way read its settings before this change, so the scheduler holds the
// request and runs again as soon as that one finishes.
func (s *Server) refreshSoon() {
	if s.Refresher == nil {
		return
	}
	if err := s.Refresher.Trigger(store.TriggerManual); err != nil && !errors.Is(err, scheduler.ErrRunning) {
		s.Log.Warn("refresh after filter change", "err", err)
	}
}

// ---------- lineup ----------

// lineupRow is one channel as the Lineup page shows it.
type lineupRow struct {
	*model.Channel
	League string // league display name
	Source string // the source the channel came from
	Picked bool   // ticked for renumbering
	Now    *model.Program
	Next   *model.Program
}

// Scheduled reports whether the channel has anything on now or later.
func (r lineupRow) Scheduled() bool { return r.Now != nil || r.Next != nil }

type lineupPage struct {
	Rows   []lineupRow
	Filter lineupFilter
	// The filter controls. Teams is absent until a league is chosen, and SwapTeams says
	// this response carries a replacement for that one slot.
	League, Kind, Scheduled picker
	Teams                   *picker
	Collections             []store.Collection // to add a selection to
	InCollection            *store.Collection  // the one being looked at, if any
	SwapTeams               bool
	Total                   int
	HasRun                  bool
	Picked                  []string // channels ticked for renumbering
	Start                   string   // the number they would be renumbered from
	Error                   string   // what went wrong with the last renumbering
}

// lineupFilter narrows the channel list. Scheduled is "", "yes", or "no"; Teams holds
// roster keys, and a channel matching any of them is kept.
type lineupFilter struct {
	League, Kind, Query, Scheduled string
	Teams                          []string
	Collection                     map[string]bool // members of the collection being looked at
}

// wants reports whether a channel is worth building a row for. League and type are
// properties of the channel itself, so they are settled before any work is done.
func (f lineupFilter) wants(ch *model.Channel) bool {
	return (f.League == "" || ch.LeagueKey == f.League) &&
		(f.Kind == "" || string(ch.Kind) == f.Kind) &&
		(f.Collection == nil || f.Collection[ch.Key])
}

// keeps reports whether a row survives the rest of the filter. Rows are built with the
// team filter already applied to Now and Next, so a row that matched on a team shows
// that team's airing rather than an unrelated one.
func (f lineupFilter) keeps(row lineupRow) bool {
	switch {
	case len(f.Teams) > 0 && !row.Scheduled() && !isTeam(row.Team, f.Teams):
		return false
	case f.Scheduled == "yes" && !row.Scheduled():
		return false
	case f.Scheduled == "no" && row.Scheduled():
		return false
	case f.Query != "" && !rowMatches(row, f.Query):
		return false
	}
	return true
}

// isTeam reports whether a channel's own team is one of the chosen keys.
func isTeam(t *model.TeamRef, keys []string) bool {
	return t != nil && slices.Contains(keys, t.Key)
}

// nowNext finds what a channel is carrying at t and what comes after. With teams given,
// only airings involving one of them count.
func nowNext(ch *model.Channel, t time.Time, teams []string) (now, next *model.Program) {
	for i := range ch.Programs {
		p := &ch.Programs[i]
		if p.Idle || !involves(p.Event, teams) {
			continue
		}
		switch {
		case onNow(*p, t):
			if now == nil || p.Event.Start.After(now.Event.Start) {
				now = p
			}
		case p.Event.Start.After(t):
			if next == nil || p.Event.Start.Before(next.Event.Start) {
				next = p
			}
		}
	}
	return now, next
}

// sourceNames maps source ids to their names, for labelling channels.
func (s *Server) sourceNames(ctx context.Context) map[int64]string {
	srcs, err := s.Store.ListSources(ctx)
	if err != nil {
		return nil
	}
	out := make(map[int64]string, len(srcs))
	for _, src := range srcs {
		out[src.ID] = src.Name
	}
	return out
}

// lineupRow assembles one row: the channel, where it came from, and what it is carrying.
// When teams are chosen, Now and Next report that team's airings, so every row shows
// why it is in the list.
func (s *Server) lineupRow(ch *model.Channel, t time.Time, sources map[int64]string, teams []string) lineupRow {
	row := lineupRow{Channel: ch, League: s.leagueName(ch.LeagueKey), Source: sources[ch.SourceID]}
	row.Now, row.Next = nowNext(ch, t, teams)
	return row
}

func (s *Server) lineup(r *http.Request) (lineupPage, error) {
	// Read from the form, so a filtered list stays filtered whether it was asked for by
	// query string or posted alongside a renumbering.
	_ = r.ParseForm()
	q := r.Form
	d := lineupPage{}
	d.Filter = lineupFilter{League: q.Get("league"), Kind: q.Get("kind"),
		Query: strings.ToLower(strings.TrimSpace(q.Get("q"))), Scheduled: q.Get("scheduled")}
	snap := s.Snapshots.Get()
	if snap == nil {
		return d, nil
	}
	d.HasRun = true
	// Teams are offered only for a chosen league, and only those the lineup actually
	// carries. A team left selected from another league is dropped.
	// The league is the only choice that changes another control: it decides which teams
	// are on offer, so its request brings a replacement for that one slot with it.
	d.League = s.leaguePicker(d.Filter.League)
	d.SwapTeams = r.Header.Get("X-Refresh-Teams") != ""
	if d.Filter.League != "" {
		teams := teamOptions(snap, d.Filter.League, q["team"])
		d.Filter.Teams = teams.Chosen()
		d.Teams = &teams
	}
	d.Kind = choose("kind", []pickerOption{{Value: "", Label: "All types"}, {Value: "slot", Label: "Event channels"},
		{Value: "team", Label: "Team channels"}, {Value: "placeholder", Label: "Unused"}}, d.Filter.Kind)
	d.Scheduled = choose("scheduled", []pickerOption{{Value: "", Label: "Scheduled or not"}, {Value: "yes", Label: "Something scheduled"},
		{Value: "no", Label: "Nothing scheduled"}}, d.Filter.Scheduled)
	// Collections are offered wherever a selection can be made, and looking at one
	// narrows the list to what is in it.
	if id, err := strconv.ParseInt(q.Get("collection"), 10, 64); err == nil {
		if c, ok, _ := s.Store.CollectionByID(r.Context(), id); ok {
			members, err := s.Store.CollectionMembers(r.Context(), id)
			if err != nil {
				return d, err
			}
			d.InCollection, d.Filter.Collection = &c, members
		}
	}
	// A renumbering that could not be applied comes back with its selection intact:
	// losing it means picking the channels out again to correct a typo.
	d.Picked, d.Start = q["key"], strings.TrimSpace(q.Get("start"))
	now, sources := time.Now(), s.sourceNames(r.Context())
	d.Rows = make([]lineupRow, 0, len(snap.Channels))
	d.Total = len(snap.Channels)
	for i := range snap.Channels {
		ch := &snap.Channels[i]
		if !d.Filter.wants(ch) {
			continue
		}
		if row := s.lineupRow(ch, now, sources, d.Filter.Teams); d.Filter.keeps(row) {
			row.Picked = slices.Contains(d.Picked, ch.Key)
			d.Rows = append(d.Rows, row)
		}
	}
	return d, nil
}

// lineupView builds the whole Lineup, including the collections its actions menu offers.
// The list is loaded here rather than in lineup, because only this block shows it: a
// filter change swaps the table alone and has no use for it.
func (s *Server) lineupView(w http.ResponseWriter, r *http.Request) (lineupPage, bool) {
	d, err := s.lineup(r)
	if err == nil {
		d.Collections, err = s.Store.Collections(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return d, false
	}
	return d, true
}

// onNow reports whether an airing is the one showing at t.
func onNow(p model.Program, t time.Time) bool {
	return !p.Idle && !p.Event.Start.After(t) && p.Event.Stop.After(t)
}

// involves reports whether an airing features one of the chosen teams. No teams chosen
// means every airing counts.
func involves(ev model.Event, teams []string) bool {
	if len(teams) == 0 {
		return true
	}
	for _, t := range ev.Teams {
		if t != nil && slices.Contains(teams, t.Key) {
			return true
		}
	}
	return false
}

// rowMatches searches only what the row puts on screen: the channel, its league and
// source, its team, and the two airings shown. Matching a hidden airing would leave the
// reader wondering why a row is in the list.
func rowMatches(row lineupRow, q string) bool {
	has := func(v string) bool { return strings.Contains(strings.ToLower(v), q) }
	switch {
	case has(row.ID), has(row.Name), has(row.League), has(row.Source), has(strconv.Itoa(row.Number)):
		return true
	case row.Team != nil && (has(row.Team.Name) || has(row.Team.Abbr)):
		return true
	}
	for _, p := range []*model.Program{row.Now, row.Next} {
		if p != nil && has(p.Event.SubTitle) {
			return true
		}
	}
	return false
}

// teamOptions builds the team picker from the teams a league's channels carry, in name
// order. Offering the whole roster would list teams the lineup has never seen.
func teamOptions(snap *model.Snapshot, league string, chosen []string) picker {
	names := map[string]string{}
	note := func(t *model.TeamRef) {
		if t != nil && t.LeagueKey == league {
			names[t.Key] = t.Name
		}
	}
	for _, ch := range snap.Channels {
		if ch.LeagueKey != league {
			continue
		}
		note(ch.Team)
		for _, p := range ch.Programs {
			for _, t := range p.Event.Teams {
				note(t)
			}
		}
	}
	out := make([]pickerOption, 0, len(names))
	for key, name := range names {
		out = append(out, pickerOption{Value: key, Label: name, Selected: slices.Contains(chosen, key)})
	}
	slices.SortFunc(out, func(a, b pickerOption) int { return strings.Compare(a.Label, b.Label) })
	return picker{Name: "team", Label: "Teams", Multiple: true, Options: out}
}

func (s *Server) handleLineup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.lineupView(w, r)
	if !ok {
		return
	}
	s.page(w, r, "lineup", s.view("Lineup", "lineup", d))
}

// channelPage is one channel and everything it is scheduled to carry.
type channelPage struct {
	Channel  model.Channel
	League   string          // display name
	Source   string          // where it came from
	Programs []model.Program // in start order, soonest first
	NowIndex int             // which of them is on now, or -1
}

// handleChannel shows one channel. It is addressed by its key, the only thing about a
// channel that never changes; the number is what it is published under, and will be
// editable when the numbering UI lands.
func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	snap := s.Snapshots.Get()
	if snap == nil {
		http.NotFound(w, r)
		return
	}
	ch, ok := snap.ByKey(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	d := channelPage{Channel: ch, League: s.leagueName(ch.LeagueKey),
		Source: s.sourceNames(r.Context())[ch.SourceID], Programs: ch.SortedPrograms(), NowIndex: -1}
	for i := range d.Programs {
		if onNow(d.Programs[i], now) {
			d.NowIndex = i
		}
	}
	s.page(w, r, "channel", s.view(ch.ID, "lineup", d))
}

// handleRenumber moves channels to numbers the user has chosen. Numbers are the one
// thing about a channel a user owns: a playlist has none, so what a consumer sees is
// whatever epg3r hands out until someone says otherwise.
func (s *Server) handleRenumber(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	keys := r.Form["key"]
	start, err := strconv.Atoi(strings.TrimSpace(r.FormValue("start")))
	if len(keys) == 0 {
		s.refuseLineup(w, r, "Choose the channels to renumber.")
		return
	}
	if err != nil || start <= 0 {
		s.refuseLineup(w, r, "A starting number has to be a positive whole number.")
		return
	}

	// The numbers run from the start in the order the reader is looking at, which is the
	// order the form posts them in.
	want := make(map[string]int, len(keys))
	for i, k := range keys {
		want[k] = start + i
	}
	var verr *store.ValidationError
	switch err := s.Store.SetChannelNumbers(r.Context(), want); {
	case errors.Is(err, store.ErrNotFound):
		s.refuseLineup(w, r, "One of those channels is no longer in the guide.")
		return
	case errors.As(err, &verr):
		s.refuseLineup(w, r, verr.Msg)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}

	// The guide follows at once; the refresh then rebuilds it from the store.
	s.Snapshots.Renumber(want, true)
	s.refreshSoon()
	toast(w, "ok", fmt.Sprintf("%s renumbered from %d", plural(len(keys), "channel"), start))
	// Done with, so the selection goes rather than inviting a second pass.
	r.Form.Del("key")
	r.Form.Del("start")
	s.showLineup(w, r)
}

// refuseLineup re-renders the Lineup with a message and the reader's selection intact,
// so correcting a mistake costs a keystroke rather than choosing the channels again.
func (s *Server) refuseLineup(w http.ResponseWriter, r *http.Request, msg string) {
	d, ok := s.lineupView(w, r)
	if !ok {
		return
	}
	d.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.partial(w, r, "lineup", "lineup_view", d)
}

// showLineup re-renders the Lineup as it now stands.
func (s *Server) showLineup(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.lineupView(w, r); ok {
		s.partial(w, r, "lineup", "lineup_view", d)
	}
}

// plural counts a thing the way a sentence would.
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// ---------- leagues ----------

type leagueCard struct {
	Base      catalog.League   // shipped defaults; the form shows these as placeholders
	Override  catalog.Override // what the user changed, and what the form fills in
	Durations picker           // game length
	StartPads picker           // early start
	Logo      string           // what this league's channels and airings actually wear, override or not
	Placard   string
	Advanced  bool // the fields behind the disclosure carry an override, so show it open
	Error     string
}

// Ladders offered for the two duration fields. The league's own default and its
// current value are added if they are not already here, so nothing is ever dropped.
var (
	gameLengths = []time.Duration{time.Hour, 90 * time.Minute, 2 * time.Hour, 150 * time.Minute, 3 * time.Hour,
		210 * time.Minute, 4 * time.Hour, 270 * time.Minute, 5 * time.Hour, 6 * time.Hour}
	startPads = []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 20 * time.Minute,
		30 * time.Minute, 45 * time.Minute, time.Hour}
)

// durationPicker builds a duration chooser: the ladder plus the league's default and
// current value, in order. Durations are chosen rather than typed, so a league cannot be
// saved with unparseable text. The default carries the empty value, so choosing it
// stores no override and the league keeps following the shipped default.
func durationPicker(name string, ladder []time.Duration, def time.Duration, current *string) picker {
	set := map[time.Duration]bool{def: true}
	for _, d := range ladder {
		set[d] = true
	}
	chosen := def
	if current != nil {
		if d, err := time.ParseDuration(*current); err == nil {
			set[d] = true
			chosen = d
		}
	}
	out := make([]pickerOption, 0, len(set))
	for _, d := range slices.Sorted(maps.Keys(set)) {
		c := pickerOption{Value: d.String(), Label: humanDuration(d), Selected: d == chosen}
		if d == def {
			c.Value, c.Label = "", c.Label+" (default)"
		}
		out = append(out, c)
	}
	p := picker{Name: name, Options: out}
	p.Label = p.Summary()
	return p
}

// humanDuration writes a duration the way a person would say it.
func humanDuration(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case d == 0:
		return "None"
	case h > 0 && m > 0:
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	case h > 0:
		return strconv.Itoa(h) + "h"
	default:
		return strconv.Itoa(m) + "m"
	}
}

type leaguesPage struct {
	Cards []leagueCard
}

// leagueCard builds one card from the shipped league and the user's override.
func (s *Server) leagueCard(base catalog.League, o catalog.Override) leagueCard {
	card := leagueCard{
		Base: base, Override: o,
		Durations: durationPicker("duration", gameLengths, base.Duration, o.Duration),
		StartPads: durationPicker("start_pad", startPads, base.StartPad, o.StartPad),
		Advanced:  o.AiringTitle != nil || o.Logo != nil || o.Placard != nil,
	}
	// The card shows what would go out, not what is typed in the boxes, so the art comes
	// from the league as the override leaves it and through the same two calls the pipeline
	// makes.
	effective := base.With(o)
	card.Logo = art.ForChannel(&effective, &model.Channel{Kind: model.KindSlot})
	card.Placard = art.ForAiring(&effective, &model.Event{})
	return card
}

func (s *Server) leagueCards(r *http.Request) ([]leagueCard, error) {
	overrides, err := s.Store.LeagueOverrides(r.Context())
	if err != nil {
		return nil, err
	}
	cards := make([]leagueCard, 0, len(s.Catalog.Leagues))
	for _, base := range s.Catalog.Leagues {
		cards = append(cards, s.leagueCard(base, overrides[base.Key]))
	}
	return cards, nil
}

func (s *Server) handleLeagues(w http.ResponseWriter, r *http.Request) {
	cards, err := s.leagueCards(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "leagues", s.view("Leagues", "leagues", leaguesPage{Cards: cards}))
}

// leagueOverrideForm reads the form. A field left empty, or equal to the shipped
// default, is not an override, so clearing a field restores the default. The duration
// selects post an empty value for their default option, so the same rule covers them.
func leagueOverrideForm(r *http.Request, base catalog.League) catalog.Override {
	var o catalog.Override
	changed := func(name, def string) *string {
		v := strings.TrimSpace(r.FormValue(name))
		if v == "" || v == def {
			return nil
		}
		return &v
	}
	o.AiringTitle = changed("airing_title", base.AiringTitle)
	o.Duration = changed("duration", base.Duration.String())
	o.StartPad = changed("start_pad", base.StartPad.String())
	o.Logo = changed("logo", base.Logo)
	o.Placard = changed("placard", base.Placard)
	return o
}

// handleSaveLeague stores the edits from a league card.
func (s *Server) handleSaveLeague(w http.ResponseWriter, r *http.Request) {
	base, ok := s.Catalog.League(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.applyLeagueOverride(w, r, *base, leagueOverrideForm(r, *base), base.Name+" saved")
}

// handleResetLeague drops a league's edits by storing an empty override.
func (s *Server) handleResetLeague(w http.ResponseWriter, r *http.Request) {
	base, ok := s.Catalog.League(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.applyLeagueOverride(w, r, *base, catalog.Override{}, base.Name+" reset to defaults")
}

func (s *Server) applyLeagueOverride(w http.ResponseWriter, r *http.Request, base catalog.League, o catalog.Override, msg string) {
	if err := s.Store.SetLeagueOverride(r.Context(), base.Key, o); err != nil {
		if errMsg, fatal := s.storeErr(w, r, err); !fatal {
			card := s.leagueCard(base, o)
			card.Error = errMsg
			s.partial(w, r, "leagues", "league_card", card)
		}
		return
	}
	s.refreshSoon()
	toast(w, "ok", msg)
	s.partial(w, r, "leagues", "league_card", s.leagueCard(base, o))
}

// ---------- previews ----------

type previewPage struct {
	Kind  string // "xmltv" | "m3u"
	Body  string
	Bytes int // the whole document, which Body may be a prefix of
	URL   string
}

// Truncated reports whether Body is only the head of the document.
func (p previewPage) Truncated() bool { return len(p.Body) < p.Bytes }

const previewLimit = 256 << 10

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	var path string
	switch kind {
	case "xmltv":
		path = XMLTVPath
	case "m3u":
		path = M3UPath
	default:
		http.NotFound(w, r) // answered before rendering: an unknown kind costs a whole guide
		return
	}
	base := s.baseURL(r)
	out, ok := s.Snapshots.render("epg3r "+s.Version, base, nil, nil)
	body := out.xml
	if kind == "m3u" {
		body = out.m3u
	}
	d := previewPage{Kind: kind, URL: base + path}
	if ok {
		d.Bytes = len(body)
		if len(body) > previewLimit {
			body = body[:previewLimit]
		}
		d.Body = string(body)
	}
	s.page(w, r, "preview", s.view("Preview "+strings.ToUpper(kind), "lineup", d))
}
