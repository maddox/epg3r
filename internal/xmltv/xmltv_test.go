package xmltv

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/model"
)

func TestReadRealGuide(t *testing.T) {
	f, err := os.Open("../../testdata/xmltv/all-sports.xmltv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Channels) != 1334 {
		t.Errorf("channels = %d, want 1334", len(g.Channels))
	}
	total := 0
	for _, ps := range g.Programmes {
		total += len(ps)
	}
	if total != 6690 || len(g.Programmes) != 152 {
		t.Errorf("programmes = %d on %d channels, want 6690 on 152", total, len(g.Programmes))
	}
	bills := g.Programmes["US NFL Buffalo Bills (HD)"]
	if len(bills) == 0 {
		t.Fatal("no Bills programmes")
	}
	var found bool
	for _, p := range bills {
		if strings.HasPrefix(p.Title, "Next game: Buffalo Bills at Houston Texans") {
			found = true
			if p.Start.Location() != time.UTC && p.Start.UTC().IsZero() {
				t.Errorf("bad start %v", p.Start)
			}
			if len(p.Categories) != 1 || p.Categories[0] != "Sports" {
				t.Errorf("categories = %v", p.Categories)
			}
		}
	}
	if !found {
		t.Error("expected the Bills 'Next game' filler programme")
	}
}

func TestParseStamp(t *testing.T) {
	got, err := parseStamp("20260901110000 +0000")
	if err != nil || !got.Equal(time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v %v", got, err)
	}
	got, err = parseStamp("20260901130000 -0400")
	if err != nil || !got.Equal(time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v %v", got, err)
	}
	if _, err := parseStamp("yesterday"); err == nil {
		t.Error("expected error")
	}
}

func sampleSnapshot() *model.Snapshot {
	ny, _ := time.LoadLocation("America/New_York")
	kick := time.Date(2026, 9, 13, 13, 0, 0, 0, ny)
	bills := model.TeamRef{LeagueKey: "nfl", Key: "buffalo-bills", Name: "Buffalo Bills", Abbr: "BUF", TMSBrandID: "34"}
	texans := model.TeamRef{LeagueKey: "nfl", Key: "houston-texans", Name: "Houston Texans", Abbr: "HOU", TMSBrandID: "43"}
	ev := model.Event{
		ID: "191277-abc123def456", SeriesID: "191277", LeagueKey: "nfl", Title: "NFL Football", SubTitle: "Buffalo Bills vs Houston Texans",
		Teams: [2]*model.TeamRef{&bills, &texans}, Kickoff: kick, Start: kick.Add(-15 * time.Minute).UTC(), Stop: kick.Add(210 * time.Minute).UTC(),
		TimeKnown: true, Genre: "Football", Categories: []string{"Sports event", "Sports"}, PlacardURL: "http://epg3r.test/art/league/nfl.png",
		Confidence: 0.95, Source: model.OriginTitle,
	}
	return &model.Snapshot{
		RunID: 1, GeneratedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Channels: []model.Channel{
			{ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://example.invalid/stream/1", Programmes: []model.Programme{{Event: ev}}},
			{ID: "NFL 06", Number: 8506, Name: "NFL 06", Kind: model.KindPlaceholder, LeagueKey: "nfl", StreamURL: "http://example.invalid/stream/2"},
			{ID: "NFL Bills", Number: 8553, Name: "NFL Bills", Kind: model.KindTeam, LeagueKey: "nfl", Team: &bills, StreamURL: "http://example.invalid/stream/3",
				Programmes: []model.Programme{{Event: ev, Note: "Buffalo Bills broadcast"}}},
		},
	}
}

func TestWriteGolden(t *testing.T) {
	var buf bytes.Buffer
	snap := sampleSnapshot()
	snap.SortChannels()
	if err := Write(&buf, snap, "epg3r test"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	const golden = "testdata/sample.xml"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(golden, buf.Bytes(), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("no golden file; run with UPDATE_GOLDEN=1: %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from golden %s\n--- got ---\n%s", golden, got)
	}

	// Structural checks that matter to Channels DVR regardless of formatting.
	for _, must := range []string{
		`<!DOCTYPE tv SYSTEM "xmltv.dtd">`,
		`<channel id="NFL 04">`,
		`<programme start="20260913164500 +0000" stop="20260913203000 +0000" channel="NFL 04">`,
		`<series-id>191277</series-id>`,
		`<episode-num system="epg3r">191277-abc123def456</episode-num>`,
		`<team-id system="tms">34</team-id>`,
		`<team-id system="tms">43</team-id>`,
		`<category lang="en">Sports event</category>`,
		`<live></live>`,
		`channel="NFL Bills">`,
		`(Buffalo Bills broadcast)`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("missing %s", must)
		}
	}
	if strings.Count(got, `191277-abc123def456`) != 2 {
		t.Error("the same game on two channels must share one episode id")
	}
	// Round trip through our own reader.
	g, err := Read(strings.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Channels) != 3 || len(g.Programmes["NFL 04"]) != 1 {
		t.Errorf("round trip: %d channels, %d programmes on NFL 04", len(g.Channels), len(g.Programmes["NFL 04"]))
	}
}
