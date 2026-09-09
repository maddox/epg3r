// Package model holds the types shared between the pipeline and the renderers.
// It is a leaf package: it imports nothing from the rest of the app, so the XMLTV
// and M3U writers never depend on parsing or storage.
package model

import (
	"cmp"
	"slices"
	"time"
)

// ChannelKind classifies what an M3U entry is for.
type ChannelKind string

const (
	KindSlot        ChannelKind = "slot"        // numbered event channel; its title changes per game
	KindTeam        ChannelKind = "team"        // permanent channel dedicated to one team
	KindNetwork     ChannelKind = "network"     // a broadcast network or local affiliate
	KindPlaceholder ChannelKind = "placeholder" // a slot channel with nothing scheduled
)

// TeamRef identifies a team resolved against the catalog roster.
type TeamRef struct {
	LeagueKey  string `json:"league_key"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Abbr       string `json:"abbr,omitempty"`
	TMSBrandID string `json:"tms_brand_id,omitempty"` // Gracenote teamBrandId, emitted as <team-id system="tms">
}

// Event is one game, independent of which channels carry it.
type Event struct {
	ID          string      `json:"id"`        // episode id, shared by every channel carrying this game
	SeriesID    string      `json:"series_id"` // groups airings into one show in Channels DVR
	LeagueKey   string      `json:"league_key"`
	Title       string      `json:"title"`     // airing title, e.g. "NFL Football"
	SubTitle    string      `json:"sub_title"` // matchup, e.g. "Chicago Bears vs Green Bay Packers"
	Description string      `json:"description,omitempty"`
	Teams       [2]*TeamRef `json:"teams"`          // resolved team per side, nil when unmatched; aligned with TeamsRaw
	TeamsRaw    [2]string   `json:"teams_raw"`      // each side as written in the source
	Home        *TeamRef    `json:"home,omitempty"` // set when orientation is known
	Away        *TeamRef    `json:"away,omitempty"`
	Kickoff     time.Time   `json:"kickoff"` // scheduled start, in the event's local zone
	Start       time.Time   `json:"start"`   // padded start, UTC
	Stop        time.Time   `json:"stop"`    // padded stop, UTC
	TimeKnown   bool        `json:"time_known"`
	DateAssumed bool        `json:"date_assumed"`
	Network     string      `json:"network,omitempty"`
	Genre       string      `json:"genre"`
	Categories  []string    `json:"categories"`
	PlacardURL  string      `json:"placard_url,omitempty"`
	Confidence  float64     `json:"confidence"`
	Source      EventOrigin `json:"source"`
}

// EventOrigin records where a schedule came from.
type EventOrigin string

const (
	OriginTitle    EventOrigin = "title"    // parsed from a slot channel title
	OriginXMLTV    EventOrigin = "xmltv"    // taken from the provider's guide
	OriginInferred EventOrigin = "inferred" // projected onto a team channel from another channel's event
)

// Channel is one exported channel with the programmes it carries.
type Channel struct {
	Key        string      `json:"key"`     // this channel's identity: the hash of its source and URL
	ID         string      `json:"id"`      // tvg-id / channel-id, e.g. "NFL 03" or "NFL Bears"
	Number     int         `json:"number"`  // channel-number
	ByUser     bool        `json:"by_user"` // the number was set by hand, so nothing reassigns it
	Name       string      `json:"name"`    // display name
	Kind       ChannelKind `json:"kind"`
	LeagueKey  string      `json:"league_key"`
	Team       *TeamRef    `json:"team,omitempty"` // for team channels
	LogoURL    string      `json:"logo_url,omitempty"`
	StreamURL  string      `json:"stream_url"`
	SourceID   int64       `json:"source_id"`
	Programmes []Programme `json:"programmes"`
	FeedNote   string      `json:"feed_note,omitempty"` // e.g. "Bears broadcast"
}

// ByKey finds a channel by its identity.
func (s *Snapshot) ByKey(key string) (Channel, bool) {
	for _, ch := range s.Channels {
		if ch.Key == key {
			return ch, true
		}
	}
	return Channel{}, false
}

// Programme is an Event placed on a Channel. Most fields come from the Event; the
// split exists so a channel can carry several games (from provider XMLTV) and so
// per-channel notes can differ.
type Programme struct {
	Event Event  `json:"event"`
	Note  string `json:"note,omitempty"`
	Idle  bool   `json:"idle,omitempty"` // a "no event scheduled" filler programme
}

// Snapshot is the complete output of one run.
type Snapshot struct {
	RunID       int64     `json:"run_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Channels    []Channel `json:"channels"`
}

// ResolvedTeams returns the sides that matched a roster, in the order written.
func (e Event) ResolvedTeams() []TeamRef {
	var out []TeamRef
	for _, t := range e.Teams {
		if t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// SideName is what to call a side: the roster name when resolved, else as written.
func (e Event) SideName(i int) string {
	if e.Teams[i] != nil {
		return e.Teams[i].Name
	}
	return e.TeamsRaw[i]
}

// SortChannels orders channels by number, in place.
func (s *Snapshot) SortChannels() {
	slices.SortFunc(s.Channels, func(a, b Channel) int { return cmp.Compare(a.Number, b.Number) })
}

// SortedPrograms returns a channel's programmes ordered by start time.
func (c Channel) SortedProgrammes() []Programme {
	out := slices.Clone(c.Programmes)
	slices.SortFunc(out, func(a, b Programme) int { return a.Event.Start.Compare(b.Event.Start) })
	return out
}
