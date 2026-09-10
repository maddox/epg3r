package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/art"
	"github.com/jonmaddox/epg3r/internal/store"
)

// GuideTester probes an XMLTV URL and reports how many programs it holds.
type GuideTester func(ctx context.Context, url string) (int, error)

// setupPath is the wizard, and the only page reachable until it is finished.
const setupPath = "/setup"

// setupPage is the wizard's state. It is carried in the form rather than stored, so
// nothing is written until the last step and a reader who walks away leaves nothing behind.
type setupPage struct {
	Step     int
	M3U      string
	XMLTV    string
	Channels int // what the playlist probe found, shown back as reassurance
	Programs int
	Interval string
	Start    string
	Error    string
}

// setupNeeded reports whether the app has never been pointed at a playlist. Every page
// other than the wizard is furniture around a guide, so until there is a source there is
// nothing for a reader to do anywhere else.
func (s *Server) setupNeeded(ctx context.Context) bool {
	has, err := s.Store.HasSources(ctx)
	if err != nil {
		s.Log.Error("could not check for sources", "err", err)
		return false // never trap someone in a wizard because a query failed
	}
	return !has
}

// setupURLs reads the two URLs the wizard carries in its form from step to step.
func setupURLs(r *http.Request) (m3u, xmltv string) {
	return strings.TrimSpace(r.FormValue("m3u_url")), strings.TrimSpace(r.FormValue("xmltv_url"))
}

// setupGate sends a reader to the wizard until it is done, and away from it afterwards.
// Only the admin UI is gated: the outputs a consumer polls, the art a guide points at and
// the assets a page loads all answer as they always do.
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Store != nil && isUIPath(r.URL.Path) {
			inWizard := strings.HasPrefix(r.URL.Path, setupPath)
			switch needed := s.setupNeeded(r.Context()); {
			case needed && !inWizard:
				redirect(w, r, setupPath)
				return
			case !needed && inWizard:
				redirect(w, r, "/")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isUIPath reports whether a path belongs to the admin UI rather than to the outputs a
// consumer polls or the assets a page loads.
func isUIPath(p string) bool {
	for _, prefix := range []string{"/static", art.Prefix, XMLTVPath, M3UPath, HealthPath, "/epg.xml"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return false
		}
	}
	return true
}

// redirect sends a browser somewhere else, and tells HTMX to do the same rather than
// swapping a whole page into whatever element made the request.
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, "setup", s.setupView(setupPage{Step: 1}))
}

// setupView renders the wizard without the nav, since none of it leads anywhere yet.
func (s *Server) setupView(p setupPage) view {
	v := s.view("Setup", "setup", p)
	v.Setup = true
	return v
}

// handleSetupURLs checks the URLs before letting the reader past them. A playlist that
// cannot be fetched or parsed is the single most likely thing to be wrong, and finding out
// here costs one click rather than a refresh, an empty lineup and a trip to the Runs page.
func (s *Server) handleSetupURLs(w http.ResponseWriter, r *http.Request) {
	p := setupPage{Step: 2}
	p.M3U, p.XMLTV = setupURLs(r)
	refuse := func(msg string) {
		p.Step, p.Error = 1, msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.partial(w, r, "setup", "setup_form", p)
	}
	if p.M3U == "" {
		refuse("Enter the playlist URL your provider gave you.")
		return
	}
	if err := checkURL(p.M3U); err != nil {
		refuse("Playlist URL: " + err.Error())
		return
	}
	if p.XMLTV != "" {
		if err := checkURL(p.XMLTV); err != nil {
			refuse("Guide URL: " + err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if s.TestSource == nil {
		refuse("Testing is not available.")
		return
	}
	n, err := s.TestSource(ctx, p.M3U)
	if err != nil {
		refuse("That playlist could not be read: " + err.Error())
		return
	}
	p.Channels = n
	if p.XMLTV != "" && s.TestGuide != nil {
		n, err := s.TestGuide(ctx, p.XMLTV)
		if err != nil {
			refuse("That guide could not be read: " + err.Error())
			return
		}
		p.Programs = n
	}

	// Defaults, shown filled in so the next step can be passed over without a decision.
	p.Interval = store.SettingDefault(store.SettingRefreshIntervalMinutes)
	p.Start = store.SettingDefault(store.SettingChannelStart)
	s.partial(w, r, "setup", "setup_form", p)
}

// handleSetupBack returns to the first step with what was typed, so correcting a URL does
// not mean typing both again.
func (s *Server) handleSetupBack(w http.ResponseWriter, r *http.Request) {
	p := setupPage{Step: 1}
	p.M3U, p.XMLTV = setupURLs(r)
	s.partial(w, r, "setup", "setup_form", p)
}

// handleSetupFinish writes everything the wizard collected, in the order that leaves the
// least behind if a step fails: the settings first, then the source, which is what opens the
// gate. A half-finished wizard shows the reader the same step again rather than a working
// app with settings nobody chose.
func (s *Server) handleSetupFinish(w http.ResponseWriter, r *http.Request) {
	p := setupPage{
		Step:     2,
		Interval: strings.TrimSpace(r.FormValue(store.SettingRefreshIntervalMinutes)),
		Start:    strings.TrimSpace(r.FormValue(store.SettingChannelStart)),
	}
	p.M3U, p.XMLTV = setupURLs(r)
	if p.M3U == "" { // only reachable by posting here directly
		redirect(w, r, setupPath)
		return
	}
	refuse := func(msg string) {
		p.Error = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.partial(w, r, "setup", "setup_form", p)
	}
	problems, err := s.Store.SetSettings(r.Context(), map[string]string{
		store.SettingRefreshIntervalMinutes: p.Interval,
		store.SettingChannelStart:           p.Start,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		refuse(firstProblem(problems))
		return
	}
	if _, err := s.Store.CreateSource(r.Context(), store.NewSource{
		Name: sourceName(p.M3U), URL: p.M3U, XMLTVURL: p.XMLTV,
	}); err != nil {
		if msg, fatal := s.storeErr(w, r, err); !fatal {
			refuse(msg)
		}
		return
	}
	s.refreshSoon()
	toast(w, "ok", "Building your guide now.")
	redirect(w, r, "/")
}

// checkURL rejects what a browser would not fetch, so an obvious typo is caught without
// waiting on a request that was never going to work.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be a full http:// or https:// address")
	}
	return nil
}

// sourceName names the source after the host it came from, which is the only thing about it
// the reader has told us and reads better on the Sources page than "Default".
func sourceName(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return "Provider"
}

// firstProblem turns a rejected setting into a sentence a reader can act on. The store names
// the key it refused, which is a database column rather than what the field is called on the
// form. Registry order, so the same input always reports the same problem first.
func firstProblem(problems map[string]string) string {
	for _, d := range store.SettingDefs {
		if msg, ok := problems[d.Key]; ok {
			return strings.Replace(msg, d.Key, d.Label, 1)
		}
	}
	return "That could not be saved."
}
