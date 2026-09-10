package store

import (
	"context"
	"testing"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// numbered puts a channel in the guide with a derived number, the state every channel is in
// before anyone touches it.
func numbered(t *testing.T, s *Store, url, channelID string, number int) (key string, source int64) {
	t.Helper()
	ctx := context.Background()
	id, err := s.CreateSource(ctx, NewSource{Name: url, URL: "http://p/" + url})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SeeChannels(ctx, id, []string{"http://x/" + url}); err != nil {
		t.Fatal(err)
	}
	key = ChannelKey(id, "http://x/"+url)
	base := number - number%1000
	got, err := s.AssignNumbers(ctx, []Assignment{{
		Key: key, PreferredID: channelID, PreferredNumber: number, Base: base, Limit: base + 800,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got[key].ChannelID != channelID || got[key].Number != number {
		t.Fatalf("setup: %+v", got[key])
	}
	return key, id
}

func startAt(n string) catalog.Override { return catalog.Override{ChannelBase: &n} }

// A re-homed channel keeps the id a consumer already knows it by. Channels DVR keys
// recordings to the channel id, so an id that churns silently breaks them — this is the one
// thing in the whole numbering path that must not move.
func TestAssignNumbersKeepsAnExistingID(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	key, source := numbered(t, s, "a", "NFL 04", 10004)

	// A number someone chose, which handing the league to epg3r discards.
	if err := s.SetChannelNumbers(ctx, map[string]int{key: 10500}); err != nil {
		t.Fatal(err)
	}
	_, cleared, err := s.RehomeLeague(ctx, Rehome{
		Key: "nfl", Override: startAt("3000"), Keys: []string{key}, DropByUser: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 || cleared[0] != key {
		t.Fatalf("cleared %v, want the hand-set channel", cleared)
	}
	all, err := s.Channels(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if all[key].Number != 0 || all[key].ChannelID != "NFL 04" {
		t.Fatalf("want the number freed and the id kept: %+v", all[key])
	}

	// Now the run places it again. Its own id is in the used set, so without KeepID the
	// de-duplication would rename it "NFL 04 2" — a channel renaming itself because it
	// already exists.
	got, err := s.AssignNumbers(ctx, []Assignment{{
		Key: key, PreferredID: "NFL 04", PreferredNumber: 3004, Base: 3000, Limit: 3800, KeepID: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got[key].ChannelID != "NFL 04" {
		t.Errorf("id = %q, want it unchanged", got[key].ChannelID)
	}
	if got[key].Number != 3004 {
		t.Errorf("number = %d, want 3004", got[key].Number)
	}
}

// KeepID only fills in a number. A channel that has taken one back since the queue was built
// is left alone.
func TestAssignNumbersLeavesANumberedChannelAlone(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	key, _ := numbered(t, s, "a", "NFL 04", 10004)

	got, err := s.AssignNumbers(ctx, []Assignment{{
		Key: key, PreferredID: "NFL 04", PreferredNumber: 3004, Base: 3000, Limit: 3800, KeepID: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got[key].Number != 10004 {
		t.Errorf("number = %d, want the 10004 it already held", got[key].Number)
	}
}

// A league shifting up by one is every channel taking the number of the one before it, which
// collides if applied a row at a time. Every destination is vacated first.
func TestRehomeSlidesABlockOntoItself(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	var keys []string
	for i := range 4 {
		key, _ := numbered(t, s, string(rune('a'+i)), "NFL 0"+string(rune('1'+i)), 10001+i)
		keys = append(keys, key)
	}

	moved, _, err := s.RehomeLeague(ctx, Rehome{
		Key: "nfl", Override: startAt("10001"), From: 10000, To: 11000, Delta: 1, Keys: keys}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 4 {
		t.Fatalf("moved %d channels, want 4", len(moved))
	}
	for i, key := range keys {
		if moved[key] != 10002+i {
			t.Errorf("channel %d landed on %d, want %d", i, moved[key], 10002+i)
		}
	}
	// A translated number is derived from the league's start, not chosen by anyone.
	all, err := s.Channels(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range all {
		if c.ByUser {
			t.Errorf("%s should no longer be marked hand-set", c.ChannelID)
		}
	}
}

// A destination held by a channel that is not moving is a conflict, and the whole re-home is
// abandoned — the override included — rather than half-applied.
func TestRehomeRefusesAnOutsideCollision(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nfl, nflSource := numbered(t, s, "a", "NFL 04", 10004)
	stray, straySource := numbered(t, s, "b", "MLB 04", 3004) // in the way

	_, _, err := s.RehomeLeague(ctx, Rehome{
		Key: "nfl", Override: startAt("3000"), From: 10000, To: 11000, Delta: -7000, Keys: []string{nfl}}, nil)
	if err == nil {
		t.Fatal("moving onto an occupied number was allowed")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("want a ValidationError, got %T: %v", err, err)
	}
	left, err := s.Channels(ctx, nflSource)
	if err != nil {
		t.Fatal(err)
	}
	blocking, err := s.Channels(ctx, straySource)
	if err != nil {
		t.Fatal(err)
	}
	if left[nfl].Number != 10004 || blocking[stray].Number != 3004 {
		t.Errorf("a refused re-home changed something: %d %d", left[nfl].Number, blocking[stray].Number)
	}
	// And the setting did not land either, or the page would disagree with the guide.
	overrides, err := s.LeagueOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, stored := overrides["nfl"]; stored {
		t.Error("the override was written despite the move failing")
	}
}

// Handing a league over discards the numbers a person chose and leaves the derived ones to be
// translated, so only the hand-set rows come back for a new number.
func TestRehomeDiscardsOnlyHandSetNumbers(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	derived, derivedSource := numbered(t, s, "a", "NFL 04", 10004)
	chosen, _ := numbered(t, s, "b", "NFL 05", 10005)
	if err := s.SetChannelNumbers(ctx, map[string]int{chosen: 10700}); err != nil {
		t.Fatal(err)
	}

	moved, cleared, err := s.RehomeLeague(ctx, Rehome{
		Key: "nfl", Override: startAt("3000"), From: 10000, To: 11000, Delta: -7000,
		Keys: []string{derived, chosen}, DropByUser: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 || cleared[0] != chosen {
		t.Fatalf("cleared %v, want just the hand-set one", cleared)
	}
	if moved[derived] != 3004 {
		t.Errorf("the derived channel should have moved with the league: %d", moved[derived])
	}
	all, err := s.Channels(ctx, derivedSource)
	if err != nil {
		t.Fatal(err)
	}
	if all[derived].Number != 3004 {
		t.Errorf("derived number = %d, want 3004", all[derived].Number)
	}
}
