package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eurything/api/internal/config"
	"github.com/eurything/api/internal/crypto"
	"github.com/eurything/api/internal/dns"
	"github.com/eurything/api/internal/ratelimit"
	"github.com/eurything/api/internal/storage"
)

const (
	maxMessageBytes = 512 * 1024
)

type Server struct {
	cfg        config.Config
	resolver   dns.Resolver
	providers  *dns.ProviderFactory
	identities *storage.IdentityStore
	inbox      *storage.InboxStore
	challenges *storage.ChallengeStore
	rateLimit  *ratelimit.Limiter
	client     *http.Client

	cacheMu   sync.Mutex
	relayCache map[string]cachedRelay
}

type cachedRelay struct {
	Host      string
	ExpiresAt time.Time
}

func NewServer(cfg config.Config, resolver dns.Resolver, providers *dns.ProviderFactory) *Server {
	return &Server{
		cfg:        cfg,
		resolver:   resolver,
		providers:  providers,
		identities: storage.NewIdentityStore(),
		inbox:      storage.NewInboxStore(),
		challenges: storage.NewChallengeStore(),
		rateLimit:  ratelimit.NewLimiter(cfg.RateLimits),
		client:     &http.Client{Timeout: 10 * time.Second},
		relayCache: make(map[string]cachedRelay),
	}
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /messages", s.handleMessagesPost)
	mux.HandleFunc("GET /messages/{identity}", s.handleMessagesGet)
	mux.HandleFunc("GET /auth/challenge", s.handleAuthChallenge)
	mux.HandleFunc("POST /identities", s.handleIdentitiesPost)
	mux.HandleFunc("GET /identities/{identity}", s.handleIdentitiesGet)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "eurything-relay"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: s.cfg.Version})
}

func (s *Server) handleIdentitiesPost(w http.ResponseWriter, r *http.Request) {
	var req IdentityRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.Identity == "" || req.PublicKey == "" || req.DNSProvider == "" || req.DNSToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing required fields")
		return
	}
	if s.cfg.RelayAddress == "" {
		writeError(w, http.StatusInternalServerError, "relay_address_missing", "relay address is not configured")
		return
	}
	if s.identities.Exists(req.Identity) {
		writeError(w, http.StatusConflict, "identity_exists", "identity already registered")
		return
	}

	normalized, publicKeyBytes, err := crypto.NormalizePublicKey(req.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key", err.Error())
		return
	}

	provider, err := s.providers.Provider(req.DNSProvider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsupported_dns_provider", err.Error())
		return
	}

	if err := provider.WriteIdentityRecords(r.Context(), req.DNSToken, req.Identity, normalized, s.cfg.RelayAddress); err != nil {
		writeError(w, http.StatusBadGateway, "dns_write_failed", err.Error())
		return
	}

	identity := storage.Identity{
		Identity:       req.Identity,
		PublicKey:      normalized,
		PublicKeyBytes: publicKeyBytes,
		CreatedAt:      time.Now().UTC(),
	}
	s.identities.Add(identity)

	resp := IdentityResponse{
		Identity:  identity.Identity,
		PublicKey: identity.PublicKey,
		Relay:     s.cfg.RelayAddress,
		CreatedAt: identity.CreatedAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleIdentitiesGet(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return
	}
	entry, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"identity":   entry.Identity,
		"public_key": entry.PublicKey,
	})
}

func (s *Server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	identity := r.URL.Query().Get("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "identity is required")
		return
	}
	challenge, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "challenge_failed", "failed to generate challenge")
		return
	}
	expiresAt := time.Now().UTC().Add(s.cfg.ChallengeTTL)
	s.challenges.Issue(identity, challenge, expiresAt)

	writeJSON(w, http.StatusOK, ChallengeResponse{
		Challenge: challenge,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleMessagesPost(w http.ResponseWriter, r *http.Request) {
	var msg Message
	if err := decodeJSON(w, r, &msg); err != nil {
		return
	}
	if msg.Sender == "" || msg.Recipient == "" || msg.Timestamp == "" || msg.Payload == "" || msg.Signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_message", "missing required message fields")
		return
	}
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", "timestamp must be RFC3339")
		return
	}

	decision := s.rateLimit.Allow(msg.Sender)
	if !decision.Allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":    "rate_limit_exceeded",
			"window":   decision.Window,
			"limit":    decision.Limit,
			"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
		})
		return
	}

	publicKey, err := s.resolvePublicKey(r.Context(), msg.Sender)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	canonical := crypto.CanonicalMessage(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload)
	if err := crypto.VerifySignature(publicKey, canonical, msg.Signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	if s.isLocalRecipient(r.Context(), msg.Recipient) {
		stored := storage.StoredMessage{
			ID:        newMessageID(),
			Sender:    msg.Sender,
			Recipient: msg.Recipient,
			Timestamp: msg.Timestamp,
			Payload:   msg.Payload,
			Signature: msg.Signature,
		}
		s.inbox.Add(msg.Recipient, stored)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if err := s.forwardMessage(r.Context(), msg); err != nil {
		writeError(w, http.StatusBadGateway, "forward_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleMessagesGet(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return
	}
	headerIdentity := r.Header.Get("X-Eurything-Identity")
	if headerIdentity == "" || headerIdentity != identity {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing or mismatch")
		return
	}
	signature := r.Header.Get("X-Eurything-Signature")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature header missing")
		return
	}

	challenge, ok := s.challenges.Consume(identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge missing or expired")
		return
	}

	entry, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}

	if err := crypto.VerifySignature(entry.PublicKeyBytes, challenge.Value, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge signature invalid")
		return
	}

	messages := s.inbox.Drain(identity)
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Server) resolvePublicKey(ctx context.Context, identity string) (ed25519.PublicKey, error) {
	if entry, ok := s.identities.Get(identity); ok {
		return entry.PublicKeyBytes, nil
	}

	txtRecords, err := s.resolver.LookupTXT(ctx, fmt.Sprintf("_eurything.%s", identity))
	if err == nil {
		return crypto.ParseTXTRecord(txtRecords)
	}

	relayHost, err := s.resolveRelayHost(ctx, identity)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s://%s/identities/%s", s.cfg.RelayScheme, relayHost, identity)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("identity lookup failed on peer relay")
	}
	var payload struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return crypto.ParsePublicKey(payload.PublicKey)
}

func (s *Server) isLocalRecipient(ctx context.Context, identity string) bool {
	if s.identities.Exists(identity) {
		return true
	}
	if s.cfg.RelayAddress == "" {
		return false
	}
	host, err := s.resolveRelayHost(ctx, identity)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, s.cfg.RelayAddress)
}

func (s *Server) resolveRelayHost(ctx context.Context, identity string) (string, error) {
	s.cacheMu.Lock()
	entry, ok := s.relayCache[identity]
	if ok && time.Now().Before(entry.ExpiresAt) {
		host := entry.Host
		s.cacheMu.Unlock()
		return host, nil
	}
	s.cacheMu.Unlock()

	hosts, err := s.resolver.LookupHost(ctx, identity)
	if err != nil || len(hosts) == 0 {
		return "", errors.New("recipient relay not resolvable")
	}
	host := hosts[0]

	s.cacheMu.Lock()
	s.relayCache[identity] = cachedRelay{
		Host:      host,
		ExpiresAt: time.Now().Add(s.cfg.DNSTTL),
	}
	s.cacheMu.Unlock()

	return host, nil
}

func (s *Server) forwardMessage(ctx context.Context, msg Message) error {
	relayHost, err := s.resolveRelayHost(ctx, msg.Recipient)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s://%s/messages", s.cfg.RelayScheme, relayHost)
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("forwarding failed with status %d", resp.StatusCode)
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large", "payload exceeds max size")
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "failed to parse JSON body")
		return err
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, ErrorResponse{Error: code, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomToken(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func newMessageID() string {
	token, err := randomToken(12)
	if err != nil {
		return fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	return "msg_" + token
}
