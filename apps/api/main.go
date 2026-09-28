package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/relay"
)

func main() { os.Exit(run()) }
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
