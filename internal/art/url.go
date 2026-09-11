// Package art draws the pictures the guide points at: a mark for every channel and a
// placard for every airing. Real team crests are fetched from the source the catalog
// addresses and cached on disk; anything without one is drawn from its name, which is the
// common case rather than the rare one — most of the college rosters have no crest to
// fetch, and a lettermark is a designed answer rather than a placeholder.
package art

import (
	"cmp"
	"strings"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
)

// Version is bumped by hand whenever what the compositor draws changes. It leads every URL
// because plenty of things cache a picture by the URL it came from and never ask again, so
// changing what we draw has to change where it lives.
const Version = "1"

// Prefix is where the art routes live.
const Prefix = "/art"

// A logo identifies a channel. It is the mark itself on transparency, drawn at no
// particular shape and composed with nothing, because it is shown small beside a channel
// name and whatever is behind it there is not ours.

// LeagueLogoPath is the logo for a channel that is not any one team, which is every slot
// channel in a league.
func LeagueLogoPath(leagueKey string) string { return join("logo", leagueKey) }

// TeamLogoPath is the logo for a channel dedicated to one team.
func TeamLogoPath(leagueKey, teamKey string) string { return join("logo", leagueKey, teamKey) }

// A placard is airing art: 4:3, the shape a guide gives a program. Both kinds are
// placards, the way both kinds of logo are logos — the leading segment says what a picture
// is for, and what is under it says which one.

// LeaguePlacardPath is the art for an airing whose teams did not both resolve.
func LeaguePlacardPath(leagueKey string) string { return join("placard", leagueKey) }

// MatchupPlacardPath is the art for one game. The order is the order the airing reads, home
// last, and is never sorted: "Bills at Texans" and "Texans at Bills" are different games and
// have to look different.
func MatchupPlacardPath(leagueKey, away, home string) string {
	return join("placard", leagueKey, away, home)
}

func join(parts ...string) string {
	return Prefix + "/" + Version + "/" + strings.Join(parts, "/") + ".png"
}

// safeKey reports whether a path segment is one of ours. Keys come from the catalog, which
// slugs them, so anything else is a request we did not generate and names nothing.
func safeKey(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// ForChannel is a channel's logo, and with ForAiring below it is the whole of what the guide
// points at. Both live here, beside the paths they build, so the rule is in one place rather
// than in both writers, and both are pure, so the pipeline calls them with no art service in
// existence at all.
//
// A team channel wears its team's mark; anything else wears the league's. A league logo set
// by hand replaces the league's mark, not its teams' — one URL should not flatten every team
// channel in the league to the same picture.
func ForChannel(lg *catalog.League, ch *model.Channel) string {
	if ch.Kind == model.KindTeam && ch.Team != nil {
		return TeamLogoPath(lg.Key, ch.Team.Key)
	}
	return cmp.Or(lg.Logo, LeagueLogoPath(lg.Key))
}

// ForAiring is an airing's art: the matchup when both sides resolved, the league otherwise.
// A placard set by hand replaces the league's, not a matchup — otherwise pasting one URL
// would quietly turn off the thing this is all for.
//
// Both sides are tested by index rather than through ResolvedTeams, which compacts and so
// loses which side is which. A placard has a left and a right.
func ForAiring(lg *catalog.League, ev *model.Event) string {
	// The sides are held in the order the game's name uses: away first once a source has
	// said who is home, otherwise whatever order the title gave. Reading them straight is
	// what keeps the picture and the name from ever disagreeing.
	if ev.Teams[0] != nil && ev.Teams[1] != nil {
		return MatchupPlacardPath(lg.Key, ev.Teams[0].Key, ev.Teams[1].Key)
	}
	return LeagueArt(lg)
}

// LeagueArt is a league's own picture: what an airing falls back to, and what a channel
// shows for itself where something wants a picture and has no airing to hang it on.
func LeagueArt(lg *catalog.League) string {
	return cmp.Or(lg.Placard, LeaguePlacardPath(lg.Key))
}
