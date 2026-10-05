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
// METRICS_ADDR, if set (":9464"), serves GET /metrics there for Prometheus:
// requests by route, posts and sign-ins by outcome, and errors, never who or
// what. It is not authenticated: keep it off the public network.
//
// Local development only: POWEUR_RESOLVER_SCHEME=http, RESOLVER_ALLOW_PRIVATE=1 and
// GUESTBOOK_RESOLVER_DIAL=host:port send every identity lookup to one local relay, as the
// poweur CLI and the OAuth bridge do. Production sets none of them.
//
// Moderation, on the host while the guestbook runs (same environment):
//
//	guestbook entries list
//	guestbook entries remove <id> [--identity alice.poweur.net] [--at 2026-10-01T19:23] [--yes]
//
// remove only says what it would do until --yes is given.
//
// With IDENTITY unset and GUESTBOOK_MEMORY=1 the log is kept in memory, for
// trying the page out. Run one instance: two would overwrite each other.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/poweur/demoapps/appmetrics"
	"github.com/poweur/demoapps/guestbook"
	"github.com/poweur/identity"
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
	if len(os.Args) > 1 && os.Args[1] == "entries" {
		return admin(ctx, os.Args[2:])
	}
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
	resolve, err := resolveOptions()
	if err != nil {
		return err
	}
	cfg.ResolveOptions = resolve
	if addr := os.Getenv("METRICS_ADDR"); addr != "" {
		reg := appmetrics.New("poweur_guestbook")
		cfg.Metrics = reg
		go func() {
			if err := reg.Serve(ctx, addr); err != nil {
				fmt.Fprintln(os.Stderr, "guestbook: metrics:", err)
			}
		}()
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

// resolveOptions is the identity resolver's configuration: the published chain unless the
// local development variables ask for one relay.
func resolveOptions() (identity.ResolveOptions, error) {
	opts := identity.ResolveOptions{Scheme: os.Getenv("POWEUR_RESOLVER_SCHEME"), AllowPrivate: os.Getenv("RESOLVER_ALLOW_PRIVATE") == "1"}
	if dial := os.Getenv("GUESTBOOK_RESOLVER_DIAL"); dial != "" {
		if !opts.AllowPrivate {
			return opts, errors.New("GUESTBOOK_RESOLVER_DIAL is for local development and needs RESOLVER_ALLOW_PRIVATE=1")
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		opts.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, dial)
			}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
		}
	}
	return opts, nil
}

// admin runs `guestbook entries …` against the same drive the server writes.
func admin(ctx context.Context, args []string) error {
	id := os.Getenv("IDENTITY")
	if id == "" {
		return errors.New("IDENTITY is required: the guestbook's own Poweur ID")
	}
	store := &guestbook.DriveStore{Identity: id}
	if code := guestbook.RunAdmin(ctx, store, args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
	return nil
}
