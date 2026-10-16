package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/poweur/identity"
)

// Server is a running bridge: the HTTP side (hook, blobs, uploads, health,
// metrics), the LMTP listener and the outbound worker.
type Server struct {
	cfg      Config
	log      *slog.Logger
	Store    *Store
	Relay    *Relay
	Dir      *Directory
	Blobs    *Blobs
	Inbound  *Inbound
	Outbound *Outbound
	Metrics  *Metrics

	lmtp    *smtp.Server
	http    *http.Server
	listenH net.Listener
	listenL net.Listener
	started time.Time
}

// OpenStoreFor opens the bridge's database for operator commands (limits).
// The bridge may be running: SQLite's WAL lets both work.
func OpenStoreFor(cfg Config) (*Store, error) {
	if len(cfg.Seed) != 32 {
		return nil, errors.New("EMAIL_BRIDGE_SEED is needed to open the database")
	}
	key, err := identity.DeriveSeedKey(cfg.Seed, "poweur/email/ledger/v1")
	if err != nil {
		return nil, err
	}
	return OpenStore(filepath.Join(cfg.DataDir, "email.db"), key)
}

// Options let tests swap the transport and pick listeners.
type Options struct {
	Logger    *slog.Logger
	Transport Transport
	// HTTPListener and LMTPListener, when set, are used instead of
	// listening on the configured addresses.
	HTTPListener net.Listener
	LMTPListener net.Listener
}

// New builds a bridge from a validated configuration.
func New(cfg Config, opts Options) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	signing, encPub, encPriv, err := cfg.Keys()
	if err != nil {
		return nil, err
	}
	ledgerKey, err := identity.DeriveSeedKey(cfg.Seed, "poweur/email/ledger/v1")
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(filepath.Join(cfg.DataDir, "email.db"), ledgerKey)
	if err != nil {
		return nil, err
	}
	blobs, err := NewBlobs(cfg.DataDir, cfg.PublicURL, cfg.BlobTTL, store)
	if err != nil {
		store.Close()
		return nil, err
	}
	transport := opts.Transport
	if transport == nil {
		if transport, err = NewTransport(cfg); err != nil {
			store.Close()
			return nil, err
		}
	}
	metrics := NewMetrics()
	relay := NewRelay(cfg.RelayURL, cfg.BridgeID, signing, encPub, encPriv, cfg.Resolve)
	dir := NewDirectory(cfg)
	s := &Server{cfg: cfg, log: log, Store: store, Relay: relay, Dir: dir, Blobs: blobs, Metrics: metrics, started: time.Now()}
	var signer *DKIMSigner
	if cfg.DKIMKeyFile != "" {
		if signer, err = LoadDKIMSigner(cfg.DKIMSelector, cfg.DKIMKeyFile, cfg.Domains); err != nil {
			store.Close()
			return nil, err
		}
	}
	s.Outbound = &Outbound{
		cfg: cfg, relay: relay, store: store, dir: dir, blobs: blobs, transport: transport, dkim: signer, encPriv: encPriv,
		log: log, metrics: metrics, now: time.Now, wake: make(chan struct{}, 1),
	}
	s.Inbound = &Inbound{cfg: cfg, dir: dir, relay: relay, store: store, blobs: blobs, log: log, metrics: metrics, onBounce: s.Outbound.Bounced}
	if cfg.Inbound == "smtp" {
		s.lmtp, err = s.Inbound.NewSMTPServer()
	} else {
		s.lmtp, err = s.Inbound.NewLMTPServer()
	}
	if err != nil {
		store.Close()
		return nil, err
	}
	s.listenH, s.listenL = opts.HTTPListener, opts.LMTPListener
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// Handler is the bridge's HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.cfg.Inbound == "lmtp" {
		mux.HandleFunc("POST /stalwart/hook", s.Inbound.HandleHook)
		mux.HandleFunc("POST /hooks/rcpt", s.Inbound.HandleHook)
	}
	mux.HandleFunc("GET /blobs/{id}", s.Blobs.ServeBlob)
	mux.HandleFunc("PUT /v1/uploads/{id}", s.Blobs.ServeUpload(s.Dir))
	mux.HandleFunc("OPTIONS /v1/uploads/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Poweur-Identity, X-Poweur-Timestamp, X-Poweur-Nonce, X-Poweur-Signature")
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("GET /metrics", s.Metrics)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Poweur email bridge %s for %s\n", Version, s.cfg.BridgeID)
	})
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok", "version": Version, "bridge": s.cfg.BridgeID,
		"versionHash": os.Getenv("VERSION_HASH"), "buildTime": os.Getenv("BUILD_TIME"),
		"uptime_seconds": int(time.Since(s.started).Seconds()),
	})
}

// Publish writes the bridge identity's capabilities.json, which is how
// clients find its upload endpoint (`email_api`). Best effort: the bridge
// works without it, uploads do not.
func (s *Server) Publish(ctx context.Context) error {
	send := "on"
	if s.cfg.Transport == "none" {
		send = "off"
	} else if s.cfg.SendPaused() {
		// Clients still let people write (the operator's IDs can send);
		// everyone else is told sending is paused.
		send = "paused"
	}
	caps := map[string]any{
		"version": 1,
		// email_send tells clients whether to offer composing at all
		// (on | paused | off).
		"features": map[string]string{"email_bridge": "v1", "email_send": send},
	}
	if s.cfg.PublicURL != "" {
		caps["endpoints"] = map[string]string{"email_api": s.cfg.PublicURL}
	}
	raw, _ := json.MarshalIndent(caps, "", "  ")
	return s.Relay.PutSystemFile(ctx, ".poweur/public/capabilities.json", raw)
}

// Run serves until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	defer s.Store.Close()
	var err error
	if s.listenH == nil {
		if s.listenH, err = net.Listen("tcp", s.cfg.HTTPAddr); err != nil {
			return err
		}
	}
	if s.listenL == nil {
		addr := s.cfg.LMTPAddr
		if s.cfg.Inbound == "smtp" {
			addr = s.cfg.SMTPListen
		}
		if s.listenL, err = net.Listen("tcp", addr); err != nil {
			s.listenH.Close()
			return err
		}
	}
	s.log.Info("email bridge starting", "version", Version, "bridge", s.cfg.BridgeID,
		"inbound", s.cfg.Inbound, "listen", s.listenL.Addr().String(), "http", s.listenH.Addr().String(),
		"outbound", s.cfg.Transport, "domains", domainsString(s.cfg.Domains))
	if err := s.Publish(ctx); err != nil {
		s.log.Warn("could not publish the bridge's capabilities", "err", err)
	}
	errs := make(chan error, 2)
	go func() { errs <- s.http.Serve(s.listenH) }()
	go func() { errs <- s.lmtp.Serve(s.listenL) }()
	go s.Outbound.Run(ctx, 30*time.Second)
	go s.listen(ctx)
	go s.housekeeping(ctx)

	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("listener failed", "err", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = s.http.Shutdown(shutdown)
	_ = s.lmtp.Close()
	return nil
}

// listen holds the relay event stream open so sends are picked up at once.
func (s *Server) listen(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		opened := time.Now()
		err := s.Relay.Listen(ctx, s.Outbound.Wake)
		if ctx.Err() != nil {
			return
		}
		if time.Since(opened) > time.Minute {
			delay = time.Second
		}
		s.log.Debug("event stream dropped", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

func (s *Server) housekeeping(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := s.Blobs.Sweep(ctx); err != nil {
			s.log.Warn("blob sweep failed", "err", err)
		} else if n > 0 {
			s.log.Info("expired blobs removed", "count", n)
		}
		_ = s.Store.Prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func domainsString(list []DomainMap) string {
	parts := make([]string, 0, len(list))
	for _, d := range list {
		if d.Mail == d.Identity {
			parts = append(parts, d.Mail)
		} else {
			parts = append(parts, d.Mail+"→"+d.Identity)
		}
	}
	return strings.Join(parts, ",")
}
