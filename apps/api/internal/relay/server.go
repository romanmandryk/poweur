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

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/ratelimit"
	"github.com/poweur/api/internal/storage"
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
	acks       *storage.AckStore
	challenges *storage.ChallengeStore
	sessions   *storage.SessionStore
	rateLimit  *ratelimit.Limiter
	client     *http.Client

	cacheMu      sync.Mutex
	relayCache   map[string]cachedRelay
	localityCache map[string]cachedLocality
}

type cachedLocality struct {
	Local     bool
	ExpiresAt time.Time
}

type cachedRelay struct {
	Host      string
	ExpiresAt time.Time
}

func NewServer(cfg config.Config, resolver dns.Resolver, providers *dns.ProviderFactory) *Server {
	s := &Server{
		cfg:           cfg,
		resolver:      resolver,
		providers:     providers,
		identities:    storage.NewIdentityStore(),
		inbox:         storage.NewInboxStore(),
		acks:          storage.NewAckStore(),
		challenges:    storage.NewChallengeStore(),
		sessions:      storage.NewSessionStore(),
		rateLimit:     ratelimit.NewLimiter(cfg.RateLimits, cfg.GlobalRateLimits),
		client:        &http.Client{Timeout: 10 * time.Second},
		relayCache:    make(map[string]cachedRelay),
		localityCache: make(map[string]cachedLocality),
	}
	go s.runPruner()
	return s
}

// runPruner periodically evicts expired sessions and locality cache entries.
func (s *Server) runPruner() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.sessions.Prune()
		s.pruneLocalityCache()
	}
}

func (s *Server) pruneLocalityCache() {
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for k, v := range s.localityCache {
		if now.After(v.ExpiresAt) {
			delete(s.localityCache, k)
		}
	}
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /messages", s.handleMessagesPost)
	mux.HandleFunc("GET /messages/{identity}", s.handleMessagesGet)
	mux.HandleFunc("POST /acks", s.handleAcksPost)
	mux.HandleFunc("GET /auth/challenge", s.handleAuthChallenge)
	mux.HandleFunc("POST /identities", s.handleIdentitiesPost)
	mux.HandleFunc("GET /identities/{identity}", s.handleIdentitiesGet)
	mux.HandleFunc("POST /identities/{identity}/encryption-key", s.handleIdentityEncryptionKeyPost)
	mux.HandleFunc("POST /sessions", s.handleSessionCreate)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleSessionDelete)
	mountWebStatic(mux, s.cfg.WebStaticDir)
	return corsMiddleware(mux)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service":       "poweur-relay",
		"relay_address": s.cfg.RelayAddress,
		"web_ui":        "GET /app/ (when WEB_STATIC_DIR is set)",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: s.cfg.Version})
}

// handleIdentitiesPost is owner-only: in addition to a valid DNS token
// (which proves the caller can write the zone), the request body MUST be
// signed by the private half of the `public_key` it is publishing. This
// prevents a hostile DNS-token holder from registering an identity under a
// public key they don't actually control.
func (s *Server) handleIdentitiesPost(w http.ResponseWriter, r *http.Request) {
	var req IdentityRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.Identity == "" || req.PublicKey == "" || req.DNSProvider == "" || req.DNSToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing required fields")
		return
	}
	if req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing identity-signed admin envelope (issued_at, nonce, identity_signature)")
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

	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	normalized, publicKeyBytes, err := crypto.NormalizePublicKey(req.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key", err.Error())
		return
	}

	encryptionPublicKey := ""
	if strings.TrimSpace(req.EncryptionPublicKey) != "" {
		normalizedEnc, _, err := crypto.NormalizeX25519PublicKey(req.EncryptionPublicKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_encryption_key", err.Error())
			return
		}
		encryptionPublicKey = normalizedEnc
	}

	canonical := crypto.CanonicalIdentityRegistration(
		req.Identity, normalized, encryptionPublicKey, s.cfg.RelayAddress, req.IssuedAt, req.Nonce,
	)
	if err := crypto.VerifySignature(publicKeyBytes, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity signature invalid")
		return
	}

	provider, err := s.providers.Provider(req.DNSProvider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsupported_dns_provider", err.Error())
		return
	}

	if err := provider.WriteIdentityRecords(r.Context(), req.DNSToken, req.Identity, normalized, encryptionPublicKey, s.cfg.RelayAddress); err != nil {
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
		Identity:            identity.Identity,
		PublicKey:           identity.PublicKey,
		EncryptionPublicKey: encryptionPublicKey,
		Relay:               s.cfg.RelayAddress,
		CreatedAt:           identity.CreatedAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleIdentityEncryptionKeyPost publishes (or rotates) the X25519
// `_poweur-enc.<identity>` TXT record for an already-registered identity.
// Owner-only: caller must sign the canonical encryption-key-update string
// with the long-lived identity key (verified via DNS or local store).
//
// This endpoint exists because early identities were minted before E2E
// encryption landed, so they have a signing record in DNS but no encryption
// record. A separate endpoint (instead of POST /identities with upsert
// semantics) keeps the primary registration flow strict — "create once" —
// and makes the "retro-fit an existing identity" flow explicit and auditable.
func (s *Server) handleIdentityEncryptionKeyPost(w http.ResponseWriter, r *http.Request) {
	identityValue := r.PathValue("identity")
	if identityValue == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return
	}
	var req EncryptionKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.EncryptionPublicKey == "" || req.DNSProvider == "" || req.DNSToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing required fields")
		return
	}
	if req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing identity-signed admin envelope (issued_at, nonce, identity_signature)")
		return
	}
	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	normalizedEnc, _, err := crypto.NormalizeX25519PublicKey(req.EncryptionPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encryption_key", err.Error())
		return
	}

	identityPub, err := s.resolveIdentityPublicKey(r.Context(), identityValue)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
		return
	}
	canonical := crypto.CanonicalEncryptionKeyUpdate(identityValue, normalizedEnc, req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(identityPub, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity signature invalid")
		return
	}

	provider, err := s.providers.Provider(req.DNSProvider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsupported_dns_provider", err.Error())
		return
	}

	if err := provider.WriteEncryptionKey(r.Context(), req.DNSToken, identityValue, normalizedEnc); err != nil {
		writeError(w, http.StatusBadGateway, "dns_write_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, EncryptionKeyResponse{
		Identity:            identityValue,
		EncryptionPublicKey: normalizedEnc,
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	})
}

// requireRecentTimestamp parses an RFC3339 timestamp and rejects it if it
// is more than 5 minutes off (in either direction) from now. Bounds the
// replay window for identity-signed admin envelopes.
func requireRecentTimestamp(ts string) error {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return fmt.Errorf("issued_at must be RFC3339")
	}
	now := time.Now().UTC()
	if t.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("issued_at is too far in the future")
	}
	if now.Sub(t) > 5*time.Minute {
		return fmt.Errorf("issued_at is too old")
	}
	return nil
}

func (s *Server) handleIdentitiesGet(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return
	}
	if entry, ok := s.identities.Get(identity); ok {
		writeJSON(w, http.StatusOK, map[string]string{
			"identity":   entry.Identity,
			"public_key": entry.PublicKey,
		})
		return
	}
	if !s.isLocalIdentity(r.Context(), identity) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}
	pub, err := s.resolveIdentityPublicKey(r.Context(), identity)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}
	s.warmIdentityCache(identity, pub)
	writeJSON(w, http.StatusOK, map[string]string{
		"identity":   identity,
		"public_key": base64.RawURLEncoding.EncodeToString(pub),
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

// handleMessagesPost is open/messaging-class: anyone may call it. The
// relay accepts the message only when at least one of the parties is a
// locally hosted identity (the "at-least-one-local" rule). Three cases:
//
//  1. Recipient-local — store in the recipient's inbox.
//  2. Sender-local, recipient-remote — privacy-proxy mode; the relay
//     forwards the message to the recipient's home relay over HTTP.
//     This is the *only* sanctioned forward path.
//  3. Both local (note-to-self) — store in inbox.
//
// Anything else (neither party local) is rejected with `403
// not_authorized` so the relay never serves as an open forwarder for the
// world.
func (s *Server) handleMessagesPost(w http.ResponseWriter, r *http.Request) {
	var msg Message
	if err := decodeJSON(w, r, &msg); err != nil {
		return
	}
	if msg.ID == "" || msg.Sender == "" || msg.Recipient == "" || msg.Timestamp == "" || msg.Payload == "" || msg.Signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_message", "missing required message fields (id, sender, recipient, timestamp, payload, signature)")
		return
	}
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", "timestamp must be RFC3339")
		return
	}
	// Encrypt-only policy: every message MUST carry encryption metadata
	// (alg + ephemeral pub + nonce). This is belt-and-suspenders on top of
	// the CLI's client-side refusal and makes it impossible for any past or
	// future client to deliver plaintext through this relay.
	if msg.Encryption == nil || msg.Encryption.Alg == "" ||
		msg.Encryption.EphemeralPublicKey == "" || msg.Encryption.Nonce == "" {
		writeError(w, http.StatusBadRequest, "encryption_required",
			"messages must be end-to-end encrypted (alg, ephemeral_public_key, nonce required)")
		return
	}

	senderLocal := s.isLocalIdentity(r.Context(), msg.Sender)
	recipientLocal := s.isLocalIdentity(r.Context(), msg.Recipient)
	if !senderLocal && !recipientLocal {
		writeError(w, http.StatusForbidden, "not_authorized",
			"relay refuses to forward messages where neither party is locally hosted")
		return
	}

	publicKey, source, err := s.resolveSigningKey(r.Context(), msg.Sender, msg.SessionID, msg.SessionProof)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	encMeta := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, encMeta)
	if err := crypto.VerifySignature(publicKey, canonical, msg.Signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature verification failed (key source: "+source+")")
		return
	}

	// Rate limit by verified sender only — charging an unverified sender field
	// before sig check would let any attacker exhaust another identity's quota.
	decision := s.rateLimit.Allow(msg.Sender)
	if !decision.Allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":    "rate_limit_exceeded",
			"scope":    decision.Scope,
			"window":   decision.Window,
			"limit":    decision.Limit,
			"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
		})
		return
	}

	if recipientLocal {
		stored := storage.StoredMessage{
			ID:        msg.ID,
			Sender:    msg.Sender,
			Recipient: msg.Recipient,
			Timestamp: msg.Timestamp,
			Payload:   msg.Payload,
			Signature: msg.Signature,
			SessionID: msg.SessionID,
			Encryption: &storage.StoredEncryptionMeta{
				Alg:                msg.Encryption.Alg,
				EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
				Nonce:              msg.Encryption.Nonce,
			},
		}
		if !s.inbox.Add(msg.Recipient, stored, s.cfg.MaxInboxPerIdentity) {
			writeError(w, http.StatusServiceUnavailable, "inbox_full",
				"recipient inbox is full; retry after the recipient drains their messages")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"id": msg.ID})
		return
	}

	// Sender-local, recipient-remote: privacy-proxy mode. The home relay
	// forwards on the sender's behalf so the recipient relay sees the
	// home relay's IP rather than the sender client's IP.
	if err := s.forwardMessage(r.Context(), msg); err != nil {
		writeError(w, http.StatusBadGateway, "forward_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": msg.ID})
}

// handleAcksPost is open/messaging-class. It mirrors handleMessagesPost
// for delivery-acknowledgement envelopes: rate-limited, at-least-one-local,
// signature-verified, then either stored locally (recipient-local) or
// forwarded over HTTP (sender-local privacy-proxy mode).
func (s *Server) handleAcksPost(w http.ResponseWriter, r *http.Request) {
	var ack Ack
	if err := decodeJSON(w, r, &ack); err != nil {
		return
	}
	if ack.Type == "" {
		ack.Type = AckTypeDeliveryAck
	}
	if ack.Type != AckTypeDeliveryAck {
		writeError(w, http.StatusBadRequest, "invalid_ack", "type must be \"ack\"")
		return
	}
	if ack.ID == "" || ack.MessageID == "" || ack.State == "" || ack.Sender == "" ||
		ack.Recipient == "" || ack.Timestamp == "" || ack.Signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_ack", "missing required ack fields")
		return
	}
	if ack.State != AckStateDeliveredClient {
		writeError(w, http.StatusBadRequest, "invalid_ack", "unsupported ack state (v1 only emits delivered_client)")
		return
	}
	if _, err := time.Parse(time.RFC3339, ack.Timestamp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ack", "timestamp must be RFC3339")
		return
	}

	senderLocal := s.isLocalIdentity(r.Context(), ack.Sender)
	recipientLocal := s.isLocalIdentity(r.Context(), ack.Recipient)
	if !senderLocal && !recipientLocal {
		writeError(w, http.StatusForbidden, "not_authorized",
			"relay refuses to forward acks where neither party is locally hosted")
		return
	}

	publicKey, source, err := s.resolveSigningKey(r.Context(), ack.Sender, ack.SessionID, ack.SessionProof)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	canonical := crypto.CanonicalAck(ack.ID, ack.MessageID, ack.State, ack.Sender, ack.Recipient, ack.Timestamp, ack.SessionID)
	if err := crypto.VerifySignature(publicKey, canonical, ack.Signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "ack signature verification failed (key source: "+source+")")
		return
	}

	decision := s.rateLimit.Allow(ack.Sender)
	if !decision.Allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":    "rate_limit_exceeded",
			"scope":    decision.Scope,
			"window":   decision.Window,
			"limit":    decision.Limit,
			"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
		})
		return
	}

	if recipientLocal {
		s.acks.Add(ack.Recipient, storage.StoredAck{
			Type:      ack.Type,
			ID:        ack.ID,
			MessageID: ack.MessageID,
			State:     ack.State,
			Sender:    ack.Sender,
			Recipient: ack.Recipient,
			Timestamp: ack.Timestamp,
			Signature: ack.Signature,
			SessionID: ack.SessionID,
		}, s.cfg.MaxAcksPerIdentity)
		writeJSON(w, http.StatusAccepted, map[string]string{"id": ack.ID})
		return
	}

	if err := s.forwardAck(r.Context(), ack); err != nil {
		writeError(w, http.StatusBadGateway, "forward_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": ack.ID})
}

func (s *Server) handleMessagesGet(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return
	}
	headerIdentity := r.Header.Get("X-Poweur-Identity")
	if headerIdentity == "" || headerIdentity != identity {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing or mismatch")
		return
	}
	signature := r.Header.Get("X-Poweur-Signature")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature header missing")
		return
	}
	sessionID := r.Header.Get("X-Poweur-Session-Id")

	challenge, ok := s.challenges.Consume(identity)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge missing or expired")
		return
	}

	var publicKey ed25519.PublicKey
	if sessionID != "" {
		session, ok := s.sessions.Get(sessionID)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "session expired or not found")
			return
		}
		if session.Identity != identity {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session does not belong to identity")
			return
		}
		publicKey = session.PublicKeyBytes
	} else {
		if !s.isLocalIdentity(r.Context(), identity) {
			writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
			return
		}
		pub, err := s.resolveIdentityPublicKey(r.Context(), identity)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
			return
		}
		s.warmIdentityCache(identity, pub)
		publicKey = pub
	}

	if err := crypto.VerifySignature(publicKey, challenge.Value, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge signature invalid")
		return
	}

	messages := s.inbox.Drain(identity)
	if messages == nil {
		messages = []storage.StoredMessage{}
	}
	acks := s.acks.Drain(identity)
	if acks == nil {
		acks = []storage.StoredAck{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "acks": acks})
}

// resolveSigningKey returns the public key the relay should verify a message
// signature against. Priority order:
//
//  1. Local session cache (when sessionID is present and known). This is the
//     fast path for messages posted to the same relay that issued the session.
//  2. Embedded SessionProof (when sessionID is present but unknown, e.g. the
//     message was forwarded from another relay). The relay verifies the proof
//     against the sender's long-lived identity key, caches the session, and
//     uses the proof's session public key to verify the message.
//  3. Long-lived identity key (when sessionID is empty). Used by headless
//     clients that opt out of sessions.
//
// The returned source string is "session", "session-proof", or "identity" for
// diagnostic headers.
func (s *Server) resolveSigningKey(ctx context.Context, sender, sessionID string, proof *SessionProof) (ed25519.PublicKey, string, error) {
	if sessionID != "" {
		if session, ok := s.sessions.Get(sessionID); ok {
			if session.Identity != sender {
				return nil, "session", errors.New("session does not belong to sender")
			}
			return session.PublicKeyBytes, "session", nil
		}
		if proof != nil {
			sess, err := s.acceptSessionProof(ctx, sender, sessionID, proof)
			if err != nil {
				return nil, "session-proof", err
			}
			return sess.PublicKeyBytes, "session-proof", nil
		}
		return nil, "session", errors.New("session expired or not found")
	}
	pub, err := s.resolveIdentityPublicKey(ctx, sender)
	if err != nil {
		return nil, "identity", err
	}
	return pub, "identity", nil
}

// acceptSessionProof verifies a SessionProof against the sender's long-lived
// identity key and, on success, caches the resulting session so subsequent
// messages on the same relay take the fast path.
func (s *Server) acceptSessionProof(ctx context.Context, sender, sessionID string, proof *SessionProof) (storage.Session, error) {
	if proof.SessionPublicKey == "" || proof.IssuedAt == "" || proof.ExpiresAt == "" ||
		proof.Nonce == "" || proof.IdentitySignature == "" {
		return storage.Session{}, errors.New("session proof incomplete")
	}
	issuedAt, err := time.Parse(time.RFC3339, proof.IssuedAt)
	if err != nil {
		return storage.Session{}, errors.New("session proof issued_at invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, proof.ExpiresAt)
	if err != nil {
		return storage.Session{}, errors.New("session proof expires_at invalid")
	}
	now := time.Now().UTC()
	if now.After(expiresAt) {
		return storage.Session{}, errors.New("session proof expired")
	}
	if expiresAt.Sub(issuedAt) > maxSessionTTL {
		return storage.Session{}, errors.New("session proof exceeds max TTL")
	}

	normalizedPub, pubBytes, err := crypto.NormalizePublicKey(proof.SessionPublicKey)
	if err != nil {
		return storage.Session{}, errors.New("session proof public key invalid: " + err.Error())
	}

	identityPub, err := s.resolveIdentityPublicKey(ctx, sender)
	if err != nil {
		return storage.Session{}, errors.New("cannot resolve identity key: " + err.Error())
	}
	canonical := crypto.CanonicalSessionRegistration(sender, normalizedPub, proof.IssuedAt, proof.ExpiresAt, proof.Nonce)
	if err := crypto.VerifySignature(identityPub, canonical, proof.IdentitySignature); err != nil {
		return storage.Session{}, errors.New("session proof signature invalid")
	}

	sess := storage.Session{
		ID:                sessionID,
		Identity:          sender,
		PublicKey:         normalizedPub,
		PublicKeyBytes:    pubBytes,
		IssuedAt:          issuedAt.UTC(),
		ExpiresAt:         expiresAt.UTC(),
		IssuedAtRaw:       proof.IssuedAt,
		ExpiresAtRaw:      proof.ExpiresAt,
		Nonce:             proof.Nonce,
		IdentitySignature: proof.IdentitySignature,
	}
	s.sessions.Put(sess)
	return sess, nil
}

// resolveIdentityPublicKey returns the Ed25519 public key for identity.
// It checks the in-memory store first (populated on registration or DNS warm),
// then falls back to the _poweur.<identity> DNS TXT record. There is no HTTP
// fallback to peer relays — that path was an SSRF vector and DNS TXT is the
// canonical source of truth for all registered identities.
func (s *Server) resolveIdentityPublicKey(ctx context.Context, identity string) (ed25519.PublicKey, error) {
	if entry, ok := s.identities.Get(identity); ok {
		return entry.PublicKeyBytes, nil
	}
	txtRecords, err := s.resolver.LookupTXT(ctx, fmt.Sprintf("_poweur.%s", identity))
	if err != nil {
		return nil, fmt.Errorf("identity public key not found (no TXT record for _poweur.%s)", identity)
	}
	return crypto.ParseTXTRecord(txtRecords)
}

// isLocalIdentity reports whether `identity` is hosted on this relay.
// Fast paths in order: identity store hit, locality cache hit, DNS lookup.
// DNS results (both local and non-local) are cached for cfg.DNSTTL to
// prevent per-request lookups and DNS amplification via spoofed sender fields.
func (s *Server) isLocalIdentity(ctx context.Context, identity string) bool {
	if s.identities.Exists(identity) {
		return true
	}
	if s.cfg.RelayAddress == "" {
		return false
	}

	now := time.Now()
	s.cacheMu.Lock()
	if entry, ok := s.localityCache[identity]; ok && now.Before(entry.ExpiresAt) {
		local := entry.Local
		s.cacheMu.Unlock()
		return local
	}
	s.cacheMu.Unlock()

	local := s.resolveLocality(ctx, identity)

	s.cacheMu.Lock()
	s.localityCache[identity] = cachedLocality{
		Local:     local,
		ExpiresAt: now.Add(s.cfg.DNSTTL),
	}
	s.cacheMu.Unlock()

	return local
}

// resolveLocality performs the DNS-based locality check without any caching.
// Both hostname-match (test/direct) and IP-overlap (CDN-fronted) cases are handled.
func (s *Server) resolveLocality(ctx context.Context, identity string) bool {
	identityHosts, err := s.resolver.LookupHost(ctx, identity)
	if err != nil || len(identityHosts) == 0 {
		return false
	}
	for _, h := range identityHosts {
		if strings.EqualFold(h, s.cfg.RelayAddress) {
			return true
		}
	}
	// CDN-fronted: both identity and relay resolve to the same edge IPs.
	selfHosts, err := s.resolver.LookupHost(ctx, s.cfg.RelayAddress)
	if err != nil || len(selfHosts) == 0 {
		return false
	}
	selfSet := make(map[string]struct{}, len(selfHosts))
	for _, h := range selfHosts {
		selfSet[strings.ToLower(h)] = struct{}{}
	}
	for _, h := range identityHosts {
		if _, ok := selfSet[strings.ToLower(h)]; ok {
			return true
		}
	}
	return false
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

	// If the message is session-signed and we have the session cached, attach
	// a self-contained SessionProof so the peer relay can verify the message
	// without sharing session state with us.
	if msg.SessionID != "" && msg.SessionProof == nil {
		if sess, ok := s.sessions.Get(msg.SessionID); ok && sess.IdentitySignature != "" {
			msg.SessionProof = &SessionProof{
				SessionPublicKey:  sess.PublicKey,
				IssuedAt:          sess.IssuedAtRaw,
				ExpiresAt:         sess.ExpiresAtRaw,
				Nonce:             sess.Nonce,
				IdentitySignature: sess.IdentitySignature,
			}
		}
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

// forwardAck mirrors forwardMessage for delivery acknowledgements: when
// the ack-sender is local but the ack-recipient lives on another relay,
// the home relay POSTs the ack onward. Used only in privacy-proxy mode;
// normal direct sends never trigger this path.
func (s *Server) forwardAck(ctx context.Context, ack Ack) error {
	relayHost, err := s.resolveRelayHost(ctx, ack.Recipient)
	if err != nil {
		return err
	}
	if ack.SessionID != "" && ack.SessionProof == nil {
		if sess, ok := s.sessions.Get(ack.SessionID); ok && sess.IdentitySignature != "" {
			ack.SessionProof = &SessionProof{
				SessionPublicKey:  sess.PublicKey,
				IssuedAt:          sess.IssuedAtRaw,
				ExpiresAt:         sess.ExpiresAtRaw,
				Nonce:             sess.Nonce,
				IdentitySignature: sess.IdentitySignature,
			}
		}
	}
	url := fmt.Sprintf("%s://%s/acks", s.cfg.RelayScheme, relayHost)
	payload, err := json.Marshal(ack)
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

// warmIdentityCache populates the in-memory identity store from a public key
// already resolved from DNS. Subsequent requests hit the store fast path instead
// of re-doing DNS. No-op if the identity was registered (already present).
func (s *Server) warmIdentityCache(identity string, pub ed25519.PublicKey) {
	s.identities.Add(storage.Identity{
		Identity:       identity,
		PublicKey:      base64.RawURLEncoding.EncodeToString(pub),
		PublicKeyBytes: pub,
		CreatedAt:      time.Now().UTC(),
	})
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

