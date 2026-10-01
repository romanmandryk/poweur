// Command hello-bot runs the hello.poweur.net demo bot (see ../../hello).
//
//	IDENTITY=hello.poweur.net RELAY_URL=https://relay.poweur.net KEYS_DIR=/keys hello-bot
//
// HELLO_DEMO_URL, if set, is what the bot's `demo` command links to.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/poweur/demoapps/hello"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := hello.Serve(ctx, hello.Options{
		Identity: os.Getenv("IDENTITY"),
		Log:      os.Stderr,
		DemoURL:  os.Getenv("HELLO_DEMO_URL"),
	})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
