// Package app wires the pieces together and runs the process.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jonmaddox/epg3r/internal/art"
	"github.com/jonmaddox/epg3r/internal/catalog"
	"github.com/jonmaddox/epg3r/internal/config"
	"github.com/jonmaddox/epg3r/internal/model"
	"github.com/jonmaddox/epg3r/internal/pipeline"
	"github.com/jonmaddox/epg3r/internal/release"
	"github.com/jonmaddox/epg3r/internal/scheduler"
	"github.com/jonmaddox/epg3r/internal/store"
	"github.com/jonmaddox/epg3r/internal/titleparse"
	"github.com/jonmaddox/epg3r/internal/web"
)

// NewLogger builds the process logger from config.
func NewLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// App is the assembled application.
type App struct {
	Store   *store.Store
	Catalog *catalog.Catalog
	Runner  *pipeline.Runner
	Log     *slog.Logger
}

// Open assembles the store, catalog, and runner.
func Open(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if n, err := st.FailStaleRuns(ctx); err != nil {
		log.Warn("could not reconcile interrupted runs", "err", err)
	} else if n > 0 {
		log.Info("marked interrupted runs as failed", "runs", n)
	}
	cat, err := catalog.Load()
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	return &App{
		Store:   st,
		Catalog: cat,
		Log:     log,
		Runner: &pipeline.Runner{
			Store: st, Catalog: cat, Log: log,
			Fetcher: &pipeline.Fetcher{CacheDir: filepath.Join(cfg.DataDir, "cache", "sources")},
		},
	}, nil
}

// Close releases resources.
func (a *App) Close() error { return a.Store.Close() }

// Serve runs the HTTP server and scheduler until ctx is canceled. With dev set the
// UI's templates and static files are read from the source tree on every request.
func Serve(ctx context.Context, cfg config.Config, version string, dev bool, log *slog.Logger) error {
	app, err := Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Close()

	// The UI shows times in the configured default zone; the store caches it.
	zone := func() *time.Location { return app.Store.Location(context.Background()) }
	srv := web.New(version, log, dev, zone)
	srv.Store, srv.Catalog = app.Store, app.Catalog
	srv.TestSource, srv.TestGuide = app.Runner.Probe, app.Runner.ProbeGuide
	// Whether a newer container exists. Looked up on a timer rather than on a request, so
	// nobody's page load waits on GitHub.
	updates := release.New(release.Repo, version, log)
	updates.Start(ctx)
	srv.Update = updates.Status
	srv.Snapshots.GuideTags = func() bool {
		v, _ := app.Store.SettingBool(context.Background(), store.SettingM3UTvcGuideTags)
		return v
	}
	srv.Art, err = art.New(art.Options{
		Catalog:  app.Catalog,
		CacheDir: filepath.Join(cfg.DataDir, "cache", "art"),
		Log:      log,
		Dev:      dev,
	})
	if err != nil {
		return err
	}
	srv.PublicBase = func() string {
		v, _ := app.Store.Setting(context.Background(), store.SettingPublicBaseURL)
		return v
	}

	// Serve the last good output immediately, before the first refresh finishes.
	var last model.Snapshot
	if runID, ok, err := app.Store.LatestSnapshot(ctx, &last); err != nil {
		log.Warn("could not load last snapshot", "err", err)
	} else if ok {
		srv.Snapshots.Set(&last)
		log.Info("serving last snapshot", "run", runID, "channels", len(last.Channels))
	}

	sched := scheduler.New(
		func() time.Duration {
			d, err := app.Store.RefreshInterval(context.Background())
			if err != nil {
				return time.Hour
			}
			return d
		},
		func(ctx context.Context, trigger store.Trigger) (store.RunStatus, error) {
			snap, rep, err := app.Runner.Run(ctx, trigger)
			if snap != nil && rep != nil && rep.Status != store.RunFailed {
				srv.Snapshots.Set(snap)
			}
			if rep == nil {
				return "", err
			}
			return rep.Status, err
		},
		log,
	)
	app.Runner.Phase = sched.SetPhase
	srv.Refresher = sched
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		// Always rebuild on start. A guide that is stale on boot is the one thing a
		// consumer cannot work around, and a refresh costs a fetch.
		sched.Start(ctx, true)
	}()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "version", version, "data_dir", cfg.DataDir)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	// Let an in-flight refresh unwind before the deferred Close drops the database.
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
		log.Warn("refresh still running at shutdown; closing anyway")
	}
	return err
}

// RunOnce performs a single refresh and prints the report.
func RunOnce(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	app, err := Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Close()
	snap, rep, err := app.Runner.Run(ctx, store.TriggerManual)
	if rep != nil {
		fmt.Printf("run %d: %s in %s\n", rep.RunID, rep.Status, rep.Duration.Round(time.Millisecond))
		fmt.Printf("  %d entries:", rep.Counts.Seen())
		for _, o := range slices.Sorted(maps.Keys(rep.Counts)) {
			fmt.Printf(" %s %d,", o, rep.Counts[o])
		}
		fmt.Println()
		for _, p := range rep.Problems {
			fmt.Println("  problem:", p)
		}
	}
	if snap != nil {
		fmt.Printf("  %d channels in the guide\n", len(snap.Channels))
	}
	return err
}

// ParseTitle runs one title through the parser and prints the result as JSON. It is
// the quickest way to see what the app makes of a provider's format.
func ParseTitle(ctx context.Context, cfg config.Config, group, title string) error {
	app, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	defer app.Close()
	lg, ok := app.Catalog.MatchLeague(group, title)
	if !ok {
		return fmt.Errorf("group %q does not match any league", group)
	}
	res := titleparse.Parse(titleparse.Context{Now: time.Now(), Loc: lg.Location(app.Store.Location(ctx)), League: lg, Catalog: app.Catalog}, title)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		League string `json:"league"`
		titleparse.Result
	}{lg.Key, res})
}

// Healthcheck probes the running server; used by the Docker HEALTHCHECK.
func Healthcheck(ctx context.Context, cfg config.Config) error {
	url := "http://" + cfg.LoopbackAddr() + web.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return nil
}
