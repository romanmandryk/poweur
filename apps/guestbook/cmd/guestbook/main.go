// Command guestbook runs the reference "Sign in with Poweur ID" relying
// party (EPIC-008 E08-T2).
//
//	GUESTBOOK_ORIGIN=https://guestbook.poweur.net go run ./cmd/guestbook
//
// Locally, against a relay on 127.0.0.1:8080:
//
//	RESOLVER_ALLOW_PRIVATE=1 POWEUR_RESOLVER_SCHEME=http \
//	GUESTBOOK_ORIGIN=http://127.0.0.1:8090 ADDR=127.0.0.1:8090 \
//	go run ./cmd/guestbook
//
// The origin must be the origin browsers actually reach it at: it is the
// value that ends up inside the signed approval, and a mismatch is exactly
// what the audience check is there to catch.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/poweur/guestbook"
)

func main() {
	origin := env("GUESTBOOK_ORIGIN", "http://127.0.0.1:8090")
	addr := env("ADDR", hostPortOf(origin))

	cfg := guestbook.Config{
		Origin: origin,
		Name:   env("GUESTBOOK_NAME", "Poweur Guestbook"),
	}
	if os.Getenv("RESOLVER_ALLOW_PRIVATE") != "" {
		cfg.ResolveOptions.AllowPrivate = true
	}
	if scheme := os.Getenv("POWEUR_RESOLVER_SCHEME"); scheme != "" {
		cfg.ResolveOptions.Scheme = scheme
	}

	srv, err := guestbook.New(cfg)
	if err != nil {
		log.Fatalf("guestbook: %v", err)
	}
	log.Printf("guestbook listening on %s as %s (app namespace apps/%s)", addr, origin, srv.AppID())
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(httpServer.ListenAndServe())
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// hostPortOf turns an origin into a listen address, defaulting the port.
func hostPortOf(origin string) string {
	rest := origin
	for _, prefix := range []string{"https://", "http://"} {
		rest = strings.TrimPrefix(rest, prefix)
	}
	if !strings.Contains(rest, ":") {
		if strings.HasPrefix(origin, "https://") {
			return rest + ":443"
		}
		return rest + ":80"
	}
	return rest
}
