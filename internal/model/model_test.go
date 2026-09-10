package model

import "testing"

// Art is stored as a path so that nothing persisted has to know the host it will be served
// from. Abs is the one place a host is applied, and it must leave alone anything that is
// not ours to rewrite.
func TestAbs(t *testing.T) {
	for _, tc := range []struct{ base, ref, want string }{
		{"http://epg3r.test", "/art/league/nfl.png", "http://epg3r.test/art/league/nfl.png"},
		{"http://epg3r.test/", "/art/league/nfl.png", "http://epg3r.test/art/league/nfl.png"},
		{"http://epg3r.test///", "/art/league/nfl.png", "http://epg3r.test/art/league/nfl.png"},
		{"http://epg3r.test", "https://cdn.invalid/x.png", "https://cdn.invalid/x.png"},
		{"http://epg3r.test", "", ""},
		{"", "/art/league/nfl.png", "/art/league/nfl.png"},
		{"", "", ""},
	} {
		if got := Abs(tc.base, tc.ref); got != tc.want {
			t.Errorf("Abs(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
		}
	}
}
