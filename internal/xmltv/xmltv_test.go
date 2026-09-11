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
	for _, ps := range g.Programs {
		total += len(ps)
	}
	if total != 6690 || len(g.Programs) != 152 {
		t.Errorf("programs = %d on %d channels, want 6690 on 152", total, len(g.Programs))
	}
	bills := g.Programs["US NFL Buffalo Bills (HD)"]
	if len(bills) == 0 {
		t.Fatal("no Bills programs")
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
		t.Error("expected the Bills 'Next game' filler program")
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
		TimeKnown: true, Genre: "Football", Categories: []string{"Sports event", "Sports"}, PlacardURL: "/art/matchup/nfl/buffalo-bills/houston-texans.png",
		Confidence: 0.95, Source: model.OriginTitle,
	}
	return &model.Snapshot{
		RunID: 1, GeneratedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Channels: []model.Channel{
			{ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", LogoURL: "/art/league/nfl.png", StreamURL: "http://example.invalid/stream/1", Programs: []model.Program{{Event: ev}}},
			{ID: "NFL 06", Number: 8506, Name: "NFL 06", Kind: model.KindPlaceholder, LeagueKey: "nfl", LogoURL: "/art/league/nfl.png", StreamURL: "http://example.invalid/stream/2"},
			{ID: "NFL Bills", Number: 8553, Name: "NFL Bills", Kind: model.KindTeam, LeagueKey: "nfl", Team: &bills, LogoURL: "/art/team/nfl/buffalo-bills.png", StreamURL: "http://example.invalid/stream/3",
				Programs: []model.Program{{Event: ev, Note: "Buffalo Bills broadcast"}}},
		},
	}
}

func TestWriteGolden(t *testing.T) {
	var buf bytes.Buffer
	snap := sampleSnapshot()
	snap.SortChannels()
	if err := Write(&buf, snap, WriteOptions{Generator: "epg3r test", BaseURL: "http://epg3r.test"}); err != nil {
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
		// The "episode/" prefix is what makes Channels DVR use this id instead of building one
		// from the series id and the matchup text. Losing it is a silent regression: the guide
		// still validates, and recordings quietly start keying on wording.
		`<episode-num system="epg3r">episode/191277-abc123def456</episode-num>`,
		`<team-id system="tms">34</team-id>`,
		`<team-id system="tms">43</team-id>`,
		`<icon src="http://epg3r.test/art/team/nfl/buffalo-bills.png"></icon>`,
		`<icon src="http://epg3r.test/art/matchup/nfl/buffalo-bills/houston-texans.png"></icon>`,
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
	if len(g.Channels) != 3 || len(g.Programs["NFL 04"]) != 1 {
		t.Errorf("round trip: %d channels, %d programs on NFL 04", len(g.Channels), len(g.Programs["NFL 04"]))
	}
}

// Art is addressed by a path everywhere it is stored, so the base URL is the only place a
// host enters the guide. A reference the user supplied already names its own host and has
// to survive that untouched.
func TestWriteBaseURL(t *testing.T) {
	snap := &model.Snapshot{Channels: []model.Channel{
		{ID: "A", Name: "A", LeagueKey: "nfl", LogoURL: "/art/league/nfl.png", StreamURL: "http://x/1",
			Programs: []model.Program{{Event: model.Event{Title: "NFL Football", SeriesID: "1", ID: "1-a",
				PlacardURL: "/art/matchup/nfl/a/b.png"}}}},
		{ID: "B", Name: "B", LeagueKey: "nfl", LogoURL: "https://elsewhere.invalid/mine.png", StreamURL: "http://x/2"},
	}}
	for _, base := range []string{"http://epg3r.test", ""} { // "": left alone, never guessed at
		var buf bytes.Buffer
		if err := Write(&buf, snap, WriteOptions{Generator: "t", BaseURL: base}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		for _, want := range []string{
			`<icon src="` + base + `/art/league/nfl.png"></icon>`,
			`<icon src="` + base + `/art/matchup/nfl/a/b.png"></icon>`,
			`<icon src="https://elsewhere.invalid/mine.png"></icon>`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("base %q: missing %s in\n%s", base, want, out)
			}
		}
	}
}
