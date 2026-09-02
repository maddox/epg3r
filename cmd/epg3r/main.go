// Command epg3r turns IPTV sports playlists into Channels DVR friendly M3U and XMLTV.
package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/jonmaddox/epg3r/internal/app"
	"github.com/jonmaddox/epg3r/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// command is one subcommand. Each owns its flags so `epg3r <cmd> -flag` works.
type command struct {
	help string
	run  func(ctx context.Context, cfg config.Config, args []string) int
}

var commands = map[string]command{
	"serve": {
		help: "run the HTTP server and scheduler (default)",
		run: func(ctx context.Context, cfg config.Config, args []string) int {
			fs := flag.NewFlagSet("serve", flag.ContinueOnError)
			dev := fs.Bool("dev", false, "reload UI templates and static files from the source tree on every request")
			if err := fs.Parse(args); err != nil {
				return 2
			}
			log := app.NewLogger(cfg)
			if err := app.Serve(ctx, cfg, version, *dev, log); err != nil {
				log.Error("fatal", "err", err)
				return 1
			}
			return 0
		},
	},
	"run-once": {
		help: "fetch, parse, and write one refresh, then exit",
		run: func(ctx context.Context, cfg config.Config, _ []string) int {
			log := app.NewLogger(cfg)
			if err := app.RunOnce(ctx, cfg, log); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			return 0
		},
	},
	"parse-title": {
		help: "show how a channel title is parsed: parse-title -group NFL \"NFL 03: A vs B ...\"",
		run: func(ctx context.Context, cfg config.Config, args []string) int {
			fs := flag.NewFlagSet("parse-title", flag.ContinueOnError)
			group := fs.String("group", "", "the playlist group-title the channel is in")
			if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
				fmt.Fprintln(os.Stderr, "usage: epg3r parse-title -group <group> <title>")
				return 2
			}
			if err := app.ParseTitle(ctx, cfg, *group, fs.Arg(0)); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			return 0
		},
	},
	"healthcheck": {
		help: "probe the running server; exit 0 when healthy",
		run: func(ctx context.Context, cfg config.Config, _ []string) int {
			if err := app.Healthcheck(ctx, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "unhealthy:", err)
				return 1
			}
			return 0
		},
	},
	"version": {
		help: "print the version",
		run: func(context.Context, config.Config, []string) int {
			fmt.Println(version)
			return 0
		},
	},
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	name := "serve"
	if len(args) > 0 {
		name = args[0]
		args = args[1:]
	}
	if name == "-h" || name == "--help" || name == "help" {
		usage()
		return 0
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
		usage()
		return 2
	}

	cfg, err := config.FromEnv(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return cmd.run(ctx, cfg, args)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: epg3r <command> [flags]")
	fmt.Fprintln(os.Stderr)
	for _, n := range slices.Sorted(maps.Keys(commands)) {
		fmt.Fprintf(os.Stderr, "  %-12s %s\n", n, commands[n].help)
	}
}
