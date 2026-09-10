package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonmaddox/epg3r/internal/store"
)

func TestTriggerAndSingleFlight(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	s := New(func() time.Duration { return time.Hour }, func(ctx context.Context, trigger store.Trigger) (store.RunStatus, error) {
		if runs.Add(1) == 1 {
			<-release // hold the first run open so a second trigger lands mid-run
		}
		return store.RunOK, nil
	}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Start(ctx, false); close(done) }()

	if err := s.Trigger(store.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Running || st.Phase != "queued" {
		t.Fatalf("Trigger should report the run as underway immediately: %+v", st)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Status().Phase == "queued" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// A request that arrives mid-run is refused for now, because a run is under way and
	// has already read its settings — but it is not lost. It runs once that one is done,
	// so a change saved during a refresh reaches the outputs rather than waiting for the
	// next interval.
	if err := s.Trigger(store.TriggerManual); !errors.Is(err, ErrRunning) {
		t.Errorf("second trigger while running: %v", err)
	}
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for (s.Status().Running || runs.Load() < 2) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := s.Status()
	if st.Running || st.LastStatus != store.RunOK || st.LastRunAt.IsZero() || st.NextAt.IsZero() {
		t.Errorf("status after run: %+v", st)
	}
	if runs.Load() != 2 {
		t.Errorf("runs = %d, want 2 (the held request runs after the one that blocked it)", runs.Load())
	}
	cancel()
	<-done
}

func TestRunOnStartAndErrors(t *testing.T) {
	s := New(func() time.Duration { return time.Hour }, func(context.Context, store.Trigger) (store.RunStatus, error) {
		return "", errors.New("boom")
	}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	s.Start(ctx, true)
	st := s.Status()
	if st.LastStatus != store.RunFailed || st.LastError != "boom" {
		t.Errorf("status: %+v", st)
	}
}

func TestFailedRunRetriesSooner(t *testing.T) {
	s := New(func() time.Duration { return time.Hour }, func(context.Context, store.Trigger) (store.RunStatus, error) {
		return "", errors.New("provider down")
	}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	before := time.Now()
	s.Start(ctx, true)
	st := s.Status()
	if st.LastStatus != store.RunFailed {
		t.Fatalf("status %+v", st)
	}
	if wait := st.NextAt.Sub(before); wait > RetryAfterFailure+time.Second {
		t.Errorf("after a failure the next attempt should be in about %s, got %s", RetryAfterFailure, wait)
	}
}
