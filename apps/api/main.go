package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/migrate"
	"github.com/poweur/api/internal/relay"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate-v1" {
		os.Exit(migrateV1(os.Args[2:]))
	}
	os.Exit(run())
}

// migrateV1 converts a v1 POWEUR_DATA to the v2 layout; run it with the relay
// stopped. See internal/migrate.
func migrateV1(args []string) int {
	flags := flag.NewFlagSet("migrate-v1", flag.ContinueOnError)
	data := flags.String("data", os.Getenv("POWEUR_DATA"), "the relay's data directory")
	dry := flags.Bool("dry-run", false, "read and report, write nothing")
	keep := flags.Bool("keep-source", false, "leave the v1 trees in place instead of renaming them to *.v1-backup")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	report, err := migrate.Run(context.Background(), migrate.Options{DataDir: *data, DryRun: *dry, KeepSource: *keep, Log: os.Stderr})
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-v1:", err)
		return 1
	}
	return 0
}
func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := config.LoadDotEnv(".env"); err != nil {
		logger.Warn("dotenv unavailable")
	}
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid relay configuration", "error", err.Error())
		return 1
	}
	server := relay.NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	if err := server.DriveError(); err != nil {
		logger.Error("drive storage unavailable", "error", err.Error())
		return 1
	}
	if err := server.StartTelemetry(context.Background(), os.Stdout); err != nil {
		logger.Error("telemetry initialization failed")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: server.Router(), ReadHeaderTimeout: 10 * time.Second}
	failures := make(chan error, 1)
	go func() { failures <- httpServer.ListenAndServe() }()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped")
			code = 1
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	flush, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	_ = server.Close(flush)
	return code
}
