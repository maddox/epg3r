package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

func TestLeagueOverrides(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	title := "Pro Football"
	if err := s.SetLeagueOverride(ctx, "nfl", catalog.Override{AiringTitle: &title}); err != nil {
		t.Fatal(err)
	}
	bad := "soon"
	var verr *ValidationError
	if err := s.SetLeagueOverride(ctx, "nfl", catalog.Override{Duration: &bad}); !errors.As(err, &verr) {
		t.Errorf("bad duration should be a ValidationError, got %v", err)
	}
	all, err := s.LeagueOverrides(ctx)
	if err != nil || len(all) != 1 || *all["nfl"].AiringTitle != "Pro Football" || all["nfl"].Duration != nil {
		t.Errorf("overrides: %+v %v", all, err)
	}
	if err := s.SetLeagueOverride(ctx, "nfl", catalog.Override{}); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.LeagueOverrides(ctx); len(all) != 0 {
		t.Errorf("empty override should delete the row: %v", all)
	}
}
