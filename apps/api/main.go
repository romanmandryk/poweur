package main

import (
	"log"
	"net/http"
	"os"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/relay"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		log.Printf("failed to load .env: %v", err)
	}
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		log.Printf("config error: %v", err)
		log.Printf("set required values in .env or environment")
		os.Exit(1)
	}
	resolver := dns.NewNetResolver()
	providers := dns.NewProviderFactory(cfg)

	server := relay.NewServer(cfg, resolver, providers)

	log.Printf("relay listening on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, server.Router()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
