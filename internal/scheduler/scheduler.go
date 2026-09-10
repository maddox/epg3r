// Package scheduler runs refreshes on an interval and on demand, one at a time.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jonmaddox/epg3r/internal/store"
)

// ErrRunning is returned by Trigger while a refresh is in progress.
var ErrRunning = errors.New("a refresh is already running")

// RetryAfterFailure is how soon a failed refresh is retried, instead of waiting the
// full interval. Providers hiccup and containers start before their networks do.
const RetryAfterFailure = time.Minute

// Status is what the UI shows about the scheduler.
type Status struct {
	Running    bool
	Phase      string
	StartedAt  time.Time
	LastRunAt  time.Time
	LastStatus store.RunStatus // "" until the first run finishes
	LastError  string
	NextAt     time.Time
}

// Scheduler owns the refresh loop.
type Scheduler struct {
	Interval func() time.Duration // read every cycle so settings changes apply without restart
	Run      func(ctx context.Context, trigger store.Trigger) (store.RunStatus, error)
	Log      *slog.Logger

	wake   chan store.Trigger
	mu     sync.Mutex // guards status and queued
	status Status
	queued *store.Trigger // a request that arrived mid-run, to be honoured after it
	now    func() time.Time
}

// New builds a scheduler.
func New(interval func() time.Duration, run func(ctx context.Context, trigger store.Trigger) (store.RunStatus, error), log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{Interval: interval, Run: run, Log: log, wake: make(chan store.Trigger, 1), now: time.Now}
}

// update applies a change to the status under the lock; callable from any goroutine.
func (s *Scheduler) update(fn func(st *Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
}

// SetPhase updates the progress text shown while running.
func (s *Scheduler) SetPhase(phase string) { s.update(func(st *Status) { st.Phase = phase }) }

// Status returns a copy of the current state.
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Trigger requests a run now. It returns ErrRunning if one is in progress — but the
// request is not lost: a run that is already under way has read its settings, so the
// change that prompted this would otherwise wait for the next interval. It is held and
// honoured as soon as the current run finishes. The check and the "queued" status are
// one update, so two concurrent triggers cannot both pass.
func (s *Scheduler) Trigger(trigger store.Trigger) error {
	var was bool
	s.mu.Lock()
	was = s.status.Running
	if was {
		s.queued = &trigger
	} else {
		s.status.Running, s.status.Phase, s.status.StartedAt = true, "queued", s.now()
	}
	s.mu.Unlock()
	if was {
		return ErrRunning
	}
	select {
	case s.wake <- trigger:
	default: // a wake is already pending; the loop will run once regardless
	}
	return nil
}

// Start runs the loop until ctx is done. When runOnStart is set the first refresh
// happens immediately.
func (s *Scheduler) Start(ctx context.Context, runOnStart bool) {
	if runOnStart {
		s.runOnce(ctx, store.TriggerStartup)
	}
	for {
		interval := s.Interval()
		if interval < time.Minute {
			interval = time.Minute
		}
		if s.Status().LastStatus == store.RunFailed && interval > RetryAfterFailure {
			interval = RetryAfterFailure
		}
		next := s.now().Add(interval)
		s.update(func(st *Status) { st.NextAt = next })

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runOnce(ctx, store.TriggerSchedule)
		case trigger := <-s.wake:
			timer.Stop()
			s.runOnce(ctx, trigger)
		}
		// Anything asked for while that run was going has not been seen by it.
		for {
			s.mu.Lock()
			q := s.queued
			s.queued = nil
			s.mu.Unlock()
			if q == nil || ctx.Err() != nil {
				break
			}
			s.runOnce(ctx, *q)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, trigger store.Trigger) {
	started := s.now()
	s.update(func(st *Status) { st.Running, st.StartedAt, st.Phase = true, started, "starting" })

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	status, err := s.Run(runCtx, trigger)
	cancel()

	finished := s.now()
	s.update(func(st *Status) {
		st.Running, st.Phase, st.LastRunAt, st.LastStatus, st.LastError = false, "", finished, status, ""
		if err != nil {
			st.LastError = err.Error()
			if status == "" {
				st.LastStatus = store.RunFailed
			}
		}
	})
	if err != nil {
		s.Log.Error("refresh failed", "trigger", trigger, "err", err)
	}
}
