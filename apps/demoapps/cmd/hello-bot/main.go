// Command hello-bot runs the hello.poweur.net demo bot (see ../../hello).
//
//	IDENTITY=hello.poweur.net RELAY_URL=https://relay.poweur.net KEYS_DIR=/keys hello-bot
//
// HELLO_DEMO_URL, if set, is what the bot's `demo` command links to.
// METRICS_ADDR, if set (":9464"), serves GET /metrics there for Prometheus:
// counts of messages, replies per keyword and errors, never who or what. Keep
// it off the public network.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/poweur/demoapps/appmetrics"
	"github.com/poweur/demoapps/hello"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var metrics *appmetrics.Registry
	if addr := os.Getenv("METRICS_ADDR"); addr != "" {
		metrics = appmetrics.New("poweur_hello")
		go func() {
			if err := metrics.Serve(ctx, addr); err != nil {
				fmt.Fprintln(os.Stderr, "hello-bot: metrics:", err)
			}
		}()
	}
	err := hello.Serve(ctx, hello.Options{
		Identity: os.Getenv("IDENTITY"),
		Log:      os.Stderr,
		DemoURL:  os.Getenv("HELLO_DEMO_URL"),
		Metrics:  metrics,
	})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
