package store

import (
	"context"
	"strconv"
	"testing"
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

// A re-homed channel keeps the id a consumer already knows it by. Channels DVR keys
// recordings to the channel id, so an id that churns silently breaks them — this is the one
// thing in the whole numbering path that must not move.
func TestAssignNumbersKeepsAnExistingID(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	key, source := numbered(t, s, "a", "NFL 04", 10004)

	// A row that knows its identity but has no number. Every writer that produces this does
	// so inside a transaction, so it is built here directly: the point of the test is what
	// AssignNumbers does when handed one, not how one comes about.
	if _, err := s.w.ExecContext(ctx, `UPDATE channels SET number = NULL WHERE key = ?`, key); err != nil {
		t.Fatal(err)
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

// shelfMove writes the start and translates the numbers, the way the settings page does.
func shelfMove(t *testing.T, s *Store, from, to, delta int) map[string]int {
	t.Helper()
	problems, moved, err := s.SetSettingsMoving(context.Background(),
		map[string]string{SettingChannelStart: strconv.Itoa(from + delta)},
		&Shelf{From: from, To: to, Delta: delta})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	return moved
}

// Moving the shelf up by one is every channel taking the number of the one before it, which
// collides if applied a row at a time. Every destination is vacated first.
func TestShelfSlidesOntoItself(t *testing.T) {
	s := openTemp(t)
	var keys []string
	for i := range 4 {
		key, _ := numbered(t, s, string(rune('a'+i)), "NFL 0"+string(rune('1'+i)), 10001+i)
		keys = append(keys, key)
	}

	moved := shelfMove(t, s, 10000, 18000, 1)
	if len(moved) != 4 {
		t.Fatalf("moved %d channels, want 4", len(moved))
	}
	for i, key := range keys {
		if moved[key] != 10002+i {
			t.Errorf("channel %d landed on %d, want %d", i, moved[key], 10002+i)
		}
	}
}

// A number someone chose moves with the shelf and stays theirs. The move relocates the whole
// range; it is not a decision to take the number back.
func TestShelfMoveCarriesAHandSetNumber(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	key, source := numbered(t, s, "a", "NFL 04", 10004)
	if err := s.SetChannelNumbers(ctx, map[string]int{key: 10500}); err != nil {
		t.Fatal(err)
	}

	moved := shelfMove(t, s, 10000, 18000, 1000)
	if moved[key] != 11500 {
		t.Errorf("moved to %d, want 11500", moved[key])
	}
	all, err := s.Channels(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if !all[key].ByUser {
		t.Error("the number is still the one the user chose, just somewhere else")
	}
	if all[key].ChannelID != "NFL 04" {
		t.Errorf("id = %q, want it unchanged", all[key].ChannelID)
	}
}

// A destination held by a channel outside the shelf is a conflict, and the whole move is
// abandoned — the setting included — rather than half-applied.
func TestShelfMoveRefusesAnOutsideCollision(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nfl, nflSource := numbered(t, s, "a", "NFL 04", 10004)
	stray, straySource := numbered(t, s, "b", "Local 4", 3004) // outside the shelf, in the way

	_, _, err := s.SetSettingsMoving(ctx, map[string]string{SettingChannelStart: "3000"},
		&Shelf{From: 10000, To: 18000, Delta: -7000})
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
		t.Errorf("a refused move changed something: %d %d", left[nfl].Number, blocking[stray].Number)
	}
	// And the setting did not land either, or the page would disagree with the guide.
	got, err := s.Setting(ctx, SettingChannelStart)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10000" {
		t.Errorf("start = %s, want the move to have rolled it back", got)
	}
}
