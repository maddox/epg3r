package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/scheduler"
	"github.com/jonmaddox/epg3r/internal/store"
)

// Refresher is the scheduler surface the UI needs.
type Refresher interface {
	Trigger(store.Trigger) error
	Status() scheduler.Status
}

// SourceTester fetches a playlist URL and reports how many channels it holds.
type SourceTester func(ctx context.Context, url string) (int, error)

// ---------- dashboard ----------

type leagueStat struct {
	Key, Name string
	Channels  int
	WithGames int
}

type dashboard struct {
	Snapshot   *model.Snapshot
	Leagues    []leagueStat
	Channels   int
	Programmes int
	WithGames  int
	LastRun    *store.Run
	Runs       []store.Run
	Sources    []store.Source
	Outputs    struct{ M3U, XMLTV string }
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := dashboard{Snapshot: s.Snapshots.Get()}
	base := s.baseURL(r)
	d.Outputs.M3U, d.Outputs.XMLTV = base+M3UPath, base+XMLTVPath

	if d.Snapshot != nil {
		byKey := map[string]*leagueStat{}
		for _, ch := range d.Snapshot.Channels {
			ls := byKey[ch.LeagueKey]
			if ls == nil {
				name := strings.ToUpper(ch.LeagueKey)
				if lg, ok := s.Catalog.League(ch.LeagueKey); ok {
					name = lg.Name
				}
				ls = &leagueStat{Key: ch.LeagueKey, Name: name}
				byKey[ch.LeagueKey] = ls
			}
			ls.Channels++
			d.Channels++
			if n := len(ch.Programmes); n > 0 {
				ls.WithGames++
				d.WithGames++
				d.Programmes += n
			}
		}
		for _, lg := range s.Catalog.Leagues {
			if ls, ok := byKey[lg.Key]; ok {
				d.Leagues = append(d.Leagues, *ls)
			}
		}
	}
	if runs, err := s.Store.ListRuns(ctx, 6); err == nil && len(runs) > 0 {
		d.LastRun = &runs[0]
		d.Runs = runs
	}
	d.Sources, _ = s.Store.ListSources(ctx)
	s.page(w, r, "dashboard", s.view("Dashboard", "dashboard", d))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if s.Refresher == nil {
		http.Error(w, "no scheduler", http.StatusServiceUnavailable)
		return
	}
	switch err := s.Refresher.Trigger(store.TriggerManual); {
	case errors.Is(err, scheduler.ErrRunning):
		toast(w, "info", "A refresh is already running")
		w.WriteHeader(http.StatusAccepted)
	case err != nil:
		toast(w, "bad", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
	default:
		toast(w, "ok", "Refresh started")
	}
	s.partial(w, r, "", "status_pill", s.status())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.partial(w, r, "", "status_pill", s.status())
}

// ---------- sources ----------

// sourceView is what the source templates receive.
type sourceView struct {
	Sources []store.Source  // list
	Source  *store.Source   // row being shown or edited
	Form    store.NewSource // form values (add form, or edit form prefilled)
	Error   string
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListSources(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "sources", s.view("Sources", "sources", sourceView{Sources: list}))
}

// sourceForm reads the fields the forms submit, starting from an existing source's
// values so fields the UI does not expose are preserved.
func sourceForm(r *http.Request, from store.NewSource) store.NewSource {
	from.Name = r.FormValue("name")
	from.URL = r.FormValue("url")
	from.XMLTVURL = r.FormValue("xmltv_url")
	from.Timezone = r.FormValue("timezone")
	from.IDPrefix = r.FormValue("id_prefix")
	if _, present := r.Form["enabled"]; present || r.FormValue("editing") != "" {
		from.Disabled = r.FormValue("enabled") == ""
	}
	return from
}

// validationStatus maps a store error to an HTTP status: 422 for user input, 500 else.
func (s *Server) storeErr(w http.ResponseWriter, r *http.Request, err error) (userMsg string, fatal bool) {
	var verr *store.ValidationError
	if errors.As(err, &verr) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return verr.Msg, false
	}
	s.fail(w, r, err)
	return "", true
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	in := sourceForm(r, store.NewSource{})
	if _, err := s.Store.CreateSource(r.Context(), in); err != nil {
		if msg, fatal := s.storeErr(w, r, err); !fatal {
			s.partial(w, r, "sources", "source_form", sourceView{Form: in, Error: msg})
		}
		return
	}
	list, err := s.Store.ListSources(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	toast(w, "ok", "Source added. Refresh to build the guide.")
	s.partial(w, r, "sources", "source_list", sourceView{Sources: list})
}

// sourceRow renders a source's row in the given block.
func (s *Server) sourceRow(block string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		src, ok := pathID(s, w, r, s.Store.GetSource)
		if !ok {
			return
		}
		s.partial(w, r, "sources", block, sourceView{Source: &src, Form: src.Input()})
	}
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	src, ok := pathID(s, w, r, s.Store.GetSource)
	if !ok {
		return
	}
	in := sourceForm(r, src.Input())
	if err := s.Store.UpdateSource(r.Context(), src.ID, in); err != nil {
		if msg, fatal := s.storeErr(w, r, err); !fatal {
			s.partial(w, r, "sources", "source_row_edit", sourceView{Source: &src, Form: in, Error: msg})
		}
		return
	}
	updated, _, err := s.Store.GetSource(r.Context(), src.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	toast(w, "ok", "Source saved")
	s.partial(w, r, "sources", "source_row", sourceView{Source: &updated})
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	src, ok := pathID(s, w, r, s.Store.GetSource)
	if !ok {
		return
	}
	if err := s.Store.DeleteSource(r.Context(), src.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	toast(w, "ok", "Source removed")
	w.WriteHeader(http.StatusOK) // empty body: HTMX removes the row
}

func (s *Server) handleTestSource(w http.ResponseWriter, r *http.Request) {
	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		src, ok := pathID(s, w, r, s.Store.GetSource)
		if !ok {
			return
		}
		url = src.URL
	}
	type result struct {
		OK    bool
		Count int
		Error string
	}
	res := result{}
	if s.TestSource == nil {
		res.Error = "testing is not available"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		n, err := s.TestSource(ctx, url)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Count = true, n
		}
	}
	s.partial(w, r, "sources", "test_result", res)
}

// pathID loads the record named by the {id} path value, answering 404 or 500 itself.
func pathID[T any](s *Server, w http.ResponseWriter, r *http.Request, get func(context.Context, int64) (T, bool, error)) (T, bool) {
	var zero T
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return zero, false
	}
	v, ok, err := get(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return zero, false
	}
	if !ok {
		http.NotFound(w, r)
		return zero, false
	}
	return v, true
}

// ---------- runs ----------

type runsPage struct {
	Runs []store.Run
}

type runDetail struct {
	Run     store.Run
	Rows    []store.RunChannel
	Filter  store.RunChannelFilter
	Leagues []catalog.League
}

const rowLimit = 500

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Store.ListRuns(r.Context(), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "runs", s.view("Runs", "runs", runsPage{Runs: runs}))
}

// handleRun serves the run detail. Filters arrive as query parameters; HTMX requests
// from the tabs or the filter form get only the block they target.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	run, ok := pathID(s, w, r, s.Store.GetRun)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RunChannelFilter{
		Status: store.Outcome(q.Get("status")),
		League: q.Get("league"),
		Query:  strings.TrimSpace(q.Get("q")),
		Limit:  rowLimit,
	}
	rows, err := s.Store.RunChannels(r.Context(), run.ID, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := runDetail{Run: run, Rows: rows, Filter: f, Leagues: s.Catalog.Leagues}
	s.page(w, r, "run", s.view("Run "+strconv.FormatInt(run.ID, 10), "runs", d))
}

// ---------- settings ----------

type settingField struct {
	store.SettingDef
	Value string
	Error string
}

// Control is the form control for this setting: a presentation decision kept out of the
// store's type constants.
func (f settingField) Control() string {
	switch {
	case f.Kind == store.KindBool:
		return "checkbox"
	case len(f.Choices) > 0:
		return "select"
	case f.Kind == store.KindInt || f.Kind == store.KindFloat:
		return "number"
	}
	return "text"
}

type settingsPage struct {
	Fields []settingField
	Saved  bool
}

func settingsFields(values map[string]string, errs map[string]string) []settingField {
	out := make([]settingField, 0, len(store.SettingDefs))
	for _, d := range store.SettingDefs {
		out = append(out, settingField{SettingDef: d, Value: values[d.Key], Error: errs[d.Key]})
	}
	return out
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	values, err := s.Store.Settings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "settings", s.view("Settings", "settings", settingsPage{Fields: settingsFields(values, nil)}))
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	submitted := map[string]string{}
	for _, d := range store.SettingDefs {
		raw := r.FormValue(d.Key)
		if d.Kind == store.KindBool && raw == "" {
			raw = "0" // an unchecked checkbox is absent from the form
		}
		submitted[d.Key] = raw
	}
	problems, err := s.Store.SetSettings(r.Context(), submitted)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.partial(w, r, "settings", "settings_form", settingsPage{Fields: settingsFields(submitted, problems)})
		return
	}
	values, _ := s.Store.Settings(r.Context())
	toast(w, "ok", "Settings saved")
	s.partial(w, r, "settings", "settings_form", settingsPage{Fields: settingsFields(values, nil), Saved: true})
}

// baseURL is how the requester reaches this server, for links to paste into Channels.
func (s *Server) baseURL(r *http.Request) string {
	if v, err := s.Store.Setting(r.Context(), store.SettingPublicBaseURL); err == nil && v != "" {
		return strings.TrimRight(v, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}
