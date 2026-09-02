// Package scheduler runs refreshes on an interval and on demand, one at a time.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
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
	status atomic.Pointer[Status]
	now    func() time.Time
}

// New builds a scheduler.
func New(interval func() time.Duration, run func(ctx context.Context, trigger store.Trigger) (store.RunStatus, error), log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{Interval: interval, Run: run, Log: log, wake: make(chan store.Trigger, 1), now: time.Now}
	s.status.Store(&Status{})
	return s
}

// update publishes a modified copy of the status. Every update allocates a fresh
// struct so readers holding the previous pointer never observe a write.
func (s *Scheduler) update(fn func(st *Status)) {
	st := *s.status.Load()
	fn(&st)
	s.status.Store(&st)
}

// SetPhase updates the progress text shown while running.
func (s *Scheduler) SetPhase(phase string) { s.update(func(st *Status) { st.Phase = phase }) }

// Status returns a copy of the current state.
func (s *Scheduler) Status() Status { return *s.status.Load() }

// Trigger requests a run now. It returns ErrRunning if one is in progress and does
// not queue more than one pending request. Runs only ever happen on the Start
// goroutine, so no lock is needed.
func (s *Scheduler) Trigger(trigger store.Trigger) error {
	if s.Status().Running {
		return ErrRunning
	}
	select {
	case s.wake <- trigger:
	default:
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
