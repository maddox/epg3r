package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/store"
)

func lineupSnapshot() *model.Snapshot {
	now := time.Now().UTC()
	bills := model.TeamRef{LeagueKey: "nfl", Key: "buffalo-bills", Name: "Buffalo Bills", Abbr: "BUF", TMSBrandID: "34"}
	texans := model.TeamRef{LeagueKey: "nfl", Key: "houston-texans", Name: "Houston Texans", Abbr: "HOU"}
	bears := model.TeamRef{LeagueKey: "nfl", Key: "chicago-bears", Name: "Chicago Bears", Abbr: "CHI"}
	packers := model.TeamRef{LeagueKey: "nfl", Key: "green-bay-packers", Name: "Green Bay Packers", Abbr: "GB"}
	jets := model.TeamRef{LeagueKey: "nfl", Key: "new-york-jets", Name: "New York Jets", Abbr: "NYJ"}
	live := model.Event{ID: "191277-live", SeriesID: "191277", LeagueKey: "nfl", Title: "NFL Football", SubTitle: "Buffalo Bills vs Houston Texans",
		Teams: [2]*model.TeamRef{&bills, &texans},
		Start: now.Add(-time.Hour), Stop: now.Add(2 * time.Hour), Kickoff: now.Add(-time.Hour)}
	later := model.Event{ID: "191277-later", SeriesID: "191277", LeagueKey: "nfl", Title: "NFL Football", SubTitle: "Chicago Bears vs Green Bay Packers",
		Teams: [2]*model.TeamRef{&bears, &packers},
		Start: now.Add(4 * time.Hour), Stop: now.Add(7 * time.Hour), Kickoff: now.Add(4 * time.Hour)}
	// Beyond now and next: it is on the channel but never on screen.
	distant := model.Event{ID: "191277-distant", SeriesID: "191277", LeagueKey: "nfl", Title: "NFL Football", SubTitle: "Buffalo Bills vs New York Jets",
		Teams: [2]*model.TeamRef{&bills, &jets},
		Start: now.Add(72 * time.Hour), Stop: now.Add(75 * time.Hour), Kickoff: now.Add(72 * time.Hour)}
	return &model.Snapshot{RunID: 1, Channels: []model.Channel{
		{Key: "k-nfl-04", ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", SourceID: 1, StreamURL: "http://x/1", Programmes: []model.Programme{{Event: live}}},
		{Key: "k-nfl-05", ID: "NFL 05", Number: 8505, Name: "NFL 05", Kind: model.KindSlot, LeagueKey: "nfl", SourceID: 1, StreamURL: "http://x/2", Programmes: []model.Programme{{Event: later}}},
		{Key: "k-nfl-06", ID: "NFL 06", Number: 8506, Name: "NFL 06", Kind: model.KindPlaceholder, LeagueKey: "nfl", SourceID: 1, StreamURL: "http://x/3"},
		{Key: "k-nfl-bills", ID: "NFL Bills", Number: 10800, Name: "NFL Bills", Kind: model.KindTeam, LeagueKey: "nfl", SourceID: 1, Team: &bills, StreamURL: "http://x/4",
			Programmes: []model.Programme{{Event: live, Note: "Buffalo Bills broadcast"}, {Event: later}, {Event: distant}}},
		{Key: "k-nba-01", ID: "NBA 01", Number: 11501, Name: "NBA 01", Kind: model.KindPlaceholder, LeagueKey: "nba", SourceID: 1, StreamURL: "http://x/5"},
	}}
}

func TestLineupPageAndFilters(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()

	if body := do(h, http.MethodGet, "/lineup", nil, false).Body.String(); !strings.Contains(body, "No lineup yet") {
		t.Error("empty state missing")
	}
	st.CreateSource(context.Background(), store.NewSource{Name: "Provider", URL: "http://p/1"})
	s.Snapshots.Set(lineupSnapshot())

	body := do(h, http.MethodGet, "/lineup", nil, false).Body.String()
	// The guide is one list. Nothing in the app takes a channel out of it, so nothing
	// here offers to.
	if !strings.Contains(body, "5</span> channels in the guide") {
		t.Errorf("the lineup should count the whole guide: %s", body[:min(900, len(body))])
	}
	for _, gone := range []string{"Exclude", "Include", "/lineup/rule", "Export", "switched off"} {
		if strings.Contains(body, gone) {
			t.Errorf("export management still present: %q", gone)
		}
	}
	for _, want := range []string{"<th>Channel Type</th>", ">Event<", ">Team<", ">Unused<", "All types"} {
		if !strings.Contains(body, want) {
			t.Errorf("channel type vocabulary missing %q", want)
		}
	}
	if !strings.Contains(body, "<th>Source</th>") || !strings.Contains(body, ">Provider<") {
		t.Error("the lineup should name the source each channel came from")
	}
	for _, want := range []string{"Buffalo Bills vs Houston Texans", "Chicago Bears vs Green Bay Packers", "nothing scheduled"} {
		if !strings.Contains(body, want) {
			t.Errorf("lineup missing %q", want)
		}
	}
	// Now and next: the live game shows under Now for NFL 04, the later one under Next for the Bills channel.
	if !strings.Contains(body, "until ") {
		t.Error("live programme should show its end time")
	}

	if body := hxGet(h, "/lineup?kind=team", "lineup-table"); !strings.Contains(body, "NFL Bills") || strings.Contains(body, "NFL 04") {
		t.Errorf("kind filter wrong: %s", body)
	}
	if body := hxGet(h, "/lineup?league=nba", "lineup-table"); !strings.Contains(body, "NBA 01") || strings.Contains(body, "NFL 04") {
		t.Errorf("league filter wrong")
	}
	if body := hxGet(h, "/lineup?q=packers", "lineup-table"); !strings.Contains(body, "NFL 05") || strings.Contains(body, "NFL 06") {
		t.Errorf("search should match a matchup: %s", body)
	}
	// Channels with nothing on are exactly the ones without a now or a next.
	scheduled := hxGet(h, "/lineup?scheduled=yes", "lineup-table")
	if !strings.Contains(scheduled, "NFL 04") || strings.Contains(scheduled, "NFL 06") || strings.Contains(scheduled, "NBA 01") {
		t.Errorf("scheduled filter wrong: %s", scheduled)
	}
	idle := hxGet(h, "/lineup?scheduled=no", "lineup-table")
	if !strings.Contains(idle, "NFL 06") || !strings.Contains(idle, "NBA 01") || strings.Contains(idle, "NFL 04") {
		t.Errorf("unscheduled filter wrong: %s", idle)
	}

}

func TestLeaguesPage(t *testing.T) {
	s, st, ref := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	s.Snapshots.Set(lineupSnapshot())

	body := do(h, http.MethodGet, "/leagues", nil, false).Body.String()
	if !strings.Contains(body, "NFL Football") || !strings.Contains(body, "3 of 4 channels carrying a game") {
		t.Errorf("leagues page: %s", body[:min(600, len(body))])
	}
	// Series ids are how the guide is wired up, not something to read.
	if strings.Contains(body, "191277") {
		t.Error("the leagues page should not show series ids")
	}
	// Durations are chosen from a list, so bad text cannot be typed in the first place.
	if strings.Count(body, `data-picker="duration"`) != len(s.Catalog.Leagues) {
		t.Error("every league card should offer a game length chooser")
	}
	if strings.Contains(body, "customised") {
		t.Error("no overrides yet")
	}

	// Save an override: disable NBA and rename the NFL airing with a longer game.
	rec := do(h, http.MethodPut, "/leagues/nfl", url.Values{"airing_title": {"Pro Football"}, "duration": {"4h0m0s"}, "start_pad": {""}}, true)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "customised") || !strings.Contains(body, `value="Pro Football"`) {
		t.Errorf("save: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `value="4h0m0s" checked`) {
		t.Errorf("the chosen game length should be selected: %s", body)
	}
	ov, _ := st.LeagueOverrides(ctx)
	if ov["nfl"].AiringTitle == nil || *ov["nfl"].AiringTitle != "Pro Football" || ov["nfl"].StartPad != nil {
		t.Errorf("override should hold only the fields that differ from defaults: %+v", ov["nfl"])
	}
	// A league page changes how airings read, never whether they are exported.
	rec = do(h, http.MethodPut, "/leagues/nba", url.Values{"airing_title": {"Pro Basketball"}}, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Enabled") {
		t.Errorf("a league card should carry no export switch: %d %s", rec.Code, rec.Body.String())
	}
	if ref.triggered != 2 {
		t.Errorf("each save should trigger a refresh, got %d", ref.triggered)
	}

	// The select cannot post bad text, but a direct request still cannot corrupt a league.
	rec = do(h, http.MethodPut, "/leagues/nfl", url.Values{"enabled": {"1"}, "duration": {"soon"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a duration") {
		t.Errorf("validation: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	// Reset removes the override.
	rec = do(h, http.MethodDelete, "/leagues/nfl", nil, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "customised") {
		t.Errorf("reset: %d", rec.Code)
	}
	if ov, _ = st.LeagueOverrides(ctx); len(ov) != 1 {
		t.Errorf("only the NBA override should remain: %v", ov)
	}
	if rec := do(h, http.MethodPut, "/leagues/xfl", url.Values{}, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown league: %d", rec.Code)
	}
}

func TestPreviews(t *testing.T) {
	s, _, _ := uiServer(t)
	h := s.Handler()
	if body := do(h, http.MethodGet, "/preview/xmltv", nil, false).Body.String(); !strings.Contains(body, "Nothing generated yet") {
		t.Error("empty preview state missing")
	}
	s.Snapshots.Set(lineupSnapshot())
	body := do(h, http.MethodGet, "/preview/xmltv", nil, false).Body.String()
	if !strings.Contains(body, "&lt;channel id=&#34;NFL 04&#34;&gt;") || strings.Contains(body, `channel id="NBA 01"`) {
		t.Errorf("xmltv preview should show the escaped guide: %s", body[:min(500, len(body))])
	}
	body = do(h, http.MethodGet, "/preview/m3u", nil, false).Body.String()
	if !strings.Contains(body, "channel-number=&#34;8504&#34;") {
		t.Error("m3u preview missing")
	}
	if rec := do(h, http.MethodGet, "/preview/pdf", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind: %d", rec.Code)
	}
}

func TestDurationChoices(t *testing.T) {
	// No override: the default is selected and carries the empty value.
	got := durationPicker("duration", gameLengths, 210*time.Minute, nil).Options
	var def pickerOption
	for _, c := range got {
		if c.Selected {
			def = c
		}
	}
	if def.Value != "" || def.Label != "3h 30m (default)" {
		t.Errorf("default choice: %+v", def)
	}

	// An override selects its own entry, and the default is still offered.
	cur := "4h0m0s"
	got = durationPicker("duration", gameLengths, 210*time.Minute, &cur).Options
	var selected, defaults int
	for _, c := range got {
		if c.Selected {
			selected++
			if c.Value != "4h0m0s" {
				t.Errorf("selected %+v", c)
			}
		}
		if c.Value == "" {
			defaults++
		}
	}
	if selected != 1 || defaults != 1 {
		t.Errorf("want one selected and one default entry, got %d and %d", selected, defaults)
	}

	// A value off the ladder is kept rather than silently dropped, and stays in order.
	odd := "3h45m0s"
	got = durationPicker("duration", gameLengths, 3*time.Hour, &odd).Options
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	if !slices.Contains(labels, "3h 45m") || !slices.Contains(labels, "3h (default)") {
		t.Errorf("labels: %v", labels)
	}
	if i, j := slices.Index(labels, "3h 30m"), slices.Index(labels, "3h 45m"); i > j {
		t.Errorf("choices should be ordered: %v", labels)
	}
	// Start pads read as plain minutes, and zero is spelled out.
	if got := durationPicker("start_pad", startPads, 15*time.Minute, nil).Options; got[0].Label != "None" {
		t.Errorf("first start pad choice: %+v", got[0])
	}
}

// The search box reads the table, not the database behind it. A row that matched on an
// airing the reader cannot see just looks wrong.
func TestLineupSearchesOnlyWhatItShows(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	st.CreateSource(context.Background(), store.NewSource{Name: "Provider", URL: "http://p/1"})
	s.Snapshots.Set(lineupSnapshot())

	// "Jets" appears only in an airing three days out, past now and next.
	if body := do(h, http.MethodGet, "/lineup?q=jets", nil, false).Body.String(); strings.Count(body, "<tr") > 1 {
		t.Error("a hidden airing should not put a row in the results")
	}
	// Everything the row does show is searchable: team, matchup, id, source.
	for _, q := range []string{"packers", "bills", "nfl+05", "provider", "8505"} {
		if body := do(h, http.MethodGet, "/lineup?q="+q, nil, false).Body.String(); strings.Count(body, "<tr") < 2 {
			t.Errorf("%q should match something visible", q)
		}
	}
}

// Choosing teams narrows the list and retargets the airings shown, so every row says why
// it is there.
func TestLineupTeamFilter(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	st.CreateSource(context.Background(), store.NewSource{Name: "Provider", URL: "http://p/1"})
	s.Snapshots.Set(lineupSnapshot())

	// Single and multiple choice are the same component, so they match in the markup.
	body0 := do(h, http.MethodGet, "/lineup", nil, false).Body.String()
	for _, want := range []string{`data-picker="league"`, `data-picker="kind"`, `data-picker="scheduled"`} {
		if !strings.Contains(body0, want) {
			t.Errorf("every filter should be a picker, missing %s", want)
		}
	}
	if strings.Contains(body0, "<select") {
		t.Error("no native selects should survive")
	}

	// The picker needs a league first; it offers that league's teams only.
	if body := do(h, http.MethodGet, "/lineup", nil, false).Body.String(); strings.Contains(body, `name="team"`) {
		t.Error("teams should not be offered before a league is chosen")
	}
	body := do(h, http.MethodGet, "/lineup?league=nfl", nil, false).Body.String()
	for _, want := range []string{"Buffalo Bills", "Green Bay Packers", "New York Jets"} {
		if !strings.Contains(body, `<input type="checkbox" name="team"`) || !strings.Contains(body, want) {
			t.Errorf("the NFL picker should offer %q", want)
		}
	}

	// The Packers play the Bears, carried by a slot channel and by the Bills channel,
	// whose next airing is otherwise something else.
	body = do(h, http.MethodGet, "/lineup?league=nfl&team=green-bay-packers", nil, false).Body.String()
	if got := strings.Count(body, `href="/lineup/`); got != 2 {
		t.Errorf("expected the two channels carrying the Packers, got %d", got)
	}
	if strings.Contains(body, "Buffalo Bills vs Houston Texans") {
		t.Error("a filtered row should show the chosen team's airing, not an unrelated one")
	}
	if n := strings.Count(body, "Chicago Bears vs Green Bay Packers"); n != 2 {
		t.Errorf("both rows should show the Packers game, got %d", n)
	}
	// Two teams widen the list rather than narrowing it further.
	body = do(h, http.MethodGet, "/lineup?league=nfl&team=green-bay-packers&team=houston-texans", nil, false).Body.String()
	if got := strings.Count(body, `href="/lineup/`); got != 3 {
		t.Errorf("expected three channels across both teams, got %d", got)
	}
	// A team left over from another league is dropped rather than emptying the list.
	body = do(h, http.MethodGet, "/lineup?league=nfl&team=some-nba-team", nil, false).Body.String()
	if got := strings.Count(body, `href="/lineup/`); got != 4 {
		t.Errorf("a stale team should be ignored, got %d rows", got)
	}
}

func TestChannelInspector(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	st.CreateSource(context.Background(), store.NewSource{Name: "Provider", URL: "http://p/1"})
	s.Snapshots.Set(lineupSnapshot())

	// A team channel carries several airings; the one on now is marked, and the
	// Gracenote ids are shown so a mismatch is visible without reading the XMLTV.
	body := do(h, http.MethodGet, "/lineup/k-nfl-bills", nil, false).Body.String()
	for _, want := range []string{"NFL Bills", "channel 10800", "Team Channel", "Buffalo Bills vs Houston Texans",
		"Chicago Bears vs Green Bay Packers", "on now", "Buffalo Bills broadcast", "Provider"} {
		if !strings.Contains(body, want) {
			t.Errorf("channel page missing %q", want)
		}
	}
	// A channel with nothing on says so rather than showing an empty table.
	if body := do(h, http.MethodGet, "/lineup/k-nfl-06", nil, false).Body.String(); !strings.Contains(body, "Nothing scheduled on this channel") {
		t.Error("empty channel state missing")
	}
	// The page lists the channel's own properties, in the reader's terms. Gracenote ids
	// go into the XMLTV, not onto the screen.
	page := do(h, http.MethodGet, "/lineup/k-nfl-bills", nil, false).Body.String()
	for _, want := range []string{"Channel ID", "Channel Number", "Stream URL"} {
		if !strings.Contains(page, want) {
			t.Errorf("channel page should list %q", want)
		}
	}
	for _, gone := range []string{"Channels DVR", "· 34", "Id given to"} {
		if strings.Contains(page, gone) {
			t.Errorf("channel page should not show %q", gone)
		}
	}

	// A channel page describes a channel. It offers nothing that changes whether the
	// channel is exported, because nothing does.
	body = do(h, http.MethodGet, "/lineup/k-nba-01", nil, false).Body.String()
	if !strings.Contains(body, "NBA 01") {
		t.Errorf("channel page: %s", body)
	}
	for _, gone := range []string{"the guide</button>", "/lineup/rule", "switched off"} {
		if strings.Contains(body, gone) {
			t.Errorf("export management still present: %q", gone)
		}
	}
	if rec := do(h, http.MethodGet, "/lineup/nope", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel: %d", rec.Code)
	}
}

// Numbers are the one thing about a channel the user owns, so the Lineup hands out a
// run of them in the order the reader is looking at.
func TestRenumbering(t *testing.T) {
	s, st, ref := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	st.CreateSource(ctx, store.NewSource{Name: "Provider", URL: "http://p/1"})
	// The store has to know these channels before it can move them.
	urls := []string{"http://x/1", "http://x/2", "http://x/3", "http://x/4", "http://x/5"}
	st.SeeChannels(ctx, 1, urls) //nolint:errcheck
	var want []store.Assignment
	for i, u := range urls {
		want = append(want, store.Assignment{Key: store.ChannelKey(1, u),
			PreferredID: []string{"NFL 04", "NFL 05", "NFL 06", "NFL Bills", "NBA 01"}[i],
			Base:        10000, Limit: 10800})
	}
	st.AssignNumbers(ctx, want) //nolint:errcheck
	snap := lineupSnapshot()
	for i := range snap.Channels {
		snap.Channels[i].Key = store.ChannelKey(1, urls[i])
	}
	s.Snapshots.Set(snap)
	key := func(i int) string { return store.ChannelKey(1, urls[i]) }

	// Nothing ticked, or no number: say so rather than doing something surprising.
	for _, form := range []url.Values{{"start": {"200"}}, {"key": {key(0)}}, {"key": {key(0)}, "start": {"0"}}} {
		if rec := do(h, http.MethodPost, "/lineup/numbers", form, true); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v should be refused, got %d", form, rec.Code)
		}
	}

	// Three channels take 200, 201, 202 in the order they were posted.
	rec := do(h, http.MethodPost, "/lineup/numbers", url.Values{"key": {key(1), key(0), key(3)}, "start": {"200"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("renumber: %d %s", rec.Code, rec.Body.String())
	}
	all, _ := st.Channels(ctx, 1)
	for i, k := range []string{key(1), key(0), key(3)} {
		if c := all[k]; c.Number != 200+i || !c.ByUser {
			t.Errorf("channel %d: %+v", i, c)
		}
	}
	if ref.triggered == 0 {
		t.Error("renumbering should ask for a refresh so the outputs follow")
	}
	// The guide says so straight away rather than serving the old numbers until the
	// refresh lands.
	live := s.Snapshots.Get()
	moved, _ := live.ByKey(key(1))
	if moved.Number != 200 || !moved.ByUser {
		t.Errorf("the snapshot should carry the new number: %+v", moved)
	}
	if !strings.Contains(rec.Body.String(), ">200<") {
		t.Error("the re-rendered lineup should show the new numbers")
	}

	// A refusal keeps the selection and the number typed: losing them means picking the
	// channels out again to correct a typo. A pass that worked clears them.
	rec = do(h, http.MethodPost, "/lineup/numbers", url.Values{"key": {key(0), key(2)}, "start": {"nope"}}, true)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad start: %d", rec.Code)
	}
	for _, k := range []string{key(0), key(2)} {
		if !strings.Contains(body, `value="`+k+`" checked`) {
			t.Errorf("channel %s should still be ticked", k)
		}
	}
	if !strings.Contains(body, `>2</span> selected`) {
		t.Error("the bar should still say two are selected")
	}
	rec = do(h, http.MethodPost, "/lineup/numbers", url.Values{"key": {key(0)}, "start": {"700"}}, true)
	if body = rec.Body.String(); strings.Contains(body, `value="`+key(0)+`" checked`) {
		t.Error("a renumbering that worked should leave nothing selected")
	}
	if !strings.Contains(body, `>0</span> selected`) {
		t.Error("and the bar should say so")
	}

	// A number another channel holds is refused, and nothing moves.
	rec = do(h, http.MethodPost, "/lineup/numbers", url.Values{"key": {key(2)}, "start": {"200"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "already taken") {
		t.Errorf("clash: %d %s", rec.Code, rec.Body.String())
	}
	if all, _ = st.Channels(ctx, 1); all[key(1)].Number != 200 {
		t.Errorf("a refused renumber must change nothing: %+v", all[key(1)])
	}
}
