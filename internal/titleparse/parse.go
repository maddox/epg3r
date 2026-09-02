package titleparse

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
)

// Context is what a parse needs beyond the title itself.
type Context struct {
	Now     time.Time
	Loc     *time.Location // zone for schedule text that names none
	League  *catalog.League
	Catalog *catalog.Catalog
}

// Result is everything learned from one title.
type Result struct {
	Kind       model.ChannelKind `json:"kind"`
	Normalized string            `json:"normalized"`

	// Slot channels
	Slot        int           `json:"slot,omitempty"`
	LabelShape  string        `json:"label_shape,omitempty"` // how the label is written, see splitLabel
	SchedShape  string        `json:"sched_shape,omitempty"` // how the schedule is written; "" for placeholders and untimed titles
	TeamARaw    string        `json:"team_a_raw,omitempty"`
	TeamBRaw    string        `json:"team_b_raw,omitempty"`
	TeamA       *catalog.Team `json:"team_a,omitempty"`
	TeamB       *catalog.Team `json:"team_b,omitempty"`
	Sep         string        `json:"sep,omitempty"`         // "vs" | "@"
	EventTitle  string        `json:"event_title,omitempty"` // when the title is not a matchup, e.g. "MLS 360"
	Womens      bool          `json:"womens,omitempty"`
	Kickoff     time.Time     `json:"kickoff"`
	Stop        time.Time     `json:"stop"` // zero unless the title stated an end time
	TimeKnown   bool          `json:"time_known"`
	DateAssumed bool          `json:"date_assumed"`
	Network     string        `json:"network,omitempty"`
	Feed        string        `json:"feed,omitempty"`
	Confidence  float64       `json:"confidence"`
	Notes       []string      `json:"notes,omitempty"`

	// Team channels
	Team *catalog.Team `json:"team,omitempty"`
}

var (
	reCountry   = regexp.MustCompile(`(?i)^(?:US|USA|UK|CA|CAN|MX)\s*[:|]?\s+`)
	reTeamsWord = regexp.MustCompile(`(?i)^[A-Z]+ teams?\s*:\s*`)
	tokenCache  sync.Map // catalog token pattern -> *regexp.Regexp
)

// leagueTokenRegex strips a leading league word ("NFL ", "(MLB) ", "NBALP: ") using the
// catalog's own vocabulary.
func leagueTokenRegex(c *catalog.Catalog) *regexp.Regexp {
	pattern := c.TokenPattern()
	if v, ok := tokenCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)^\(?` + pattern + `\)?\s*[:|]?\s+`)
	tokenCache.Store(pattern, re)
	return re
}

// Parse classifies a title for a league already resolved from the playlist group.
func Parse(ctx Context, title string) Result {
	norm, _ := Normalize(title)
	res := Result{Normalized: norm}
	lg := ctx.League

	if slot, rest, labelShape, ok := splitLabel(lg, norm); ok {
		res.Slot = slot
		res.LabelShape = labelShape
		if isPlaceholder(rest) {
			res.Kind = model.KindPlaceholder
			return res
		}
		res.Kind = model.KindSlot
		res.fillEvent(ctx, parseEvent(rest))
		return res
	}

	// No slot label: a permanent channel. Strip country, league word, and tags, then
	// see whether what is left is a team.
	body := norm
	body = reCountry.ReplaceAllString(body, "")
	body = reTeamsWord.ReplaceAllString(body, "")
	if ctx.Catalog != nil {
		body = leagueTokenRegex(ctx.Catalog).ReplaceAllString(body, "")
	}
	body = strings.Trim(body, sepChars)
	if body != "" && ctx.Catalog != nil {
		if t, method, score := ctx.Catalog.Teams(lg, false).Match(body); t != nil && (method == catalog.MatchExact || score >= 0.93) {
			res.Kind = model.KindTeam
			res.Team = t
			res.Confidence = score
			return res
		}
	}
	res.Kind = model.KindNetwork
	res.EventTitle = body
	return res
}

func (res *Result) fillEvent(ctx Context, ev eventText) {
	res.TeamARaw, res.TeamBRaw = ev.TeamA, ev.TeamB
	res.Sep = ev.Sep
	res.EventTitle = ev.Title
	res.Womens = ev.Womens
	res.Network = ev.Network
	res.Feed = ev.Feed
	res.Notes = ev.Notes
	res.SchedShape = shape(ev.ScheduleRaw, true)

	if ctx.Catalog != nil && ev.TeamA != "" {
		idx := ctx.Catalog.Teams(ctx.League, ev.Womens)
		res.TeamA, _, _ = idx.Match(ev.TeamA)
		res.TeamB, _, _ = idx.Match(ev.TeamB)
	}

	loc := ctx.Loc
	if loc == nil {
		loc = time.UTC
	}
	res.Kickoff, res.DateAssumed = ev.Schedule.Resolve(ctx.Now, loc)
	res.TimeKnown = ev.Schedule.HasTime
	if ev.Schedule.Stop != nil {
		res.Stop, _ = ev.Schedule.Stop.Resolve(ctx.Now, loc)
	}

	switch {
	case ev.TeamA == "" && ev.Title == "":
		res.Confidence = 0
	case !ev.Schedule.HasTime:
		res.Confidence = 0.5
	case res.DateAssumed:
		res.Confidence = 0.7
	default:
		res.Confidence = 0.95
	}
	if ev.TeamA != "" {
		if res.TeamA == nil {
			res.Confidence -= 0.05
		}
		if res.TeamB == nil {
			res.Confidence -= 0.05
		}
	}
}
