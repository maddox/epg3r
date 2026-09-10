package titleparse

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/m3u"
	"github.com/jonmaddox/epg3r/internal/model"
)

// TestCoverageOnRealPlaylist runs every entry of the real playlist through the parser
// and reports what it made of them. It fails when a slot channel yields nothing
// usable, which is the one outcome that means the parser needs work.
func TestCoverageOnRealPlaylist(t *testing.T) {
	f, err := os.Open("../../testdata/m3u/all-sports.m3u")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := m3u.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, ny)

	counts := map[string]int{}
	var unmatchedLeague, zeroConf, unmatchedTeams, networks []string
	for _, e := range entries {
		lg, ok := cat.MatchLeague(e.Group(), e.Title)
		if !ok {
			unmatchedLeague = append(unmatchedLeague, e.Group()+" :: "+e.Title)
			counts["no-league"]++
			continue
		}
		res := Parse(Context{Now: now, Loc: ny, League: lg, Catalog: cat}, e.Title)
		counts[string(res.Kind)]++
		switch res.Kind {
		case model.KindSlot:
			if res.Confidence <= 0 {
				zeroConf = append(zeroConf, e.Title)
			}
			if res.TeamARaw != "" && (res.TeamA == nil || res.TeamB == nil) {
				unmatchedTeams = append(unmatchedTeams, fmt.Sprintf("%s -> [%s] [%s]", e.Title, res.TeamARaw, res.TeamBRaw))
			}
			if res.TeamARaw != "" && res.TeamA != nil && res.TeamB != nil {
				counts["slot-both-teams"]++
			}
			if res.TimeKnown {
				counts["slot-with-time"]++
			}
		case model.KindNetwork:
			networks = append(networks, e.Title)
		}
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-16s %d", k, counts[k])
	}
	logSome := func(label string, items []string) {
		sort.Strings(items)
		t.Logf("%s (%d):", label, len(items))
		for i, s := range items {
			if i >= 40 {
				t.Logf("  ... %d more", len(items)-i)
				break
			}
			t.Logf("  %s", s)
		}
	}
	logSome("slot titles with unmatched teams", unmatchedTeams)
	logSome("network/other channels", networks)
	logSome("entries with no league", unmatchedLeague)

	if len(zeroConf) > 0 {
		logSome("slot channels with zero confidence", zeroConf)
		t.Errorf("%d slot channels produced nothing usable", len(zeroConf))
	}
	if counts["no-league"] > 0 {
		// Only G League slots are expected to fall through today.
		for _, s := range unmatchedLeague {
			if !strings.Contains(s, "NBAG") {
				t.Errorf("unexpected entry with no league: %s", s)
			}
		}
	}
}
