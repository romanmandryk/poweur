// Command poweur-email runs the Poweur email bridge (EPIC-023).
//
//	poweur-email serve          run the bridge (configuration from EMAIL_* env)
//	poweur-email check-config   validate the configuration and exit
//	poweur-email version        print the version
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
		fmt.Fprintln(os.Stderr, "usage: poweur-email [serve|check-config|version]")
		return 2
	}
}
