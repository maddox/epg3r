package m3u

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/model"
)

func TestWriteRoundTrip(t *testing.T) {
	kick := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	snap := &model.Snapshot{Channels: []model.Channel{
		{ID: "NFL 06", Number: 8506, Name: "NFL 06", Kind: model.KindPlaceholder, LeagueKey: "nfl", LogoURL: "/art/league/nfl.png", StreamURL: "http://x/2"},
		{ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", LogoURL: "http://logo/nfl.png", StreamURL: "http://x/1",
			Programmes: []model.Programme{{Event: model.Event{Title: "NFL Football", SubTitle: `Bills "vs" Texans`, Start: kick, Stop: kick.Add(3 * time.Hour)}}}},
	}}
	snap.SortChannels()
	var buf bytes.Buffer
	if err := Write(&buf, snap, WriteOptions{GuideTags: true, Now: kick.Add(-time.Hour), BaseURL: "http://epg3r.test"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "#EXTM3U\n") {
		t.Error("missing header")
	}
	if strings.Index(out, "NFL 04") > strings.Index(out, "NFL 06") {
		t.Error("channels should be sorted by number")
	}
	for _, must := range []string{
		`channel-id="NFL 04"`, `tvg-id="NFL 04"`, `channel-number="8504"`, `group-title="NFL"`,
		`tvg-logo="http://logo/nfl.png"`,                  // the user's own URL, untouched
		`tvg-logo="http://epg3r.test/art/league/nfl.png"`, // a path, addressed under the base
		`tvc-guide-title="NFL Football"`, `tvc-guide-description="Bills 'vs' Texans"`, `tvc-guide-categories="Sports event"`,
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %s in\n%s", must, out)
		}
	}
	if strings.Contains(out, `tvc-guide-title="" `) || strings.Count(out, "tvc-guide-title") != 1 {
		t.Error("idle channel should have no guide tags")
	}

	entries, err := Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Attr("channel-id") != "NFL 04" || entries[0].URL != "http://x/1" || entries[0].Title != "NFL 04" {
		t.Errorf("round trip: %+v", entries)
	}
}
