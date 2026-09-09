package catalog

import (
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
