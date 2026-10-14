// Command poweur-email runs the Poweur email bridge (EPIC-023).
//
//	poweur-email serve          run the bridge (configuration from EMAIL_* env)
//	poweur-email check-config   validate the configuration and exit
//	poweur-email version        print the version
//	poweur-email limits list|show|set|reset   one identity's sending limits
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/poweur/email/bridge"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("poweur-email %s", bridge.Version)
		if h := os.Getenv("VERSION_HASH"); h != "" {
			fmt.Printf(" (%s)", h)
		}
		fmt.Println()
		return 0
	case "limits":
		return limits(args[1:])
	case "check-config":
		cfg, err := bridge.FromEnv()
		if err == nil {
			err = cfg.Validate()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid configuration:", err)
			return 1
		}
		fmt.Printf("ok: %s answers for %d domain(s), submits via %s\n", cfg.BridgeID, len(cfg.Domains), cfg.Transport)
		return 0
	case "serve":
		level := slog.LevelInfo
		if os.Getenv("EMAIL_DEBUG") == "1" {
			level = slog.LevelDebug
		}
		log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		cfg, err := bridge.FromEnv()
		if err != nil {
			log.Error("invalid configuration", "err", err)
			return 1
		}
		srv, err := bridge.New(cfg, bridge.Options{Logger: log})
		if err != nil {
			log.Error("cannot start", "err", err)
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := srv.Run(ctx); err != nil {
			log.Error("bridge stopped", "err", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: poweur-email [serve|check-config|version|limits]")
		return 2
	}
}

// limits manages per-identity sending limits in the bridge's database, the
// way `relay quotas set` raises storage: operators grant more on request.
//
//	poweur-email limits list
//	poweur-email limits show <identity>
//	poweur-email limits set <identity> [--daily=N] [--spacing=1m] [--new-recipients=N] [--note=…]
//	poweur-email limits reset <identity>
func limits(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: poweur-email limits list | show <id> | set <id> [--daily=N] [--spacing=1m] [--new-recipients=N] [--note=text] | reset <id>")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	cfg, err := bridge.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:", err)
		return 1
	}
	store, err := bridge.OpenStoreFor(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer store.Close()
	ctx := context.Background()
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	switch args[0] {
	case "list":
		list, err := store.ListLimits(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("defaults: %d a day, spacing %s doubling to %s, %d new recipients a day\n",
			cfg.OutDailyLimit, cfg.OutSpacing, cfg.OutSpacingMax, cfg.OutNewRecipients)
		for _, l := range list {
			fmt.Printf("%s: daily=%d spacing=%s new=%d %s\n", l.Identity, l.Daily, l.Spacing, l.NewDaily, l.Note)
		}
		return 0
	case "show", "reset", "set":
		if len(args) < 2 {
			return usage()
		}
	default:
		return usage()
	}
	id := strings.ToLower(strings.TrimSpace(args[1]))
	switch args[0] {
	case "show":
		l, ok, err := store.LimitsFor(ctx, id)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !ok {
			fmt.Printf("%s uses the defaults\n", id)
			return 0
		}
		_ = out.Encode(l)
	case "reset":
		if err := store.DeleteLimits(ctx, id); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("%s is back on the defaults\n", id)
	case "set":
		fs := flag.NewFlagSet("limits set", flag.ContinueOnError)
		daily := fs.Int("daily", 0, "emails per 24 hours (0: default)")
		spacing := fs.Duration("spacing", 0, "first gap between sends, doubling (0: default)")
		newRcpt := fs.Int("new-recipients", 0, "new correspondents per day (0: default)")
		note := fs.String("note", "", "why (shown in list)")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		l, _, _ := store.LimitsFor(ctx, id)
		l.Identity = id
		if *daily > 0 {
			l.Daily = *daily
		}
		if *spacing > 0 {
			l.Spacing = *spacing
		}
		if *newRcpt > 0 {
			l.NewDaily = *newRcpt
		}
		if *note != "" {
			l.Note = *note
		}
		if err := store.SetLimits(ctx, l); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = out.Encode(l)
	}
	return 0
}
