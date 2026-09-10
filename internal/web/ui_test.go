package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/scheduler"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/store/storetest"
)

type fakeRefresher struct {
	st        scheduler.Status
	triggered int
}

func (f *fakeRefresher) Trigger(store.Trigger) error {
	f.triggered++
	if f.st.Running {
		return scheduler.ErrRunning
	}
	return nil
}
func (f *fakeRefresher) Status() scheduler.Status { return f.st }

func uiServer(t *testing.T) (*Server, *store.Store, *fakeRefresher) {
	t.Helper()
	st := storetest.Open(t)
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	ref := &fakeRefresher{}
	s := newTestServer()
	s.Store, s.Catalog, s.Refresher = st, cat, ref
	s.TestSource = func(ctx context.Context, u string) (int, error) {
		if strings.Contains(u, "bad") {
			return 0, errors.New("HTTP 502")
		}
		return 42, nil
	}
	return s, st, ref
}

// hxGet issues an HTMX GET naming the element it targets, so the server returns just
// that block.
func hxGet(h http.Handler, path, target string) string {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", target)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

func do(h http.Handler, method, path string, form url.Values, hx bool) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAllTemplatesParse(t *testing.T) {
	tpl := newTemplates(false, nil)
	set, base, err := tpl.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"dashboard", "sources", "runs", "run", "settings"} {
		if set[page] == nil {
			t.Errorf("missing page template %q", page)
		}
	}
	if base.Lookup("status_pill") == nil {
		t.Error("status_pill should be a base partial")
	}
}

func TestPagesRenderEmptyAndFull(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()

	// Empty state: no sources, no runs.
	for _, path := range []string{"/", "/sources", "/runs", "/settings"} {
		rec := do(h, http.MethodGet, path, nil, false)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
		}
	}
	if body := do(h, http.MethodGet, "/", nil, false).Body.String(); !strings.Contains(body, "Add your first source") {
		t.Error("dashboard should show the first-source empty state")
	}

	// Populate: a source, a run with rows, a snapshot.
	src, _ := st.CreateSource(ctx, store.NewSource{Name: "Provider", URL: "http://p.example/list.m3u"})
	id, _ := st.StartRun(ctx, store.TriggerManual)
	start := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	rows := []store.RunChannel{
		{SourceID: src, Group: "NFL", RawTitle: "NFL 04: Bills vs Texans", Status: store.OutcomeExported, LeagueKey: "nfl", Kind: "slot", ChannelID: "NFL 04", ChannelNumber: 8504, Matchup: "Buffalo Bills vs Houston Texans", StartAt: &start, Confidence: 0.95},
		{SourceID: src, Group: "NBA", RawTitle: "NBA 01: Offline", Status: store.OutcomeIdle, LeagueKey: "nba", Kind: "placeholder", ChannelID: "NBA 01", ChannelNumber: 11501},
		{SourceID: src, Group: "NFL", RawTitle: "USA: NFL NETWORK", Status: store.OutcomeNetwork, LeagueKey: "nfl", Kind: "network", Reason: "not an event channel"},
	}
	snap := &model.Snapshot{RunID: id, Channels: []model.Channel{
		{ID: "NFL 04", Number: 8504, Name: "NFL 04", Kind: model.KindSlot, LeagueKey: "nfl", StreamURL: "http://x/1",
			Programmes: []model.Programme{{Event: model.Event{ID: "191277-a", SeriesID: "191277", Title: "NFL Football", SubTitle: "Bills vs Texans", Start: start, Stop: start.Add(3 * time.Hour), Kickoff: start}}}},
		{ID: "NBA 01", Number: 11501, Name: "NBA 01", Kind: model.KindPlaceholder, LeagueKey: "nba", StreamURL: "http://x/2"},
	}}
	if err := st.FinishRun(ctx, id, store.RunOK, "", rows, snap, 10); err != nil {
		t.Fatal(err)
	}
	s.Snapshots.Set(snap)

	rec := do(h, http.MethodGet, "/", nil, false)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", rec.Code, body)
	}
	for _, want := range []string{"Carrying a game", "NFL", "NBA", "/m3u", "/xmltv", "Provider", "Recent runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	body = do(h, http.MethodGet, "/runs", nil, false).Body.String()
	if !strings.Contains(body, "#1") || !strings.Contains(body, "Exported") {
		t.Error("runs page missing the run")
	}
	body = do(h, http.MethodGet, "/runs/1", nil, false).Body.String()
	for _, want := range []string{"Run #1", "Bills vs Texans", "NFL NETWORK", "not an event channel"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
	if rec := do(h, http.MethodGet, "/runs/99", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", rec.Code)
	}
}

func TestHXRequestGetsContentOnly(t *testing.T) {
	s, _, _ := uiServer(t)
	h := s.Handler()
	full := do(h, http.MethodGet, "/settings", nil, false).Body.String()
	part := do(h, http.MethodGet, "/settings", nil, true).Body.String()
	if !strings.Contains(full, "<!doctype html>") || strings.Contains(part, "<!doctype html>") {
		t.Error("HX-Request should render the content block only")
	}
	if !strings.Contains(part, "Refresh every") {
		t.Error("partial should still contain the page content")
	}
}

func TestRunChannelFilters(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	src, _ := st.CreateSource(ctx, store.NewSource{Name: "p", URL: "http://p/1"})
	id, _ := st.StartRun(ctx, store.TriggerManual)
	rows := []store.RunChannel{
		{SourceID: src, RawTitle: "NFL 04: Bills vs Texans", Status: store.OutcomeExported, LeagueKey: "nfl", ChannelID: "NFL 04", ChannelNumber: 8504},
		{SourceID: src, RawTitle: "NBA 01: Offline", Status: store.OutcomeIdle, LeagueKey: "nba", ChannelID: "NBA 01", ChannelNumber: 11501},
	}
	st.FinishRun(ctx, id, store.RunOK, "", rows, map[string]any{}, 10)

	// A tab request targets the whole view; the filter form targets the table only.
	body := hxGet(h, "/runs/1?status=idle", "channel-view")
	if !strings.Contains(body, "NBA 01") || strings.Contains(body, "NFL 04") {
		t.Errorf("status filter wrong: %s", body)
	}
	if !strings.Contains(body, `id="channel-view"`) || strings.Contains(body, "<!doctype") || strings.Contains(body, "Run #1") {
		t.Error("tab request should return only the channel view block")
	}
	body = hxGet(h, "/runs/1?q=texans", "channel-table")
	if !strings.Contains(body, "NFL 04") || strings.Contains(body, "NBA 01") || strings.Contains(body, `id="channel-view"`) {
		t.Errorf("search filter should return only the table: %s", body)
	}
	body = hxGet(h, "/runs/1?league=nba", "channel-table")
	if !strings.Contains(body, "NBA 01") || strings.Contains(body, "NFL 04") {
		t.Error("league filter wrong")
	}
	if body := hxGet(h, "/runs/1?q=zzz", "channel-table"); !strings.Contains(body, "Nothing matches") {
		t.Error("empty result state missing")
	}
	// An HTMX request for a target the page does not define falls back to the content block.
	if body := hxGet(h, "/runs/1", "main"); !strings.Contains(body, "Run #1") || strings.Contains(body, "<!doctype") {
		t.Error("unknown target should get the content block")
	}
}

func TestSourcesFlow(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()

	// Validation failure re-renders the form with the error and the submitted values.
	rec := do(h, http.MethodPost, "/sources", url.Values{"name": {"X"}, "url": {"ftp://nope"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "must start with http") || !strings.Contains(rec.Body.String(), `value="ftp://nope"`) {
		t.Errorf("bad url: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hx-swap-oob") {
		t.Error("a failed create must not touch the list")
	}
	// Testing with no URL says so rather than failing silently.
	if body := do(h, http.MethodPost, "/sources/test", url.Values{"url": {""}}, true).Body.String(); !strings.Contains(body, "enter a playlist URL") {
		t.Errorf("empty test url: %s", body)
	}
	rec = do(h, http.MethodPost, "/sources", url.Values{"name": {"X"}, "url": {"http://p/1"}, "timezone": {"Mars/Base"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unknown time zone") {
		t.Errorf("bad tz: %d", rec.Code)
	}

	// Create: the response is an empty form plus the list swapped out of band, with a toast.
	rec = do(h, http.MethodPost, "/sources", url.Values{"name": {"Provider"}, "url": {"http://p/1"}, "xmltv_url": {"http://p/g.xml"}}, true)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Provider") || !strings.Contains(rec.Header().Get("HX-Trigger"), "toast") {
		t.Fatalf("create: %d %s %s", rec.Code, rec.Header().Get("HX-Trigger"), body)
	}
	if !strings.Contains(body, `id="source-list" hx-swap-oob="innerHTML"`) || strings.Contains(body, `value="http://p/1"`) {
		t.Errorf("create should return an emptied form and the list out of band: %s", body)
	}
	list, _ := st.ListSources(ctx)
	if len(list) != 1 || list[0].XMLTVURL != "http://p/g.xml" {
		t.Fatalf("stored: %+v", list)
	}
	id := list[0].ID

	// Edit row, update (disable and rename), show row.
	if body := do(h, http.MethodGet, "/sources/1/edit", nil, true).Body.String(); !strings.Contains(body, `name="url"`) {
		t.Error("edit form missing")
	}
	rec = do(h, http.MethodPut, "/sources/1", url.Values{"editing": {"1"}, "name": {"Renamed"}, "url": {"http://p/2"}}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed") || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("update: %d %s", rec.Code, rec.Body.String())
	}
	got, _, _ := st.GetSource(ctx, id)
	if got.Enabled || got.URL != "http://p/2" || got.XMLTVURL != "" {
		t.Errorf("update not stored: %+v", got)
	}
	// Validation on update keeps the edit form open with the message.
	rec = do(h, http.MethodPut, "/sources/1", url.Values{"editing": {"1"}, "url": {"nope"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "must start with http") {
		t.Errorf("update validation: %d", rec.Code)
	}

	// Test fetch, good and bad.
	if body := do(h, http.MethodPost, "/sources/test", url.Values{"url": {"http://p/ok"}}, true).Body.String(); !strings.Contains(body, "42 channels") {
		t.Errorf("test ok: %s", body)
	}
	if body := do(h, http.MethodPost, "/sources/test", url.Values{"url": {"http://p/bad"}}, true).Body.String(); !strings.Contains(body, "HTTP 502") {
		t.Errorf("test bad: %s", body)
	}

	// Delete removes the row.
	rec = do(h, http.MethodDelete, "/sources/1", nil, true)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("delete: %d %q", rec.Code, rec.Body.String())
	}
	if list, _ := st.ListSources(ctx); len(list) != 0 {
		t.Error("source not deleted")
	}
	if rec := do(h, http.MethodGet, "/sources/1", nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("gone source: %d", rec.Code)
	}
}

func TestSettingsSaveAndValidate(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()

	form := url.Values{}
	for _, d := range store.SettingDefs {
		form.Set(d.Key, d.Default)
	}
	form.Set(store.SettingRefreshIntervalMinutes, "0")
	rec := do(h, http.MethodPut, "/settings", form, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "at least 1") {
		t.Errorf("validation: %d", rec.Code)
	}
	if v, _ := st.Setting(context.Background(), store.SettingRefreshIntervalMinutes); v != "60" {
		t.Error("invalid submission must not be partially saved")
	}

	form.Set(store.SettingRefreshIntervalMinutes, "30")
	form.Del(store.SettingRefreshOnStart) // unchecked checkbox is absent from the form
	form.Set(store.SettingChannelIDStyle, "slug")
	rec = do(h, http.MethodPut, "/settings", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved.") {
		t.Errorf("save: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	all, _ := st.Settings(context.Background())
	if all.RefreshInterval() != 30*time.Minute || all.Bool(store.SettingRefreshOnStart) || all[store.SettingChannelIDStyle] != "slug" {
		t.Errorf("settings not saved: %v", all)
	}
}

// A start 500 above the last one is nobody's problem now: one number moves the whole shelf,
// so there is no arrangement of leagues for a reader to get wrong. What the move does to the
// numbers themselves is the store's business and is tested there; this is the handler wiring,
// including that a value it cannot read moves nothing.
func TestChannelStartSetting(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	form := url.Values{}
	for _, d := range store.SettingDefs {
		form.Set(d.Key, d.Default)
	}
	form.Set(store.SettingChannelStart, "10500")
	if rec := do(h, http.MethodPut, "/settings", form, true); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	if v, _ := st.Setting(ctx, store.SettingChannelStart); v != "10500" {
		t.Fatalf("start = %s", v)
	}
	// And a start that cannot be read is refused without moving anything.
	form.Set(store.SettingChannelStart, "nope")
	if rec := do(h, http.MethodPut, "/settings", form, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad start: %d", rec.Code)
	}
	if v, _ := st.Setting(ctx, store.SettingChannelStart); v != "10500" {
		t.Errorf("a refused start changed the setting: %s", v)
	}
}

func TestRefreshAndStatusPill(t *testing.T) {
	s, _, ref := uiServer(t)
	h := s.Handler()
	rec := do(h, http.MethodPost, "/refresh", nil, true)
	if rec.Code != http.StatusOK || ref.triggered != 1 || !strings.Contains(rec.Header().Get("HX-Trigger"), "Refresh started") {
		t.Errorf("refresh: %d %s", rec.Code, rec.Header().Get("HX-Trigger"))
	}
	if body := rec.Body.String(); !strings.Contains(body, "Refresh now") || strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Errorf("idle pill should carry the button and not poll: %s", body)
	}
	ref.st = scheduler.Status{Running: true, Phase: "parsing 1,338 channels"}
	rec = do(h, http.MethodPost, "/refresh", nil, true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Header().Get("HX-Trigger"), "already running") {
		t.Errorf("refresh while running: %d %s", rec.Code, rec.Header().Get("HX-Trigger"))
	}
	body := do(h, http.MethodGet, "/status", nil, true).Body.String()
	if !strings.Contains(body, `hx-trigger="every 3s"`) || !strings.Contains(body, "parsing 1,338 channels") || !strings.Contains(body, "disabled") {
		t.Errorf("running pill should poll and disable the button: %s", body)
	}
	ref.st = scheduler.Status{LastRunAt: time.Now().Add(-5 * time.Minute), LastStatus: store.RunPartial, NextAt: time.Now().Add(55 * time.Minute)}
	body = do(h, http.MethodGet, "/status", nil, true).Body.String()
	if !strings.Contains(body, "Last refresh 5m ago") || !strings.Contains(body, "next in 54 min") && !strings.Contains(body, "next in 55 min") {
		t.Errorf("idle pill: %s", body)
	}
}

func TestStaticAssetsServed(t *testing.T) {
	s, _, _ := uiServer(t)
	h := s.Handler()
	for _, p := range []string{"/static/app.css", "/static/htmx.min.js"} {
		if rec := do(h, http.MethodGet, p, nil, false); rec.Code != http.StatusOK || rec.Body.Len() < 1000 {
			t.Errorf("%s: %d %d bytes", p, rec.Code, rec.Body.Len())
		}
	}
}

func TestLayoutLetsValidationBodiesSwap(t *testing.T) {
	s, _, _ := uiServer(t)
	body := do(s.Handler(), http.MethodGet, "/settings", nil, false).Body.String()
	if !strings.Contains(body, `name="htmx-config"`) || !strings.Contains(body, `"code":"422","swap":true`) {
		t.Error("layout must configure HTMX to swap 422 responses, or validation errors never show")
	}
}

// Every outcome must have a label and a pill style, or the run page renders a blank
// tab and an unstyled badge for it.
func TestEveryOutcomeIsPresentable(t *testing.T) {
	css, err := os.ReadFile("static/src/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range store.Outcomes {
		if outcomeLabels[o] == "" {
			t.Errorf("outcome %q has no label in outcomeLabels", o)
		}
		if !regexp.MustCompile(`\.outcome-` + string(o) + `[\s,{]`).Match(css) {
			t.Errorf("outcome %q has no .outcome-%s rule in app.css", o, o)
		}
	}
}

// Same for channel kinds, which the lineup labels.
func TestEveryChannelKindHasALabel(t *testing.T) {
	for _, k := range []model.ChannelKind{model.KindSlot, model.KindTeam, model.KindPlaceholder, model.KindNetwork} {
		if kindLabels[k] == "" {
			t.Errorf("channel kind %q has no label", k)
		}
	}
}
