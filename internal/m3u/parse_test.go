package m3u

import (
	"os"
	"strings"
	"testing"
)

func TestParseAttributesAndTitle(t *testing.T) {
	in := "#EXTM3U\r\n" +
		"#EXTINF:-1 tvg-id=\"a.us\" tvg-name=\"NFL 03\" tvg-logo=\"\" group-title=\"NFL\",NFL 03: Steelers vs Falcons (09.08 1:00PM ET) (FOX)\r\n" +
		"http://x/1\r\n" +
		"\r\n" +
		"#EXTINF:-1,Bare Title, With Comma\r\n" +
		"http://x/2\r\n" +
		"#EXTINF:0 group-title=\"A, B\",Group has a comma\r\n" +
		"#EXTVLCOPT:http-user-agent=foo\r\n" +
		"http://x/3\r\n"
	got, err := Parse(strings.NewReader("\uFEFF" + in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	e := got[0]
	if e.Line != 2 || e.Attr("tvg-id") != "a.us" || e.Attr("tvg-name") != "NFL 03" || e.Group() != "NFL" {
		t.Errorf("entry 0 attrs wrong: %+v", e)
	}
	if e.Title != "NFL 03: Steelers vs Falcons (09.08 1:00PM ET) (FOX)" || e.URL != "http://x/1" {
		t.Errorf("entry 0 title/url wrong: %+v", e)
	}
	if got[1].Title != "Bare Title, With Comma" || len(got[1].Attrs) != 0 {
		t.Errorf("entry 1 wrong: %+v", got[1])
	}
	if got[2].Group() != "A, B" || got[2].Title != "Group has a comma" || got[2].URL != "http://x/3" {
		t.Errorf("entry 2 wrong: %+v", got[2])
	}
}

func TestParseExtgrpFallback(t *testing.T) {
	in := "#EXTM3U\n#EXTGRP:Sports\n#EXTINF:-1 tvg-name=\"x\",X\nhttp://x/1\n#EXTINF:-1 group-title=\"Own\",Y\nhttp://x/2\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Group() != "Sports" || got[1].Group() != "Own" {
		t.Errorf("groups wrong: %q %q", got[0].Group(), got[1].Group())
	}
}

func TestParseSkipsOrphanURL(t *testing.T) {
	got, err := Parse(strings.NewReader("#EXTM3U\nhttp://orphan\n#EXTINF:-1,A\nhttp://x/1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "http://x/1" {
		t.Errorf("got %+v", got)
	}
}

func TestParseRealFixture(t *testing.T) {
	f, err := os.Open("../../testdata/m3u/all-sports.m3u")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1338 {
		t.Fatalf("expected 1338 entries, got %d", len(got))
	}
	groups := map[string]int{}
	for _, e := range got {
		groups[e.Group()]++
		if e.URL == "" || e.Title == "" {
			t.Errorf("line %d: empty url or title: %+v", e.Line, e)
		}
	}
	want := map[string]int{"NBA": 267, "NCAA Basketball": 251, "MLB Baseball league": 221, "NFL": 176,
		"NCAAF": 138, "MLS": 100, "WNBA League Pass": 99, "NHL": 86}
	for g, n := range want {
		if groups[g] != n {
			t.Errorf("group %q: got %d want %d", g, groups[g], n)
		}
	}
	if len(groups) != len(want) {
		t.Errorf("unexpected groups: %v", groups)
	}
}
