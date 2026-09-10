package titleparse

import (
	"os"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"gopkg.in/yaml.v3"
)

type fixtureFile struct {
	Now      time.Time     `yaml:"now"`
	Timezone string        `yaml:"timezone"`
	Cases    []fixtureCase `yaml:"cases"`
}

type fixtureCase struct {
	Title  string `yaml:"title"`
	Group  string `yaml:"group"`
	Expect struct {
		Kind        string     `yaml:"kind"`
		Slot        *int       `yaml:"slot"`
		TeamA       string     `yaml:"team_a"`
		TeamB       string     `yaml:"team_b"`
		TeamARaw    string     `yaml:"team_a_raw"`
		TeamBRaw    string     `yaml:"team_b_raw"`
		Team        string     `yaml:"team"`
		Sep         string     `yaml:"sep"`
		EventTitle  string     `yaml:"event_title"`
		Kickoff     *time.Time `yaml:"kickoff"`
		Stop        *time.Time `yaml:"stop"`
		TimeKnown   *bool      `yaml:"time_known"`
		DateAssumed *bool      `yaml:"date_assumed"`
		Network     string     `yaml:"network"`
		Feed        string     `yaml:"feed"`
		Womens      *bool      `yaml:"womens"`
	} `yaml:"expect"`
}

func loadFixtures(t *testing.T) (fixtureFile, *catalog.Catalog, *time.Location) {
	t.Helper()
	body, err := os.ReadFile("testdata/titles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var ff fixtureFile
	if err := yaml.Unmarshal(body, &ff); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(ff.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	return ff, cat, loc
}

func TestFixtures(t *testing.T) {
	ff, cat, loc := loadFixtures(t)
	for _, c := range ff.Cases {
		t.Run(c.Title, func(t *testing.T) {
			lg, ok := cat.MatchLeague(c.Group, c.Title)
			if !ok {
				t.Fatalf("group %q did not resolve to a league", c.Group)
			}
			res := Parse(Context{Now: ff.Now, Loc: loc, League: lg, Catalog: cat}, c.Title)
			e := c.Expect

			if string(res.Kind) != e.Kind {
				t.Errorf("kind = %s, want %s (normalized %q)", res.Kind, e.Kind, res.Normalized)
			}
			if e.Slot != nil && res.Slot != *e.Slot {
				t.Errorf("slot = %d, want %d", res.Slot, *e.Slot)
			}
			checkTeam := func(label, want string, got *catalog.Team, raw string) {
				if want == "" {
					return
				}
				if got == nil {
					t.Errorf("%s = nil (raw %q), want %q", label, raw, want)
				} else if got.Name != want {
					t.Errorf("%s = %q (raw %q), want %q", label, got.Name, raw, want)
				}
			}
			checkTeam("team_a", e.TeamA, res.TeamA, res.TeamARaw)
			checkTeam("team_b", e.TeamB, res.TeamB, res.TeamBRaw)
			checkTeam("team", e.Team, res.Team, res.EventTitle)
			if e.TeamARaw != "" && res.TeamARaw != e.TeamARaw {
				t.Errorf("team_a_raw = %q, want %q", res.TeamARaw, e.TeamARaw)
			}
			if e.TeamBRaw != "" && res.TeamBRaw != e.TeamBRaw {
				t.Errorf("team_b_raw = %q, want %q", res.TeamBRaw, e.TeamBRaw)
			}
			if e.Sep != "" && res.Sep != e.Sep {
				t.Errorf("sep = %q, want %q", res.Sep, e.Sep)
			}
			if e.EventTitle != "" && res.EventTitle != e.EventTitle {
				t.Errorf("event_title = %q, want %q", res.EventTitle, e.EventTitle)
			}
			if e.Kickoff != nil && !res.Kickoff.Equal(*e.Kickoff) {
				t.Errorf("kickoff = %s, want %s", res.Kickoff, e.Kickoff)
			}
			if e.Stop != nil && !res.Stop.Equal(*e.Stop) {
				t.Errorf("stop = %s, want %s", res.Stop, e.Stop)
			}
			if e.TimeKnown != nil && res.TimeKnown != *e.TimeKnown {
				t.Errorf("time_known = %v, want %v", res.TimeKnown, *e.TimeKnown)
			}
			if e.DateAssumed != nil && res.DateAssumed != *e.DateAssumed {
				t.Errorf("date_assumed = %v, want %v", res.DateAssumed, *e.DateAssumed)
			}
			if e.Network != "" && res.Network != e.Network {
				t.Errorf("network = %q, want %q", res.Network, e.Network)
			}
			if e.Feed != "" && res.Feed != e.Feed {
				t.Errorf("feed = %q, want %q", res.Feed, e.Feed)
			}
			if e.Womens != nil && res.Womens != *e.Womens {
				t.Errorf("womens = %v, want %v", res.Womens, *e.Womens)
			}
			if res.Kind == model.KindSlot && res.Confidence <= 0 {
				t.Errorf("slot channel with zero confidence: %+v", res)
			}
		})
	}
}

func TestScheduleParsing(t *testing.T) {
	cases := map[string]Schedule{
		"09.08 1:00 PM ET":         {Month: 9, Day: 8, Hour: 13, HasDate: true, HasTime: true, TZ: "ET"},
		"Aug 28 08:00 PM":          {Month: 8, Day: 28, Hour: 20, HasDate: true, HasTime: true},
		"Sunday 09/13 1:00 PM ET":  {Month: 9, Day: 13, Hour: 13, HasDate: true, HasTime: true, TZ: "ET"},
		"Tue 01 Sep 20:05":         {Month: 9, Day: 1, Hour: 20, Minute: 5, HasDate: true, HasTime: true},
		"Fri 18th Sep 10:00 PM ET": {Month: 9, Day: 18, Hour: 22, HasDate: true, HasTime: true, TZ: "ET"},
		"Sep 2nd Tue 7:00 PM ET":   {Month: 9, Day: 2, Hour: 19, HasDate: true, HasTime: true, TZ: "ET"},
		"Sun Apr 28th 10:30 PM":    {Month: 4, Day: 28, Hour: 22, Minute: 30, HasDate: true, HasTime: true},
		"2026 09 09 18:25:00":      {Year: 2026, Month: 9, Day: 9, Hour: 18, Minute: 25, HasDate: true, HasTime: true},
		"8:05 PM ET":               {Hour: 20, Minute: 5, HasTime: true, TZ: "ET"},
		"1/26 3:00 PM":             {Month: 1, Day: 26, Hour: 15, HasDate: true, HasTime: true},
		"12:30 AM ET":              {Hour: 0, Minute: 30, HasTime: true, TZ: "ET"},
		"12:00 PM":                 {Hour: 12, HasTime: true},
	}
	for in, want := range cases {
		got, ok := parseSchedule(in)
		got.Stop = nil
		if !ok {
			t.Errorf("%q: not recognized", in)
			continue
		}
		if got != want {
			t.Errorf("%q: got %+v, want %+v", in, got, want)
		}
	}
	for _, in := range []string{"Home Stream", "ESPN+", "FOX", "ABC ESPN ESPN DEPORTES", "Columbus Crew", ""} {
		if _, ok := parseSchedule(in); ok {
			t.Errorf("%q should not parse as a schedule", in)
		}
	}
}

func TestResolveYearAndZone(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 12, 28, 9, 0, 0, 0, ny)

	// A January game seen in late December is next year.
	k, assumed := Schedule{Month: 1, Day: 4, Hour: 16, Minute: 25, HasDate: true, HasTime: true}.Resolve(now, ny)
	if assumed || !k.Equal(time.Date(2027, 1, 4, 16, 25, 0, 0, ny)) {
		t.Errorf("got %s assumed=%v", k, assumed)
	}
	// A title timezone overrides the source zone, and EST year-round means New York.
	la, _ := time.LoadLocation("America/Los_Angeles")
	k, _ = Schedule{Month: 7, Day: 4, Hour: 20, HasDate: true, HasTime: true, TZ: "EST"}.Resolve(now, la)
	if k.UTC().Hour() != 0 { // 8 PM EDT = 00:00 UTC
		t.Errorf("EST during summer should still resolve as New York: %s", k.UTC())
	}
	// No date means today in the source zone.
	k, assumed = Schedule{Hour: 20, Minute: 5, HasTime: true}.Resolve(now, ny)
	if !assumed || k.Day() != 28 || k.Hour() != 20 {
		t.Errorf("got %s assumed=%v", k, assumed)
	}
}
