// Command guestbook runs guestbook.poweur.net (see ../../guestbook).
//
//	ORIGIN=https://guestbook.poweur.net IDENTITY=guestbook.poweur.net \
//	  RELAY_URL=https://relay.poweur.net KEYS_DIR=/keys guestbook
//
// ORIGIN is where visitors reach the site; sign-in is bound to it. IDENTITY is
// the guestbook's own Poweur ID, whose drive holds the log; its keys come from
// KEYS_DIR and its relay from RELAY_URL, as for the poweur CLI. LISTEN is the
// address to serve on (default :8080). ARCHIVE_URL is where the log is public
// (default ORIGIN/pub/guestbook/). GUESTBOOK_NAME titles the page.
//
// With IDENTITY unset and GUESTBOOK_MEMORY=1 the log is kept in memory, for
// trying the page out. Run one instance: two would overwrite each other.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/poweur/demoapps/guestbook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "guestbook:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	origin := strings.TrimRight(os.Getenv("ORIGIN"), "/")
	if origin == "" {
		return errors.New("ORIGIN is required, e.g. https://guestbook.poweur.net")
	}
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	archive := os.Getenv("ARCHIVE_URL")
	if archive == "" {
		archive = origin + "/pub/guestbook/"
	}

	cfg := guestbook.Config{
		Origin:     origin,
		Name:       os.Getenv("GUESTBOOK_NAME"),
		ArchiveURL: archive,
		Log:        os.Stderr,
	}
	switch id := os.Getenv("IDENTITY"); {
	case id != "":
		store := &guestbook.DriveStore{Identity: id}
		if err := store.Init(ctx); err != nil {
			return fmt.Errorf("opening %s's drive: %w", id, err)
		}
		cfg.Store = store
	case os.Getenv("GUESTBOOK_MEMORY") == "1":
		fmt.Fprintln(os.Stderr, "guestbook: keeping entries in memory; they are lost on restart")
	default:
		return errors.New("IDENTITY is required: the guestbook's own Poweur ID (or GUESTBOOK_MEMORY=1 to try it out)")
	}

	srv, err := guestbook.New(cfg)
	if err != nil {
		return err
	}
	if err := srv.Load(ctx); err != nil {
		return fmt.Errorf("reading the log: %w", err)
	}
	go srv.Refresh(ctx, time.Minute)

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "guestbook: serving %s on %s\n", origin, listen)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdown)
	}
}
