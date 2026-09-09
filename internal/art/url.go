// Package art draws the pictures the guide points at: a mark for every channel and a
// placard for every airing. Real team crests are fetched from the source the catalog
// addresses and cached on disk; anything without one is drawn from its name, which is the
// common case rather than the rare one — most of the college rosters have no crest to
// fetch, and a lettermark is a designed answer rather than a placeholder.
package art

import "strings"

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

// A placard is airing art: 4:3, the shape a guide gives a programme. Both kinds are
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
