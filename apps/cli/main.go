package main

import (
	"os"

	"github.com/poweur/cli/internal/cli"
	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
)

func main() {
	_ = config.LoadDotEnv(".env")
	// Web-first identity resolution (EPIC-001). Local/dev relays often use HTTP.
	if scheme := os.Getenv("POWEUR_RESOLVER_SCHEME"); scheme != "" {
		allow := os.Getenv("RESOLVER_ALLOW_PRIVATE") == "1" || os.Getenv("RESOLVER_ALLOW_PRIVATE") == "true"
		dial := os.Getenv("POWEUR_RESOLVER_DIAL")
		identity.ConfigureResolver(scheme, allow, dial, nil)
	}
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
