package web

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/scheduler"
	"github.com/jonmaddox/epg3r/internal/store"
)

//go:embed templates static
var assets embed.FS

// templates loads the layout, partials, and every page. Each page is parsed together
// with the layout so blocks can be overridden per page; the set is cached unless dev
// mode reloads from the source tree on every request.
type templates struct {
	fsys fs.FS
	dev  bool
	tag  string                // fingerprint of the built assets
	zone func() *time.Location // zone the UI shows times in
	mu   sync.Mutex
	set  map[string]*template.Template
	base *template.Template
}

// assetTag fingerprints the built stylesheet and script, so the URL the browser is
// given changes whenever they do. A version string does not: in development it never
// changes at all, and the browser goes on running whichever copy it has.
func (t *templates) assetTag() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tag != "" && !t.dev {
		return t.tag
	}
	sum := sha256.New()
	for _, name := range []string{"static/app.css", "static/app.js"} {
		b, err := fs.ReadFile(t.fsys, name)
		if err != nil {
			return "dev"
		}
		sum.Write(b)
	}
	t.tag = hex.EncodeToString(sum.Sum(nil))[:10]
	return t.tag
}

func newTemplates(dev bool, zone func() *time.Location) *templates {
	t := &templates{dev: dev, zone: zone}
	if dev {
		t.fsys = os.DirFS("internal/web")
	} else {
		t.fsys = assets
	}
	if t.zone == nil {
		t.zone = func() *time.Location { return time.UTC }
	}
	return t
}

func (t *templates) funcs() template.FuncMap {
	in := func(v any) time.Time {
		switch x := v.(type) {
		case time.Time:
			return x.In(t.zone())
		case *time.Time:
			if x != nil {
				return x.In(t.zone())
			}
		}
		return time.Time{}
	}
	return template.FuncMap{
		"since": func(v any) string {
			x := in(v)
			if x.IsZero() {
				return "never"
			}
			d := time.Since(x)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 48*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return x.Format("Jan 2")
			}
		},
		"until": func(v any) string {
			x := in(v)
			if x.IsZero() {
				return ""
			}
			d := time.Until(x)
			switch {
			case d < time.Minute:
				return "under a minute"
			case d < time.Hour:
				return fmt.Sprintf("%d min", int(d.Minutes()))
			default:
				return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
			}
		},
		// stamp formats a time.Time or *time.Time; nil and zero render as empty.
		"stamp": func(v any) string {
			x := in(v)
			if x.IsZero() {
				return ""
			}
			return x.Format("Mon Jan 2 3:04 PM")
		},
		"dur": func(a time.Time, b *time.Time) string {
			if b == nil {
				return ""
			}
			if d := b.Sub(a); d >= time.Second {
				return d.Round(time.Second).String()
			}
			return "under 1s"
		},
		"upper": strings.ToUpper,
		// A run kept from an older version may carry an outcome this one no longer has;
		// show it as it stands rather than as an empty badge.
		"outcome":   func(o store.Outcome) string { return cmp.Or(outcomeLabels[o], string(o)) },
		"runStatus": func(s store.RunStatus) string { return cmp.Or(statusLabels[s], string(s)) },
		"trigger":   func(t store.Trigger) string { return cmp.Or(triggerLabels[t], string(t)) },
		"kind":      func(k model.ChannelKind) string { return kindLabels[k] },
		"kindName":  func(k model.ChannelKind) string { return kindNames[k] },
		"outcomes":  func() []store.Outcome { return store.Outcomes },
		"count":     func(c store.RunCounts, key string) int { return c[store.Outcome(key)] },
		"deref": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		// A confidence is a fraction; nobody reads 0.85 as "pretty sure".
		"pct2": func(f float64) int { return int(f*100 + 0.5) },
		"pct": func(n, total int) int {
			if total == 0 {
				return 0
			}
			return n * 100 / total
		},
		// choices builds a settings row's chooser, so every select in the app is one component.
		"choices": stringPicker,
		"zones":   zonePicker,
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
	}
}

// kindLabels name the channel types in a column, where the header says what they are.
// Most channels here exist for one event and are reused for the next one; a team channel
// always shows the same team; a spare is one the provider has left empty for now.
var kindLabels = map[model.ChannelKind]string{
	model.KindSlot:        "Event",
	model.KindTeam:        "Team",
	model.KindPlaceholder: "Spare",
	model.KindNetwork:     "Network",
}

// kindNames are the same types standing on their own, where no column header says what
// they are.
var kindNames = map[model.ChannelKind]string{
	model.KindSlot:        "Event channel",
	model.KindTeam:        "Team channel",
	model.KindPlaceholder: "Spare channel",
	model.KindNetwork:     "Network channel",
}

// outcomeLabels say what happened to a channel in plain words, because this is the page
// someone opens when their guide looks wrong. The tests check none is missing.
var outcomeLabels = map[store.Outcome]string{
	store.OutcomeExported:  "Something scheduled",
	store.OutcomeIdle:      "Nothing scheduled",
	store.OutcomeLowConf:   "Unclear listing",
	store.OutcomeUnmatched: "League unknown",
	store.OutcomeDuplicate: "Duplicate",
	store.OutcomeNoNumber:  "No number free",
	store.OutcomeNetwork:   "TV network",
}

// statusLabels say how a refresh went. The stored values are ok, partial and failed, which
// are fine in a database and terse on a page.
var statusLabels = map[store.RunStatus]string{
	store.RunRunning: "Running",
	store.RunOK:      "Finished",
	store.RunPartial: "Some problems",
	store.RunFailed:  "Failed",
}

// triggerLabels say what started a refresh.
var triggerLabels = map[store.Trigger]string{
	store.TriggerStartup:  "On start",
	store.TriggerSchedule: "On schedule",
	store.TriggerManual:   "By hand",
}

// load parses (or returns the cached) template sets: the base set of layout and
// partials, and one clone per page with that page's blocks added.
func (t *templates) load() (map[string]*template.Template, *template.Template, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.set != nil && !t.dev {
		return t.set, t.base, nil
	}
	base, err := template.New("").Funcs(t.funcs()).ParseFS(t.fsys, "templates/layouts/*.html", "templates/partials/*.html")
	if err != nil {
		return nil, nil, fmt.Errorf("parse layout: %w", err)
	}
	pages, err := fs.Glob(t.fsys, "templates/pages/*.html")
	if err != nil {
		return nil, nil, err
	}
	set := map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		clone, err := base.Clone()
		if err != nil {
			return nil, nil, err
		}
		if _, err := clone.ParseFS(t.fsys, p); err != nil {
			return nil, nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		set[name] = clone
	}
	t.set, t.base = set, base
	return set, base, nil
}

// page renders a page. A plain request gets the full layout. An HTMX request gets
// only the block its target asks for: the element id in HX-Target, with dashes as
// underscores, when the page defines a block by that name; otherwise the content
// block. A boosted navigation is a plain request in this respect.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name string, data any) {
	set, base, err := s.tpl.load()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	block := "layout"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		block = "content"
		if target := strings.ReplaceAll(r.Header.Get("HX-Target"), "-", "_"); target != "" {
			if set[name] != nil && set[name].Lookup(target) != nil {
				block = target
				// Page blocks receive the page data, as they do under {{with .Data}}.
				if v, ok := data.(view); ok {
					data = v.Data
				}
			}
		}
	}
	s.render(w, r, set, base, name, block, data)
}

// partial renders one named block. With an empty page name the block comes from the
// base set of layout and partials.
func (s *Server) partial(w http.ResponseWriter, r *http.Request, pageName, block string, data any) {
	set, base, err := s.tpl.load()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, set, base, pageName, block, data)
}

// render writes one block from an already-loaded template set.
func (s *Server) render(w http.ResponseWriter, r *http.Request, set map[string]*template.Template, base *template.Template, pageName, block string, data any) {
	tpl := base
	if pageName != "" {
		if tpl = set[pageName]; tpl == nil {
			s.fail(w, r, fmt.Errorf("no page %q", pageName))
			return
		}
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, block, data); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("render", "path", r.URL.Path, "err", err, "req", RequestID(r.Context()))
	http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
}

// toast asks the page's toast listener to show a message.
func toast(w http.ResponseWriter, kind, msg string) {
	b, _ := json.Marshal(map[string]any{"toast": map[string]string{"kind": kind, "msg": msg}})
	w.Header().Set("HX-Trigger", string(b))
}

// view is what every page receives.
type view struct {
	Title   string
	Nav     string // active nav key
	Version string
	Assets  string // fingerprint of the built css and js, for the asset URLs
	Status  scheduler.Status
	Setup   bool // the first-run wizard: no nav, since nothing else is reachable yet
	Data    any
}

func (s *Server) view(title, nav string, data any) view {
	return view{Title: title, Nav: nav, Version: s.Version, Assets: s.tpl.assetTag(), Status: s.status(), Data: data}
}

func (s *Server) status() scheduler.Status {
	if s.Refresher == nil {
		return scheduler.Status{}
	}
	return s.Refresher.Status()
}
