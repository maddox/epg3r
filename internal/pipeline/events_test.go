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
	men.ID, women.ID = episodeID(&men), episodeID(&women)

	if men.SeriesID != "191260" || !strings.HasPrefix(men.ID, "191260-") || men.Title != "College Basketball" {
		t.Errorf("men's game: %s %s %s", men.SeriesID, men.ID, men.Title)
	}
	if women.SeriesID != "191292" || !strings.HasPrefix(women.ID, "191292-") || women.Title != "College Women's Basketball" {
		t.Errorf("women's game: %s %s %s", women.SeriesID, women.ID, women.Title)
	}
	if eventKey(&men) == eventKey(&women) {
		t.Error("a men's and a women's game between the same schools on one day must be distinct events")
	}
	if len(women.Teams) != 2 || women.Teams[1].TMSBrandID == men.Teams[1].TMSBrandID {
		t.Errorf("women's game should use the women's roster ids: %+v vs %+v", women.Teams, men.Teams)
	}
}
