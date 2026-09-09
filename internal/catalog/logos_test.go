package catalog

import (
	"regexp"
	"testing"
)

// Every shipped league grounds its art in its own brand colour rather than falling back to
// the neutral one, and says where its teams' marks live.
func TestEveryLeagueDescribesItsArt(t *testing.T) {
	c := load(t)
	for _, lg := range c.Leagues {
		if lg.Color == DefaultColor {
			t.Errorf("league %s has no colour of its own", lg.Key)
		}
		if _, _, _, ok := lg.RGB(); !ok {
			t.Errorf("league %s: colour %q does not parse", lg.Key, lg.Color)
		}
		if lg.LogoPath == "" {
			t.Errorf("league %s has no logo_path", lg.Key)
		}
	}
	nfl, _ := c.League("nfl")
	if r, g, b, _ := nfl.RGB(); r != 0x01 || g != 0x33 || b != 0x69 {
		t.Errorf("nfl RGB = %d %d %d", r, g, b)
	}
	if _, _, _, ok := (&League{Color: "#12345"}).RGB(); ok {
		t.Error("a short hex should not parse")
	}
	if _, _, _, ok := (&League{Color: "#gggggg"}).RGB(); ok {
		t.Error("a non-hex should not parse")
	}
}

// logo_id is written by `make logo-ids` and committed. These assert the shape the mark
// source addresses each league by, and a floor under how many resolved — so a generator run
// that silently matched nothing cannot be committed unnoticed.
func TestLogoIDs(t *testing.T) {
	c := load(t)
	shapeByPath := map[string]*regexp.Regexp{
		"nfl": reAbbr, "mlb": reAbbr, "nba": reAbbr, "nhl": reAbbr, "wnba": reAbbr,
		"soccer": reNumeric, "ncaa": reNumeric,
	}
	floors := map[string]int{
		"NFL": 32, "MLB": 30, "NBA": 30, "NHL": 32, "WNBA": 15, "MLS": 30,
		"NCAA Football": 300, "NCAA Basketball": 320, "NCAA Womens Basketball": 320,
	}
	counted := map[string]int{}
	for _, lg := range c.Leagues {
		shape := shapeByPath[lg.LogoPath]
		if shape == nil {
			t.Errorf("league %s: no expected id shape for logo_path %q", lg.Key, lg.LogoPath)
			continue
		}
		for _, ti := range []*TeamIndex{c.Teams(&lg, false), c.Teams(&lg, true)} {
			if ti == nil {
				continue
			}
			for _, team := range ti.Teams {
				if team.LogoID == "" {
					continue
				}
				counted[ti.Roster]++
				if !shape.MatchString(team.LogoID) {
					t.Errorf("%s: %s has logo_id %q, which is not how %s marks are addressed",
						ti.Roster, team.Name, team.LogoID, lg.LogoPath)
				}
			}
		}
	}
	for roster, floor := range floors {
		if n := counted[roster]; n < floor {
			t.Errorf("roster %s has %d logo ids, fewer than the %d expected; re-run make logo-ids", roster, n, floor)
		}
	}
}

var (
	reAbbr    = regexp.MustCompile(`^[a-z]+$`)
	reNumeric = regexp.MustCompile(`^[0-9]+$`)
)
