package pipeline

import (
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/titleparse"
	"github.com/jonmaddox/epg3r/internal/xmltv"
)

// newLeagueEvent starts an event with everything that comes from the league, so no
// constructor can forget a field.
func newLeagueEvent(lg *catalog.League, womens bool, origin model.EventOrigin) model.Event {
	return model.Event{
		SeriesID:   lg.SeriesIDFor(womens),
		LeagueKey:  lg.Key,
		Title:      lg.AiringTitleFor(womens),
		Genre:      lg.Genre,
		Categories: lg.Categories,
		Source:     origin,
	}
}

func teamRef(lg *catalog.League, t *catalog.Team) model.TeamRef {
	return model.TeamRef{LeagueKey: lg.Key, Key: t.Key, Name: t.Name, Abbr: t.Abbr, TMSBrandID: t.TMSBrandID}
}

// identity is what makes two events the same game: series, the day it falls on, and the
// sorted identity of both sides (roster keys when resolved, normalized raw names
// otherwise, so "A vs B" and "B @ A" match). Kickoff time is deliberately excluded so a
// provider correcting 7:00 to 7:05 does not create a new game; the day is included so
// rematches stay distinct.
//
// The day is read in one zone for the whole run rather than in whichever zone each title
// happened to be written in. A provider that stamps its times in London has an evening
// game land on tomorrow's date, and keying on that would file it as a different game from
// the one every other channel is carrying.
func identity(ev *model.Event, loc *time.Location) []string {
	parts := []string{ev.SeriesID, ev.Kickoff.In(loc).Format("2006-01-02")}
	teams := teamKeys(ev)
	if len(teams) == 0 {
		parts = append(parts, catalog.Normalize(ev.SubTitle))
	}
	return append(parts, teams...)
}

func teamKeys(ev *model.Event) []string {
	var keys []string
	for i, raw := range ev.TeamsRaw {
		switch {
		case ev.Teams[i] != nil:
			keys = append(keys, ev.Teams[i].Key)
		case raw != "":
			keys = append(keys, catalog.Normalize(raw))
		}
	}
	slices.Sort(keys)
	return keys
}

func eventKey(ev *model.Event, loc *time.Location) string {
	return strings.Join(identity(ev, loc), "|")
}

// episodeID is the identity hashed under the series id; every channel carrying the
// game emits the same one, so Channels DVR records it once.
func episodeID(ev *model.Event, loc *time.Location) string {
	sum := sha1.Sum([]byte(eventKey(ev, loc)))
	return ev.SeriesID + "-" + hex.EncodeToString(sum[:])[:12]
}

// eventIndex collects every game learned during a run, from any signal, so that all
// channels carrying a game share one Event.
type eventIndex struct {
	byKey  map[string]*model.Event
	order  []string
	byTeam map[string][]*model.Event // league|teamKey, built by finalize
	loc    *time.Location            // the zone every event's day is read in
}

func newEventIndex(loc *time.Location) *eventIndex {
	return &eventIndex{byKey: map[string]*model.Event{}, loc: loc}
}

// add merges ev into the index. A guide-sourced event overrides a title-sourced one
// for timing, because the guide knows the real duration; everything else fills gaps.
func (ix *eventIndex) add(ev model.Event) *model.Event {
	key := eventKey(&ev, ix.loc)
	cur, ok := ix.byKey[key]
	if !ok {
		e := ev
		ix.byKey[key] = &e
		ix.order = append(ix.order, key)
		return &e
	}
	if rank(ev.Source) > rank(cur.Source) {
		cur.Kickoff, cur.Start, cur.Stop, cur.TimeKnown, cur.DateAssumed, cur.Source = ev.Kickoff, ev.Start, ev.Stop, ev.TimeKnown, ev.DateAssumed, ev.Source
	}
	for i := range cur.Teams {
		if cur.Teams[i] == nil && ev.Teams[i] != nil {
			cur.Teams[i] = ev.Teams[i]
		}
	}
	// Orientation is a fact one source can know and another cannot. Whichever learns it
	// settles it, and the sides are put in that order: the game has one order, and the name,
	// the picture and the team ids all read from it.
	if cur.Home == nil && ev.Home != nil {
		cur.Away, cur.Home, cur.SubTitle = ev.Away, ev.Home, ev.SubTitle
		if cur.Teams[0] != nil && cur.Teams[0].Key == ev.Home.Key {
			cur.Teams[0], cur.Teams[1] = cur.Teams[1], cur.Teams[0]
			cur.TeamsRaw[0], cur.TeamsRaw[1] = cur.TeamsRaw[1], cur.TeamsRaw[0]
		}
	}
	cur.Network = cmp.Or(cur.Network, ev.Network)
	cur.Description = cmp.Or(cur.Description, ev.Description)
	cur.Confidence = max(cur.Confidence, ev.Confidence)
	return cur
}

func rank(o model.EventOrigin) int {
	switch o {
	case model.OriginXMLTV:
		return 2
	case model.OriginTitle:
		return 1
	}
	return 0
}

// finalize assigns episode ids and builds the per-team lookup. Call once all signals
// have been added.
func (ix *eventIndex) finalize() {
	ix.byTeam = map[string][]*model.Event{}
	for _, key := range ix.order {
		ev := ix.byKey[key]
		ev.ID = episodeID(ev, ix.loc)
		for _, t := range ev.ResolvedTeams() {
			k := ev.LeagueKey + "|" + t.Key
			ix.byTeam[k] = append(ix.byTeam[k], ev)
		}
	}
	for _, evs := range ix.byTeam {
		slices.SortFunc(evs, func(a, b *model.Event) int { return a.Kickoff.Compare(b.Kickoff) })
	}
}

// matchup finds the game between two teams, the one nearest to now when a season has them
// meeting more than once. It answers for a channel that named the teams but not the day.
func (ix *eventIndex) matchup(leagueKey, a, b string, now time.Time) *model.Event {
	var best *model.Event
	for _, ev := range ix.forTeam(leagueKey, a) {
		if !hasTeam(ev, b) {
			continue
		}
		if best == nil || ev.Kickoff.Sub(now).Abs() < best.Kickoff.Sub(now).Abs() {
			best = ev
		}
	}
	return best
}

// hasTeam reports whether one of an event's two sides is this roster team.
func hasTeam(ev *model.Event, key string) bool {
	for _, t := range ev.Teams {
		if t != nil && t.Key == key {
			return true
		}
	}
	return false
}

// forTeam returns the events in a league involving a roster team, soonest first.
func (ix *eventIndex) forTeam(leagueKey, teamKey string) []*model.Event {
	return ix.byTeam[leagueKey+"|"+teamKey]
}

// eventFromTitle builds an Event from a parsed slot title.
func eventFromTitle(lg *catalog.League, res titleparse.Result) model.Event {
	ev := newLeagueEvent(lg, res.Womens, model.OriginTitle)
	ev.Kickoff, ev.TimeKnown, ev.DateAssumed = res.Kickoff, res.TimeKnown, res.DateAssumed
	ev.Network, ev.Confidence = res.Network, res.Confidence
	ev.TeamsRaw = [2]string{res.TeamARaw, res.TeamBRaw}

	switch {
	case res.TeamARaw == "":
		ev.SubTitle = res.EventTitle
	default:
		for i, t := range []*catalog.Team{res.TeamA, res.TeamB} {
			if t != nil {
				ref := teamRef(lg, t)
				ev.Teams[i] = &ref
			}
		}
		// "@" in a channel name means the first side is visiting the second, which is what
		// "at" says. "vs" claims nothing about who is home, and stays "vs" — the same way
		// Gracenote words a matchup it knows and one it does not.
		sep := "vs"
		if res.Sep == "@" {
			sep = "at"
			ev.Away, ev.Home = ev.Teams[0], ev.Teams[1]
		}
		ev.SubTitle = ev.SideName(0) + " " + sep + " " + ev.SideName(1)
	}

	ev.Start = ev.Kickoff.Add(-lg.StartPad).UTC()
	if !res.Stop.IsZero() {
		ev.Stop = res.Stop.Add(lg.EndPad).UTC()
	} else {
		ev.Stop = ev.Kickoff.Add(lg.Duration + lg.EndPad).UTC()
	}
	return ev
}

var (
	reNextGame = regexp.MustCompile(`^Next game: (.+?) at (.+?) at (\d{2}/\d{2}/\d{4} \d{2}:\d{2} [AP]M) \(([^)]+)\)$`)
	reGameAt   = regexp.MustCompile(`^(.+?) (at|vs\.?|@) (.+?)( possible Overtime)?$`)
)

// eventsFromGuide reads a provider's programs for one channel. It understands two
// title shapes: "X at Y" for a game in progress, whose times are the program's own,
// and "Next game: X at Y at <when>" for the filler a provider airs between games.
// Filler carries its own kickoff, so it depends only on the title — and a provider
// repeats the same filler all day on every channel showing that team, so the same title
// arrives thousands of times to describe a few dozen games. Filler is therefore worked
// out once per title and remembered in memo, which the caller keeps for one source.
func eventsFromGuide(lg *catalog.League, teams *catalog.TeamIndex, progs []xmltv.Program, loc *time.Location, memo map[string]*model.Event) []model.Event {
	var out []model.Event
	var last *model.Event
	seen := map[string]bool{} // filler titles already taken from this channel
	for _, p := range progs {
		if ev, known := memo[lg.Key+"\x00"+p.Title]; known {
			if !seen[p.Title] {
				seen[p.Title] = true
				out = append(out, *ev)
			}
			last = nil
			continue
		}
		if m := reNextGame.FindStringSubmatch(p.Title); m != nil {
			zone := loc
			if z, err := titleparse.LoadLocation(m[4]); err == nil {
				zone = z
			}
			kick, err := time.ParseInLocation("01/02/2006 03:04 PM", m[3], zone)
			if err != nil {
				continue
			}
			if ev, ok := guideEvent(lg, teams, m[1], m[2], true, kick, kick.Add(-lg.StartPad), kick.Add(lg.Duration+lg.EndPad), 0.85, loc); ok {
				memo[lg.Key+"\x00"+p.Title] = &ev
				seen[p.Title] = true
				out = append(out, ev)
			}
			last = nil
			continue
		}
		m := reGameAt.FindStringSubmatch(p.Title)
		if m == nil {
			last = nil
			continue
		}
		if m[4] != "" && last != nil && strings.HasPrefix(p.Title, last.SubTitle) {
			last.Stop = p.Stop.UTC() // overtime block extends the game
			continue
		}
		oriented := m[2] == "at" || m[2] == "@"
		if ev, ok := guideEvent(lg, teams, m[1], m[3], oriented, p.Start, p.Start, p.Stop, 0.9, loc); ok {
			ev.Description = p.Desc
			out = append(out, ev)
			last = &out[len(out)-1]
		} else {
			last = nil
		}
	}
	return out
}

// guideEvent builds a game from a provider's program title. oriented says whether the title
// named the sides in away-at-home order, which "at" and "@" do and "vs" does not.
func guideEvent(lg *catalog.League, teams *catalog.TeamIndex, away, home string, oriented bool, kickoff, start, stop time.Time, conf float64, loc *time.Location) (model.Event, bool) {
	a, _, _ := teams.Match(away)
	h, _, _ := teams.Match(home)
	if a == nil || h == nil {
		return model.Event{}, false
	}
	ev := newLeagueEvent(lg, false, model.OriginXMLTV)
	ra, rh := teamRef(lg, a), teamRef(lg, h)
	ev.Teams = [2]*model.TeamRef{&ra, &rh}
	if oriented {
		ev.Away, ev.Home = &ra, &rh
		ev.SubTitle = a.Name + " at " + h.Name
	} else {
		ev.SubTitle = a.Name + " vs " + h.Name
	}
	ev.TeamsRaw = [2]string{a.Name, h.Name}
	ev.Kickoff = kickoff.In(lg.Location(loc))
	ev.Start, ev.Stop = start.UTC(), stop.UTC()
	ev.TimeKnown = true
	ev.Confidence = conf
	return ev, true
}
