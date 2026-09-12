package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/titleparse"
)

func TestWomensGamesGetTheirOwnSeries(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := cat.League("ncaab")
	ny, _ := time.LoadLocation("America/New_York")
	ctx := titleparse.Context{Now: time.Date(2027, 1, 20, 12, 0, 0, 0, ny), Loc: ny, League: lg, Catalog: cat}

	men := eventFromTitle(lg, titleparse.Parse(ctx, "NCAAB 032 | NORTHWESTERN @ 17 ILLINOIS (M) | 1/26 3:00 PM | BTN"))
	women := eventFromTitle(lg, titleparse.Parse(ctx, "NCAAB 036 | NORTHWESTERN @ ILLINOIS (W) | 1/26 3:00 PM | B1G+"))
	men.ID, women.ID = episodeID(&men, ny), episodeID(&women, ny)

	if men.SeriesID != "191260" || !strings.HasPrefix(men.ID, "191260-") || men.Title != "College Basketball" {
		t.Errorf("men's game: %s %s %s", men.SeriesID, men.ID, men.Title)
	}
	if women.SeriesID != "191292" || !strings.HasPrefix(women.ID, "191292-") || women.Title != "College Women's Basketball" {
		t.Errorf("women's game: %s %s %s", women.SeriesID, women.ID, women.Title)
	}
	if eventKey(&men, ny) == eventKey(&women, ny) {
		t.Error("a men's and a women's game between the same schools on one day must be distinct events")
	}
	if women.Teams[1] == nil || men.Teams[1] == nil || women.Teams[1].TMSBrandID == men.Teams[1].TMSBrandID {
		t.Errorf("women's game should use the women's roster ids: %+v vs %+v", women.Teams, men.Teams)
	}
}

func TestIdentityKeepsSidesAlignedWhenOneIsUnresolved(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := cat.League("nfl")
	ny, _ := time.LoadLocation("America/New_York")
	ctx := titleparse.Context{Now: time.Date(2026, 9, 1, 12, 0, 0, 0, ny), Loc: ny, League: lg, Catalog: cat}

	// Two games on one day against the same known home team, each with an away team the
	// roster does not know. They are different games and must stay different.
	a := eventFromTitle(lg, titleparse.Parse(ctx, "NFL 04: Unknown Provider Name vs Houston Texans (09.13 1:00PM ET)"))
	b := eventFromTitle(lg, titleparse.Parse(ctx, "NFL 05: Some Other Mystery vs Houston Texans (09.13 4:30PM ET)"))
	if a.Teams[0] != nil || a.Teams[1] == nil || a.Teams[1].Name != "Houston Texans" {
		t.Fatalf("sides misaligned: %+v raw %v", a.Teams, a.TeamsRaw)
	}
	if eventKey(&a, ny) == eventKey(&b, ny) {
		t.Errorf("distinct games collapsed into one key %q", eventKey(&a, ny))
	}
	if a.SubTitle != "Unknown Provider Name vs Houston Texans" || a.SideName(0) != "Unknown Provider Name" {
		t.Errorf("subtitle/side names wrong: %q %q", a.SubTitle, a.SideName(0))
	}
	// Orientation does not matter: "B @ A" is the same game as "A vs B".
	c := eventFromTitle(lg, titleparse.Parse(ctx, "NFL 06: Houston Texans @ Unknown Provider Name (09.13 1:00PM ET)"))
	if eventKey(&a, ny) != eventKey(&c, ny) {
		t.Errorf("same game with sides swapped got different keys: %q vs %q", eventKey(&a, ny), eventKey(&c, ny))
	}
}
