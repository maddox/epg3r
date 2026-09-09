package web

import (
	"os"
	"regexp"
	"testing"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// The league chips in the UI and the grounds the generated art is drawn on are the same
// brand colours, but they live in two files that no build step connects: the catalog is Go
// data, the chips are Tailwind source. This is what keeps them from drifting apart.
//
// A chip that names no hex is opting out on purpose — NHL's brand colour is black, which
// works as an art ground and disappears at chip opacity — so only declared hexes are
// compared.
func TestChipColoursMatchTheCatalog(t *testing.T) {
	css, err := os.ReadFile("static/src/app.css")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]string{}
	for _, m := range chipRule.FindAllStringSubmatch(string(css), -1) {
		declared[m[1]] = m[2]
	}
	if len(declared) == 0 {
		t.Fatal("no chip rules found; the selector this test reads has changed")
	}
	for _, lg := range cat.Leagues {
		hex, ok := declared[lg.Key]
		if !ok {
			continue // no chip rule, or one that opts out of a hex
		}
		if hex != lg.Color {
			t.Errorf("league %s: chip is %s but the catalog says %s", lg.Key, hex, lg.Color)
		}
		delete(declared, lg.Key)
	}
	for key := range declared {
		t.Errorf("chip for %q, which is not a league", key)
	}
}

var chipRule = regexp.MustCompile(`\.chip\[data-league="([a-z]+)"\][^}]*bg-\[(#[0-9a-fA-F]{6})\]`)
