package catalog

import (
	"regexp"
	"testing"
	"time"
)

func load(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadAndValidate(t *testing.T) {
	c := load(t)
	if len(c.Leagues) != 8 {
		t.Fatalf("expected 8 leagues, got %d", len(c.Leagues))
	}
	nfl, ok := c.League("nfl")
	if !ok || nfl.SeriesID != "191277" || nfl.ChannelBase != 10000 || nfl.ChannelID(0, 3) != "NFL 03" || nfl.SlotChannelNumber(0, 3) != 10003 {
		t.Errorf("NFL values wrong: %+v", nfl)
	}
	if nfl.ChannelID(1, 3) != "NFL 03 B" || nfl.SlotChannelNumber(1, 3) != 10103 || nfl.TeamChannelBase() != 10800 || nfl.MaxFamilies() != 8 {
		t.Errorf("family layout wrong: %s %d %d %d", nfl.ChannelID(1, 3), nfl.SlotChannelNumber(1, 3), nfl.TeamChannelBase(), nfl.MaxFamilies())
	}
	ncaaf, _ := c.League("ncaaf")
	if ncaaf.SlotSpan != 200 || ncaaf.SlotChannelNumber(0, 136) != 16136 || ncaaf.SlotChannelNumber(1, 7) != 16207 || ncaaf.MaxFamilies() != 4 {
		t.Errorf("NCAA layout wrong: span %d, %d %d", ncaaf.SlotSpan, ncaaf.SlotChannelNumber(0, 136), ncaaf.SlotChannelNumber(1, 7))
	}
	if len(nfl.Categories) != 2 || nfl.Categories[0] != "Sports event" {
		t.Errorf("default categories missing: %v", nfl.Categories)
	}
	if nfl.Duration.Hours() != 3.5 {
		t.Errorf("duration = %s", nfl.Duration)
	}
	for _, roster := range []string{"NFL", "MLB", "NBA", "NHL", "MLS", "WNBA", "NCAA Football", "NCAA Basketball", "NCAA Womens Basketball"} {
		if _, ok := c.Roster(roster); !ok {
			t.Errorf("missing roster %q", roster)
		}
	}
	ncaab, _ := c.League("ncaab")
	if c.Teams(ncaab, true).Roster != "NCAA Womens Basketball" || c.Teams(ncaab, false).Roster != "NCAA Basketball" {
		t.Error("women's roster switch wrong")
	}
	if ncaab.SeriesIDFor(false) != "191260" || ncaab.SeriesIDFor(true) != "191292" || ncaab.AiringTitleFor(true) != "College Women's Basketball" {
		t.Errorf("women's series switch wrong: %s %s", ncaab.SeriesIDFor(true), ncaab.AiringTitleFor(true))
	}
	for key, want := range map[string]string{"nhl": "448880", "ncaaf": "191261", "wnba": "191289", "mlb": "191273"} {
		if lg, _ := c.League(key); lg.SeriesID != want {
			t.Errorf("%s series_id = %s, want %s", key, lg.SeriesID, want)
		}
	}
}

func TestMatchLeagueFromFixtureGroups(t *testing.T) {
	c := load(t)
	cases := map[string]string{
		"NFL":                 "nfl",
		"MLB Baseball league": "mlb",
		"MLS":                 "mls",
		"NBA":                 "nba",
		"NHL":                 "nhl",
		"WNBA League Pass":    "wnba",
		"NCAAF":               "ncaaf",
		"NCAA Basketball":     "ncaab",
	}
	for group, want := range cases {
		lg, ok := c.MatchLeague(group, "")
		if !ok || lg.Key != want {
			t.Errorf("group %q -> %v, want %s", group, lg, want)
		}
	}
	if lg, ok := c.MatchLeague("Movies", "Some Movie"); ok {
		t.Errorf("Movies matched %s", lg.Key)
	}
	// The NBA group carries G League slots; the exclude pattern keeps them out.
	if _, ok := c.MatchLeague("NBA", "NBAG 04: Windy City Bulls vs Motor City Cruise"); ok {
		t.Error("G League title should be excluded from NBA")
	}
}

func TestLabelAndTokenPatterns(t *testing.T) {
	c := load(t)
	nhl, _ := c.League("nhl")
	re := regexp.MustCompile(`(?i)^` + nhl.LabelPattern() + `$`)
	for _, s := range []string{"NHL", "nhl", "NHL Game"} {
		if !re.MatchString(s) {
			t.Errorf("%q should be an NHL label", s)
		}
	}
	if re.MatchString("NHL Network") {
		t.Error("NHL Network is not a slot label")
	}
	mls, _ := c.League("mls")
	if !regexp.MustCompile(`(?i)^` + mls.LabelPattern() + `$`).MatchString("(Apple) (MLS)") {
		t.Error("(Apple) (MLS) should be an MLS label alias")
	}
	tok := regexp.MustCompile(`(?i)^` + c.TokenPattern() + `$`)
	for _, s := range []string{"NFL", "NBALP", "NHL Game", "wnba"} {
		if !tok.MatchString(s) {
			t.Errorf("%q should be a league token", s)
		}
	}
	if tok.MatchString("ESPN") {
		t.Error("ESPN is not a league token")
	}
}

func TestTeamMatching(t *testing.T) {
	c := load(t)
	type tc struct {
		roster, in, want string
		method           MatchMethod
	}
	cases := []tc{
		{"NFL", "Green Bay Packers", "Green Bay Packers", MatchExact},
		{"NFL", "Packers", "Green Bay Packers", MatchExact},
		{"NFL", "GB", "Green Bay Packers", MatchExact},
		{"NFL", "LA Chargers", "Los Angeles Chargers", MatchExact},
		{"NFL", "NY Giants", "New York Giants", MatchExact},
		{"NFL", "San Francisco 49ers", "San Francisco 49ers", MatchExact},
		{"NFL", "49ers", "San Francisco 49ers", MatchExact},
		{"NFL", "Washington", "Washington Commanders", MatchExact},
		{"MLB", "D backs", "Arizona Diamondbacks", MatchExact},
		{"MLB", "Red Sox", "Boston Red Sox", MatchExact},
		{"MLB", "Athletics", "Athletics", MatchExact},
		{"MLB", "St. Louis Cardinals", "St. Louis Cardinals", MatchExact},
		{"MLB", "Cardinals", "St. Louis Cardinals", MatchExact},
		{"NBA", "LA Lakers", "Los Angeles Lakers", MatchExact},
		{"NBA", "Trail Blazers", "Portland Trail Blazers", MatchExact},
		{"NBA", "LA Clippers", "LA Clippers", MatchExact},
		{"NBA", "Timberwolves", "Minnesota Timberwolves", MatchExact},
		{"NBA", "worriors", "Golden State Warriors", MatchFuzzy},
		{"NHL", "Edmonton", "Edmonton Oilers", MatchExact},
		{"NHL", "Golden Knights", "Vegas Golden Knights", MatchExact},
		{"MLS", "D C United", "D.C. United", MatchExact},
		{"MLS", "LAFC", "LAFC", MatchExact},
		{"MLS", "Columbus Crew", "Columbus Crew", MatchExact},
		{"WNBA", "Connecticut Sun", "Connecticut Sun", MatchExact},
		{"NCAA Football", "WEST GEORGIA", "West Georgia", MatchExact},
		{"NCAA Football", "KENNESAW STATE", "Kennesaw State", MatchExact},
		{"NCAA Football", "SC STATE", "South Carolina State", MatchExact},
		{"NCAA Football", "UAPB", "Arkansas-Pine Bluff", MatchExact},
		{"NCAA Football", "ILLINOIS", "Illinois", MatchExact},
		{"NCAA Football", "PRAIRIE VIEW A&M", "Prairie View A&M", MatchExact},
		{"NCAA Basketball", "UIC", "Illinois-Chicago", MatchExact},
		{"NCAA Basketball", "UCONN", "UConn", MatchExact},
	}
	for _, tc := range cases {
		ti, ok := c.Roster(tc.roster)
		if !ok {
			t.Fatalf("no roster %s", tc.roster)
		}
		got, method, score := ti.Match(tc.in)
		if got == nil {
			t.Errorf("%s: %q -> no match", tc.roster, tc.in)
			continue
		}
		if got.Name != tc.want || method != tc.method {
			t.Errorf("%s: %q -> %q (%s %.2f), want %q (%s)", tc.roster, tc.in, got.Name, method, score, tc.want, tc.method)
		}
		if got.TMSBrandID == "" {
			t.Errorf("%s: %q has no Gracenote id", tc.roster, got.Name)
		}
	}
}

func TestTeamMatchingAmbiguousAndUnknown(t *testing.T) {
	c := load(t)
	mlb, _ := c.Roster("MLB")
	if got, _, _ := mlb.Match("Sox"); got != nil {
		t.Errorf("Sox is ambiguous, got %s", got.Name)
	}
	nfl, _ := c.Roster("NFL")
	if got, _, _ := nfl.Match("New York"); got != nil {
		t.Errorf("New York is ambiguous in the NFL, got %s", got.Name)
	}
	if got, _, _ := nfl.Match("Windy City Bulls"); got != nil {
		t.Errorf("G League team should not match an NFL team, got %s", got.Name)
	}
	if got, _, _ := nfl.Match(""); got != nil {
		t.Error("empty name matched")
	}
}

func TestGracenoteIDsUseBrand(t *testing.T) {
	c := load(t)
	nfl, _ := c.Roster("NFL")
	got, _, _ := nfl.Match("Los Angeles Chargers")
	if got == nil || got.TMSBrandID != "14215" || got.TMSTeamID != "56" {
		t.Errorf("Chargers ids wrong: %+v", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"St. Louis Cardinals": "st louis cardinals",
		"Texas A&M":           "texas a and m",
		"Club América":        "club america",
		"Oakland A's":         "oakland as",
		"  D.C.  United ":     "d c united",
		"Hawai'i":             "hawaii",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortName(t *testing.T) {
	c := load(t)
	cases := map[[2]string]string{
		{"NFL", "Buffalo Bills"}:            "Bills",
		{"NBA", "Portland Trail Blazers"}:   "Blazers",
		{"MLB", "Boston Red Sox"}:           "Red Sox",
		{"MLB", "Chicago White Sox"}:        "White Sox",
		{"NFL", "New York Giants"}:          "Giants",
		{"NCAA Football", "Kennesaw State"}: "Kennesaw State",
	}
	for k, want := range cases {
		ti, _ := c.Roster(k[0])
		team, _, _ := ti.Match(k[1])
		if team == nil {
			t.Fatalf("%v not found", k)
		}
		if got := ti.ShortName(team); got != want {
			t.Errorf("ShortName(%s) = %q, want %q", k[1], got, want)
		}
	}
}

func TestParseRejectsBadManifests(t *testing.T) {
	bad := map[string]string{
		"duplicate series id": `
leagues:
  - {key: a, name: A, airing_title: A, duration: 1h, series_id: "1", channel_base: 1000, label_prefix: A}
  - {key: b, name: B, airing_title: B, duration: 1h, series_id: "1", channel_base: 2000, label_prefix: B}
`,
		"overlapping blocks": `
leagues:
  - {key: a, name: A, airing_title: A, duration: 1h, series_id: "1", channel_base: 1000, label_prefix: A}
  - {key: b, name: B, airing_title: B, duration: 1h, series_id: "2", channel_base: 1500, label_prefix: B}
`,
		"missing roster": `
leagues:
  - {key: a, name: A, airing_title: A, duration: 1h, series_id: "1", channel_base: 1000, label_prefix: A, roster: Nope}
`,
		"team without id": `
leagues: []
rosters:
  - roster: X
    teams: [{name: Someone}]
`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWithOverrides(t *testing.T) {
	c := load(t)
	title, dur := "Pro Football", "4h"
	eff := c.WithOverrides(map[string]Override{"nfl": {AiringTitle: &title, Duration: &dur}})
	nfl, _ := eff.League("nfl")
	if nfl.AiringTitle != "Pro Football" || nfl.Duration != 4*time.Hour || nfl.SeriesID != "191277" {
		t.Errorf("override not applied: %+v", nfl)
	}
	if orig, _ := c.League("nfl"); orig.AiringTitle != "NFL Football" {
		t.Error("the base catalog must be untouched")
	}
	if _, ok := eff.MatchLeague("NFL", "NFL 04: A vs B"); !ok {
		t.Error("an override must not stop a league matching")
	}
	if eff.Teams(nfl, false) == nil {
		t.Error("rosters must be shared")
	}
	if c.WithOverrides(nil) != c {
		t.Error("no overrides should return the same catalog")
	}
}

func TestOverrideValidate(t *testing.T) {
	good, bad, zero := "3h30m", "soon", "0s"
	if err := (Override{Duration: &good, StartPad: &zero}).Validate(); err != nil {
		t.Errorf("valid override rejected: %v", err)
	}
	if err := (Override{Duration: &bad}).Validate(); err == nil {
		t.Error("bad duration accepted")
	}
	if err := (Override{Duration: &zero}).Validate(); err == nil {
		t.Error("zero duration accepted")
	}
}
