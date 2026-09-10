package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jonmaddox/epg3r/internal/store"
)

// Until there is a source, every page in the app is furniture around a guide that does not
// exist, so the wizard is the only thing a reader can reach.
func TestSetupGateHoldsTheUIUntilThereIsASource(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()

	for _, path := range []string{"/", "/lineup", "/leagues", "/sources", "/settings", "/runs"} {
		rec := do(h, http.MethodGet, path, nil, false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != setupPath {
			t.Errorf("%s: %d %q, want a redirect to the wizard", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := do(h, http.MethodGet, setupPath, nil, false); rec.Code != http.StatusOK {
		t.Fatalf("the wizard itself: %d", rec.Code)
	}
	// The outputs a consumer polls and the assets a page loads are not the UI, and answer
	// whether or not anyone has finished the wizard.
	for _, path := range []string{"/healthz", "/m3u", "/xmltv", "/static/app.css"} {
		if rec := do(h, http.MethodGet, path, nil, false); rec.Code == http.StatusSeeOther {
			t.Errorf("%s should not be gated", path)
		}
	}

	// Once there is a source the wizard is done, and going back to it goes home instead.
	setUp(t, st)
	if rec := do(h, http.MethodGet, setupPath, nil, false); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("a finished wizard should send a reader home: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(h, http.MethodGet, "/", nil, false); rec.Code != http.StatusOK {
		t.Errorf("dashboard after setup: %d", rec.Code)
	}
}

// The URLs are checked before the reader is let past them. A playlist that cannot be read is
// the likeliest thing to be wrong, and finding out here costs a click rather than a refresh,
// an empty lineup and a trip to the Runs page.
func TestSetupChecksTheURLs(t *testing.T) {
	s, st, _ := uiServer(t)
	h := s.Handler()
	ctx := context.Background()

	for _, tc := range []struct{ name, m3u, xmltv, want string }{
		{"no playlist", "", "", "Enter the playlist link"},
		{"not a url", "provider.example/list.m3u", "", "full http:// or https://"},
		{"playlist will not load", "http://p.example/bad.m3u", "", "playlist could not be read"},
		{"guide will not load", "http://p.example/ok.m3u", "http://p.example/bad.xml", "guide could not be read"},
		{"guide is not a url", "http://p.example/ok.m3u", "nope", "full http:// or https://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, setupPath+"/urls",
				url.Values{"m3u_url": {tc.m3u}, "xmltv_url": {tc.xmltv}}, true)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code = %d, want 422", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("missing %q: %s", tc.want, body)
			}
			// The step is refused, so what was typed comes back to be corrected.
			if tc.m3u != "" && !strings.Contains(body, tc.m3u) {
				t.Errorf("the playlist URL should come back: %s", body)
			}
		})
	}
	if list, _ := st.ListSources(ctx); len(list) != 0 {
		t.Errorf("a refused step wrote something: %+v", list)
	}
}

// A playlist that reads takes the reader to the numbering step, with the defaults filled in
// so it can be passed over without a decision.
func TestSetupSecondStepOffersDefaults(t *testing.T) {
	s, _, _ := uiServer(t)
	h := s.Handler()

	rec := do(h, http.MethodPost, setupPath+"/urls", url.Values{
		"m3u_url": {"http://p.example/ok.m3u"}, "xmltv_url": {"http://p.example/ok.xml"}}, true)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, body)
	}
	for _, want := range []string{
		"Found 42 channels", "500 listings",
		`name="refresh_interval_minutes"`, `value="60"`,
		`name="channel_start"`, `value="10000"`,
		// Carried in the form, so nothing is stored until the last step.
		`name="m3u_url" value="http://p.example/ok.m3u"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
}

// Finishing writes the settings and the source together, and the source is what opens the
// gate, so a rejected setting leaves the reader on the same step with nothing written.
func TestSetupFinishes(t *testing.T) {
	s, st, ref := uiServer(t)
	h := s.Handler()
	ctx := context.Background()
	form := url.Values{
		"m3u_url":                  {"http://provider.example/ok.m3u"},
		"xmltv_url":                {"http://provider.example/ok.xml"},
		"refresh_interval_minutes": {"0"},
		"channel_start":            {"10500"},
	}
	rec := do(h, http.MethodPost, setupPath, form, true)
	// The message names the field as the form labels it, not as the database column the
	// store refused.
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Check for new listings every: must be at least 1") {
		t.Fatalf("a bad interval should be refused: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "refresh_interval_minutes must") {
		t.Error("the refusal should not name a database column")
	}
	if list, _ := st.ListSources(ctx); len(list) != 0 {
		t.Fatalf("a refused finish created a source: %+v", list)
	}

	form.Set("refresh_interval_minutes", "30")
	rec = do(h, http.MethodPost, setupPath, form, true)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("finish: %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	list, _ := st.ListSources(ctx)
	if len(list) != 1 || list[0].URL != "http://provider.example/ok.m3u" || list[0].XMLTVURL != "http://provider.example/ok.xml" {
		t.Fatalf("source: %+v", list)
	}
	// Named after the host, which is the only thing about it the reader has told us.
	if list[0].Name != "provider.example" {
		t.Errorf("name = %q", list[0].Name)
	}
	all, _ := st.Settings(ctx)
	if all[store.SettingRefreshIntervalMinutes] != "30" || all[store.SettingChannelStart] != "10500" {
		t.Errorf("settings: %v", all)
	}
	// The guide starts building at once; there is nothing else for a reader to do next.
	if ref.triggered != 1 {
		t.Errorf("refresh triggered %d times, want 1", ref.triggered)
	}
}

// Going back keeps what was typed, so correcting one URL does not mean typing both again.
func TestSetupBackKeepsWhatWasTyped(t *testing.T) {
	s, _, _ := uiServer(t)
	body := do(s.Handler(), http.MethodPost, setupPath+"/back", url.Values{
		"m3u_url": {"http://p.example/ok.m3u"}, "xmltv_url": {"http://p.example/ok.xml"}}, true).Body.String()
	for _, want := range []string{`value="http://p.example/ok.m3u"`, `value="http://p.example/ok.xml"`, "Check and continue"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
}
