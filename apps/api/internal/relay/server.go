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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/buildinfo"
	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/drive"
	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/ratelimit"
	"github.com/poweur/api/internal/storage"
	"github.com/poweur/api/internal/telemetry"
	idpkg "github.com/poweur/identity"
)

const (
	maxMessageBytes = 512 * 1024
)

type Server struct {
	quotas          *quotaOverrides
	telemetry       *telemetry.Runtime
	startupFailures []string
	stop            chan struct{}
	closeOnce       sync.Once
	pruneOnce       sync.Once
	cfg             config.Config
	resolver        dns.Resolver
	providers       *dns.ProviderFactory
	identities      *storage.IdentityStore
	inbox           *storage.InboxStore
	requests        *storage.RequestStore
	anonInbox       *storage.InboxStore
	anon            *anonState
	powSecret       []byte
	acks            *storage.AckStore
	challenges      *storage.ChallengeStore
	keystore        *storage.KeystoreStore
	rendezvous      *storage.RendezvousStore
	sessions        *storage.SessionStore
	rateLimit       *ratelimit.Limiter
	regGate         *RegistrationGate
	client          *http.Client
	idCache         *idpkg.Cache
	// requestRelayLimit meters contact-request admissions per sending relay
	// (E07-T5). Nil when the operator caps no window.
	requestRelayLimit *ratelimit.PeerLimiter
	// abuse counts verified sys.abuse.report submissions (E07-T5).
	abuse *abuseLog

	// sysFiles is the relay's access to identities' system documents
	// (EPIC-020 E20-T6): files under POWEUR_DATA, or memory without it.
	sysFiles SystemFiles
	// drive is the object store selected by STORAGE_PROVIDER. Nil when the
	// relay has no POWEUR_DATA and is not using S3.
	drive    provider.Store
	driveErr error
	// engine serves /drive/{identity} over drive (E20-T5); nil without it.
	engine *engine.Engine
	// driveStreams fans drive changes out to owner and member streams.
	driveStreams *driveStreams
	// linkUses and linkFailures rate-limit link holders (E20-T7).
	linkUses, linkFailures *linkLimiter
	// rosters caches group identities' rosters for group shares (E20-T7).
	rosters         *rosterCache
	groupShareNoted map[string]bool
	// sysLocks makes owner API preconditions atomic with writes and deletes.
	sysLocks *deviceLocks

	// hub fans delivery notifications out to open push streams (E09-T2).
	hub *hub
	// deviceLocks serializes read-modify-write on each identity's
	// device registry (.poweur/state/devices.json).
	deviceLocks *deviceLocks

	cacheMu       sync.Mutex
	relayCache    map[string]cachedRelay
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
	var startupFailures []string
	// Everything durable lives in the drive provider (EPIC-020 E20-T6):
	// drives under drives/, the relay's own registries under relay/. Without
	// one (no POWEUR_DATA, fs provider) the relay keeps memory only.
	driveStore, driveErr := drive.Open(cfg)
	if driveErr != nil {
		startupFailures = append(startupFailures, "drive_storage_open_failed")
	}
	var objects storage.Objects
	if driveStore != nil {
		objects = driveStore
	}
	store, err := storage.OpenIdentityStore(objects)
	if err != nil {
		// Fall back to memory-only rather than crashing constructors used in tests.
		store = storage.NewIdentityStore()
		startupFailures = append(startupFailures, "identity_storage_open_failed")
	}
	keystore, err := storage.OpenKeystoreStore(objects)
	if err != nil {
		keystore = storage.NewKeystoreStore()
		startupFailures = append(startupFailures, "keystore_open_failed")
	}
	// Undelivered mail survives a restart (EPIC-009 E09-T1); a relay with no
	// durable store keeps the old memory-only behaviour rather than refusing to run.
	inbox, err := storage.OpenInboxStore(objects)
	if err != nil {
		inbox = storage.NewInboxStore()
		startupFailures = append(startupFailures, "inbox_storage_open_failed")
	}
	acks, err := storage.OpenAckStore(objects)
	if err != nil {
		acks = storage.NewAckStore()
		startupFailures = append(startupFailures, "ack_storage_open_failed")
	}
	s := &Server{
		startupFailures:   startupFailures,
		stop:              make(chan struct{}),
		cfg:               cfg,
		resolver:          resolver,
		providers:         providers,
		identities:        store,
		inbox:             inbox,
		requests:          storage.NewRequestStore(),
		anonInbox:         storage.NewInboxStore(),
		anon:              newAnonState(),
		powSecret:         newPowSecret(),
		acks:              acks,
		challenges:        storage.NewChallengeStore(),
		keystore:          keystore,
		rendezvous:        storage.NewRendezvousStore(),
		sessions:          storage.NewSessionStore(), // sessions remain memory-only by design
		rateLimit:         ratelimit.NewLimiter(cfg.RateLimits, cfg.GlobalRateLimits),
		requestRelayLimit: ratelimit.NewPeerLimiter(cfg.RequestRelayLimits),
		abuse:             newAbuseLog(),
		regGate:           NewRegistrationGate(cfg.RegistrationGate, cfg.RegistrationInviteCodes),
		client:            &http.Client{Timeout: 10 * time.Second},
		idCache:           idpkg.NewCache(),
		hub:               newHub(),
		driveStreams:      newDriveStreams(),
		linkUses:          newLinkLimiter(),
		rosters:           newRosterCache(),
		linkFailures:      newLinkLimiter(),
		deviceLocks:       newDeviceLocks(),
		sysLocks:          newDeviceLocks(),
		relayCache:        make(map[string]cachedRelay),
		localityCache:     make(map[string]cachedLocality),
	}
	s.quotas = newQuotaOverrides(cfg.StorageQuotasFile, func(err error) {
		s.event(context.Background(), "storage.quotas", "invalid")
		logQuotaFileError(err)
	})
	s.driveErr = driveErr
	if driveStore != nil {
		s.quotas.store = driveStore
	}
	s.sysFiles = newMemSystemFiles()
	if driveStore != nil {
		s.drive = driveStore
		s.engine = newDriveEngine(driveStore, s)
		s.sysFiles = driveSystemFiles{engine: s.engine}
		go s.warmRosters()
	}
	return s
}

// Drive is the object store selected by STORAGE_PROVIDER. It is nil when the
// relay has no durable drive.
func (s *Server) Drive() provider.Store { return s.drive }

// DriveError is set when a durable drive was required and could not be opened.
func (s *Server) DriveError() error { return s.driveErr }

// runPruner periodically evicts expired sessions and locality cache entries.
func (s *Server) runPruner() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		s.sampleTelemetry()
		s.sessions.Prune()
		s.anon.prune()
		s.abuse.prune(time.Now().UTC())
		s.pruneLocalityCache()
		s.expireSpool()
		s.event(context.Background(), "maintenance.prune", "success")
	}
}

// expireSpool retires mail nobody ever came for, and tells each sender.
//
// The notice is a `sys.delivery.failed` ack the **relay** generates, so it
// carries no signature — nobody else can honestly say "I gave up holding
// this". Clients must read it as what it is: the relay's own admission, not
// proof about the recipient. It is queued to the sender like any ack, so it
// surfaces on their next pickup.
func (s *Server) expireSpool() {
	if s.cfg.SpoolTTL <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.cfg.SpoolTTL)
	for _, expired := range s.inbox.Expire(cutoff) {
		s.event(context.Background(), "message.expire", "success")
		if expired.Item.Sender == "" {
			continue // anonymous senders have no inbox to notify
		}
		s.acks.Add(expired.Item.Sender, storage.StoredAck{
			Type:      "sys.delivery.failed",
			ID:        "exp_" + expired.Item.ID,
			MessageID: expired.Item.ID,
			State:     "expired",
			Sender:    s.cfg.RelayAddress,
			Recipient: expired.Item.Sender,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, s.cfg.MaxInboxPerIdentity)
	}
	// An undelivered *receipt* is only worth so much; expiring it silently is
	// right, because notifying about a notification has no bottom.
	for range s.acks.Expire(cutoff) {
		s.event(context.Background(), "ack.expire", "success")
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
	s.pruneOnce.Do(func() { go s.runPruner() })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /messages", s.handleMessagesPost)
	mux.HandleFunc("GET /messages/{identity}", s.handleMessagesGet)
	mux.HandleFunc("POST /messages/{identity}/consume", s.handleMessagesConsume)
	mux.HandleFunc("GET /groups/{group}", s.handleGroupGet)
	mux.HandleFunc("GET /groups/{group}/epoch", s.handleGroupEpoch)
	mux.HandleFunc("POST /groups/{group}/messages", s.handleGroupMessagesPost)
	mux.HandleFunc("GET /events/{identity}", s.handleEvents)
	mux.HandleFunc("GET /requests/{identity}", s.handleRequestsGet)
	mux.HandleFunc("GET /anon/{identity}", s.handleAnonGet)
	mux.HandleFunc("POST /abuse", s.handleAbuseReport)
	mux.HandleFunc("POST /acks", s.handleAcksPost)
	mux.HandleFunc("GET /auth/challenge", s.handleAuthChallenge)
	mux.HandleFunc("GET /auth/pow", s.handleAuthPow)
	mux.HandleFunc("POST /identities", s.handleIdentitiesPost)
	mux.HandleFunc("GET /identities/{identity}", s.handleIdentitiesGet)
	mux.HandleFunc("GET /hosted/availability", s.handleHostedAvailability)
	mux.HandleFunc("POST /identities/{identity}/encryption-key", s.handleIdentityEncryptionKeyPost)
	mux.HandleFunc("POST /identities/{identity}/export", s.handleIdentityExport)
	mux.HandleFunc("POST /identities/{identity}/rotate", s.handleIdentityRotate)
	mux.HandleFunc("PUT /identities/{identity}/keystore", s.handleKeystorePut)
	mux.HandleFunc("POST /identities/{identity}/keystore/list", s.handleKeystoreList)
	mux.HandleFunc("POST /identities/{identity}/keystore/fetch", s.handleKeystoreFetch)
	mux.HandleFunc("DELETE /identities/{identity}/keystore/{enrollment}", s.handleKeystoreDelete)
	mux.HandleFunc("POST /identities/{identity}/enroll/offer", s.handleEnrollOffer)
	mux.HandleFunc("POST /identities/{identity}/enroll/{rendezvous}/fetch", s.handleEnrollFetch)
	mux.HandleFunc("POST /identities/{identity}/enroll/{rendezvous}/deliver", s.handleEnrollDeliver)
	mux.HandleFunc("POST /identities/{identity}/enroll/{rendezvous}/reveal", s.handleEnrollReveal)
	mux.HandleFunc("GET /identities/{identity}/enroll/{rendezvous}", s.handleEnrollPoll)
	mux.HandleFunc("DELETE /identities/{identity}/enroll/{rendezvous}", s.handleEnrollCancel)
	mux.HandleFunc("POST /sessions", s.handleSessionCreate)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleSessionDelete)
	mux.HandleFunc("GET /identities/{identity}/system/{path...}", s.handleSystemFileGet)
	mux.HandleFunc("PUT /identities/{identity}/system/{path...}", s.handleSystemFilePut)
	mux.HandleFunc("DELETE /identities/{identity}/system/{path...}", s.handleSystemFileDelete)
	mux.HandleFunc("GET /drive/{identity}", s.handleDriveGet)
	mux.HandleFunc("POST /drive/{identity}/chunks/missing", s.handleDriveMissing)
	mux.HandleFunc("PUT /drive/{identity}/chunks/{chunk}", s.handleDriveChunkPut)
	mux.HandleFunc("POST /drive/{identity}/commit", s.handleDriveCommit)
	mux.HandleFunc("GET /drive/{identity}/changes", s.handleDriveChanges)
	mux.HandleFunc("GET /drive/{identity}/shares", s.handleDriveShares)
	mux.HandleFunc("GET /drive/{identity}/events", s.handleDriveEvents)
	mux.HandleFunc("GET /drive/{identity}/links/{link}", s.handleDriveLink)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}", s.handleDriveNode)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/children", s.handleDriveChildren)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/history", s.handleDriveHistory)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/shares", s.handleDriveNodeShares)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/records", s.handleDriveRecords)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/chunks/{chunk}", s.handleDriveChunk)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/versions/{version}", s.handleDriveVersion)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/versions/{version}/pages/{page}", s.handleDrivePage)
	mux.HandleFunc("GET /drive/{identity}/nodes/{node}/versions/{version}/chunks/{chunk}", s.handleDriveChunk)
	mux.HandleFunc("GET /devices/{identity}", s.handleDevicesGet)
	mux.HandleFunc("POST /devices/{identity}/revoke", s.handleDevicesRevoke)
	mux.HandleFunc("GET /.well-known/did.json", s.handleDIDWeb)
	mux.HandleFunc("GET /.well-known/poweur/{path...}", s.handleWellKnown)
	mux.HandleFunc("GET /s/{link}", s.handleLinkViewer)
	mux.HandleFunc("GET /s/assets/{file}", s.handleLinkViewerAsset)
	mountWebStatic(mux, "/app", s.cfg.WebStaticDir, s.cfg.Telemetry.BrowserConfig(s.cfg.Version))
	mountRootIcons(mux, s.cfg.WebStaticDir)
	return s.instrument(mux, corsMiddleware(mux))
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Someone with no identity yet who lands on the launcher host wants the
	// app, not a service banner — but this path is also the relay's root
	// document, which every client reads to learn the relay's address and which
	// hosts are launchers. Redirecting that turned the document into an HTML
	// page and left the app unable to tell which front door it was standing in.
	// So the redirect is for readers who did not ask for the document.
	if s.cfg.WebStaticDir != "" && r.URL.Path == "/" && !wantsJSON(r) {
		host := r.Host
		if h, _, err := splitHostPort(host); err == nil {
			host = h
		}
		if s.cfg.IsLauncherHost(host) {
			http.Redirect(w, r, "/app/", http.StatusFound)
			return
		}
	}
	s.advertiseIndieAuth(w, r)
	version, buildTime, versionHash := s.releaseInfo()
	writeJSON(w, http.StatusOK, map[string]any{
		"service":       "poweur-relay",
		"relay_address": s.cfg.RelayAddress,
		// The host that serves the claim flow, so a client can tell whether it
		// is running as the launcher and where to hand a new identity off from.
		"launcher_host": s.cfg.LauncherHost,
		// Every host that serves the claim flow, so a client can tell which
		// front door it is (EPIC-015 E15-T7). launcher_host stays the
		// canonical one a hand-off targets.
		"launcher_hosts": s.cfg.LauncherHosts,
		"hosted_domains": s.cfg.HostedDomains,
		"web_ui":         "GET /app/ (when WEB_STATIC_DIR is set)",
		"version":        version,
		"buildTime":      buildTime,
		"versionHash":    versionHash,
	})
}

// advertiseIndieAuth points IndieAuth clients at the operator's bridge when
// the root of a hosted identity is fetched: `https://alice.poweur.net/` is
// Alice's IndieAuth profile URL, and a Link header is how a client discovers
// who authenticates her (EPIC-022 E22-T6). The relay shares nothing with the
// bridge; an identity can point elsewhere from its own site.
func (s *Server) advertiseIndieAuth(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OAuthBridgeURL == "" || r.URL.Path != "/" {
		return
	}
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !s.identities.Exists(host) {
		return
	}
	w.Header().Add("Link", "<"+s.cfg.OAuthBridgeURL+"/.well-known/oauth-authorization-server>; rel=\"indieauth-metadata\"")
}

// releaseInfo is the semver + build stamp advertised on GET / and /health.
func (s *Server) releaseInfo() (version, buildTime, versionHash string) {
	version = s.cfg.Version
	if version == "" {
		version = buildinfo.Version
	}
	rawTime := s.cfg.BuildTime
	if rawTime == "" {
		rawTime = buildinfo.Time
	}
	buildTime = buildinfo.FormatTime(rawTime)
	versionHash = s.cfg.VersionHash
	if versionHash == "" {
		versionHash = buildinfo.Hash
	}
	return
}

// wantsJSON reports whether the caller asked for the root document itself.
// Every `@poweur/client` JSON request says so; a browser navigating says
// `text/html`, and curl says `*/*`.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	version, buildTime, versionHash := s.releaseInfo()
	resp := HealthResponse{
		Status:      "ok",
		Version:     version,
		BuildTime:   buildTime,
		VersionHash: versionHash,
		Storage:     s.storageHealth(),
	}
	if resp.Storage != nil && resp.Storage.Configured && !resp.Storage.Writable {
		resp.Status = "degraded"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleIdentitiesPost registers an identity. Two modes:
//
//   - Hosted: no dns_provider/dns_token; identity must be under HOSTED_DOMAINS;
//     client supplies a signed identity_document (or fields + admin envelope).
//   - DNS: dns_provider + dns_token present; relay writes zone records as before.
//
// In both modes the relay persists a signed identity document when possible.
func (s *Server) handleIdentitiesPost(w http.ResponseWriter, r *http.Request) {
	var req IdentityRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.Identity == "" || req.PublicKey == "" {
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

	hosted := req.DNSProvider == "" && req.DNSToken == ""
	if !hosted && (req.DNSProvider == "" || req.DNSToken == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "dns_provider and dns_token are both required for DNS registration")
		return
	}

	// An operator registration (OPERATOR_TOKEN) may take a hosted name the
	// policy holds back: reserved, short or blocked. Everything else — the
	// hosted domain, the signed envelope and document, name shape — still
	// applies. A wrong token is refused outright rather than treated as a
	// normal request, so a typo can't quietly produce an ordinary one.
	operator, ok := s.operatorRegistration(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_operator_token", "operator token is not valid on this relay")
		return
	}
	if operator && !hosted {
		writeError(w, http.StatusBadRequest, "invalid_request", "operator registration is for hosted names only")
		return
	}

	if operator {
		// The operator is not a stranger: no proof-of-work or invite.
	} else if hosted && s.regGate.Mode() == RegistrationGatePow {
		// PoW gate (EPIC-014 E14-T5): verified here rather than inside the
		// gate because the handler holds the challenge secret.
		if !s.checkRegistrationPow(w, req) {
			return
		}
	} else if hosted {
		if err := s.regGate.AllowHosted(req.InviteCode); err != nil {
			if ge, ok := err.(gateError); ok {
				status := http.StatusForbidden
				if ge.code == "invite_required" {
					status = http.StatusUnauthorized
				}
				writeError(w, status, ge.code, ge.detail)
				return
			}
			writeError(w, http.StatusForbidden, "registration_denied", err.Error())
			return
		}
	}

	// Registration rate limit (per identity name + flood bucket).
	if decision := s.rateLimit.Allow("register:" + strings.ToLower(req.Identity)); !decision.Allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "registration rate limit exceeded")
		return
	}
	if decision := s.rateLimit.Allow("register:flood"); !decision.Allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "registration rate limit exceeded")
		return
	}

	// Hosted handles answer to the operator's policy (EPIC-018 E18-T1); the
	// reason code travels with the error so a client can react to *why*
	// without parsing the message.
	if !operator {
		if err := idpkg.ValidateHostedHandleWithPolicy(req.Identity, s.cfg.NamePolicy); err != nil && hosted {
			writeError(w, http.StatusBadRequest, "invalid_identity",
				fmt.Sprintf("%s (%s)", err.Error(), idpkg.ReasonOf(err)))
			return
		}
	}
	// Reserved labels hold names back from self-service claims only; once an
	// operator has created one, every client can reach it.
	validateName := idpkg.ValidateClaimableName
	if operator {
		validateName = idpkg.ValidateIdentityName
	}
	if err := validateName(req.Identity); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity", err.Error())
		return
	}

	if hosted && !s.cfg.IsHostedDomain(req.Identity) {
		writeError(w, http.StatusBadRequest, "not_hosted_domain", "identity is not under a configured hosted domain")
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
	verifiedActor(r, req.Identity)
	if operator {
		// Audit trail: operator-created names bypass the policy.
		s.event(r.Context(), "identity.operator_register", "accepted")
	}

	var docJSON []byte
	if len(req.IdentityDocument) > 0 {
		doc, err := idpkg.ParseDocument(req.IdentityDocument, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_identity_document", err.Error())
			return
		}
		if !strings.EqualFold(doc.Identity, req.Identity) {
			writeError(w, http.StatusBadRequest, "invalid_identity_document", "document identity mismatch")
			return
		}
		if idpkg.NormalizePublicKeyKey(doc.PublicKey) != normalized {
			writeError(w, http.StatusBadRequest, "invalid_identity_document", "document public key mismatch")
			return
		}
		docJSON = req.IdentityDocument
	} else {
		// Synthesize a document for DNS registrations (and hosted clients that
		// only send the admin envelope). Signed by the same key via the fact
		// we already verified the registration envelope — but the document
		// itself must be signed. Clients should send identity_document; for
		// backward compat DNS path we store fields without a document when
		// durable store is off, or reject if durable store requires one.
		encFmt := ""
		if encryptionPublicKey != "" {
			encFmt = idpkg.FormatX25519PublicKey(mustDecodeB64(encryptionPublicKey))
		}
		doc := idpkg.NewDocument(req.Identity, idpkg.FormatEd25519PublicKey(publicKeyBytes), encFmt, s.cfg.RelayAddress, nil)
		doc.UpdatedAt = req.IssuedAt
		// Cannot sign without private key on relay — leave unsigned only in memory.
		// For durable/hosted, require client-supplied signed document.
		if hosted || s.identities.Durable() {
			writeError(w, http.StatusBadRequest, "identity_document_required", "signed identity_document is required")
			return
		}
		_ = doc
	}

	if !hosted {
		provider, err := s.providers.Provider(req.DNSProvider)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unsupported_dns_provider", err.Error())
			return
		}
		if err := provider.WriteIdentityRecords(r.Context(), req.DNSToken, req.Identity, normalized, encryptionPublicKey, s.cfg.RelayAddress); err != nil {
			writeError(w, http.StatusBadGateway, "dns_write_failed", err.Error())
			return
		}
	}

	identity := storage.Identity{
		Identity:            strings.ToLower(req.Identity),
		PublicKey:           normalized,
		PublicKeyBytes:      publicKeyBytes,
		EncryptionPublicKey: encryptionPublicKey,
		Relay:               s.cfg.RelayAddress,
		DocumentJSON:        docJSON,
		CreatedAt:           time.Now().UTC(),
	}
	if s.identities.Durable() {
		if err := s.identities.Put(identity); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
	} else if !s.identities.Add(identity) {
		writeError(w, http.StatusConflict, "identity_exists", "identity already registered")
		return
	}
	s.mirrorIdentityDocument(r.Context(), identity)

	resp := IdentityResponse{
		Identity:            identity.Identity,
		PublicKey:           identity.PublicKey,
		EncryptionPublicKey: encryptionPublicKey,
		Relay:               s.cfg.RelayAddress,
		CreatedAt:           identity.CreatedAt.Format(time.RFC3339),
		IdentityDocument:    docJSON,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func mustDecodeB64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, _ = base64.StdEncoding.DecodeString(s)
	}
	return b
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
	verifiedActor(r, identityValue)

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
		resp := IdentityResponse{
			Identity:            entry.Identity,
			PublicKey:           entry.PublicKey,
			EncryptionPublicKey: entry.EncryptionPublicKey,
			Relay:               entry.Relay,
			CreatedAt:           entry.CreatedAt.Format(time.RFC3339),
			IdentityDocument:    entry.DocumentJSON,
		}
		if resp.Relay == "" {
			resp.Relay = s.cfg.RelayAddress
		}
		writeJSON(w, http.StatusOK, resp)
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
	writeJSON(w, http.StatusOK, IdentityResponse{
		Identity:  identity,
		PublicKey: base64.RawURLEncoding.EncodeToString(pub),
		Relay:     s.cfg.RelayAddress,
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
	// Unsigned + senderless = anonymous ingress (EPIC-014): opt-in per
	// recipient, challenge-gated, separate queue. A message with a sender
	// but no signature (or vice versa) stays invalid below.
	if msg.Sender == "" && msg.Signature == "" {
		s.handleAnonMessage(w, r, msg)
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
	// Typed envelope extensions (E09-T3): shape checks and the reserved
	// `sys.*` namespace. Cheap, plaintext, and ahead of signature
	// verification — an envelope whose metadata carries a control character
	// has no unambiguous canonical string to verify against.
	if !validateEnvelopeExtensions(w, msg) {
		return
	}
	// Group provenance is not something a direct send may claim about a
	// group this relay hosts (E09-T5); that path is POST /groups/{group}/messages,
	// where membership and epoch are actually checked.
	if !s.refuseForgedLocalGroup(w, r, msg) {
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
	canonical := crypto.CanonicalMessageEnvelope(
		msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload,
		msg.ID, msg.SessionID, msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata, encMeta)
	if err := crypto.VerifySignature(publicKey, canonical, msg.Signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature verification failed (key source: "+source+")")
		return
	}
	if !refuseExpiredEnvelope(w, msg) {
		return
	}
	verifiedActor(r, msg.Sender)
	requestDetail(r, messageKind(msg.Type))
	if senderLocal {
		requestAction(r, "message.submit")
	} else {
		requestAction(r, "message.receive")
		if st, ok := r.Context().Value(telemetryKey{}).(*requestTelemetry); ok {
			st.direct = false
		}
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
		// Inbox policy (EPIC-007): the recipient's relay is the enforcement
		// point for local and cross-relay-forwarded senders alike.
		verdict, detail := s.evaluateInboxPolicy(r.Context(), msg)
		switch verdict {
		case policyReject:
			writeError(w, http.StatusForbidden, "policy_rejected", detail)
			return
		case policyQueueRequest:
			// The requests queue is the one door open to strangers, so it is
			// the one door a flood of cheap identities comes through. Meter
			// the relay that carried them (E07-T5), not just the identity.
			if decision, ok := s.meterRequestRelay(r.Context(), msg.Sender); !ok {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":    "rate_limit_exceeded",
					"scope":    decision.Scope,
					"window":   decision.Window,
					"limit":    decision.Limit,
					"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
				})
				return
			}
			outcome := s.requests.Add(msg.Recipient, msg.Sender, storedFromMessage(msg), requestCooldown)
			if outcome != storage.RequestQueued {
				writeError(w, http.StatusConflict, outcome,
					"a request with this consent scope is already pending or in cooldown")
				return
			}
			// A queued request is a delivery too: without this, a contact
			// request waits silently until the recipient happens to open the
			// app, which is exactly the wait push exists to remove.
			s.event(r.Context(), messageKind(msg.Type)+".queue", "success")
			s.notify(msg.Recipient, "request", msg.ID)
			writeJSON(w, http.StatusAccepted, map[string]string{"id": msg.ID, "status": "request_queued"})
			return
		}
		if !s.inbox.Add(msg.Recipient, storedFromMessage(msg), s.cfg.MaxInboxPerIdentity) {
			writeError(w, http.StatusServiceUnavailable, "inbox_full",
				"recipient inbox is full; retry after the recipient drains their messages")
			return
		}
		// Tell anyone listening that there is something to pick up (E09-T2).
		// The notification carries no payload: the cursor read it triggers is
		// where delivery actually happens.
		s.event(r.Context(), "message.enqueue", "success")
		s.notify(msg.Recipient, "message", msg.ID)
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
	if ack.State != AckStateDeliveredClient && ack.State != AckStateRead {
		writeError(w, http.StatusBadRequest, "invalid_ack", "unsupported ack state (want delivered_client or read)")
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
	verifiedActor(r, ack.Sender)

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
		s.notify(ack.Recipient, "ack", ack.MessageID)
		writeJSON(w, http.StatusAccepted, map[string]string{"id": ack.ID})
		return
	}

	if err := s.forwardAck(r.Context(), ack); err != nil {
		writeError(w, http.StatusBadGateway, "forward_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": ack.ID})
}

// consumeChallenge spends the nonce this request names, falling back to the
// most recent one for callers that do not echo it. Spending by value is what
// lets a client hold a push stream open and poll at the same time without the
// two invalidating each other.
func (s *Server) consumeChallenge(identity string, r *http.Request) (storage.Challenge, bool) {
	if value := r.Header.Get("X-Poweur-Challenge"); value != "" {
		return s.challenges.ConsumeValue(identity, value)
	}
	return s.challenges.Consume(identity)
}

// authorizeInboxRead proves the caller owns the inbox: a one-shot challenge
// signed by the identity key or a live session key. Shared by the pickup and
// the consume that follows it — forgetting someone's mail must need at least
// the proof that reading it does.
func (s *Server) authorizeInboxRead(w http.ResponseWriter, r *http.Request, identity string) bool {
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return false
	}
	headerIdentity := r.Header.Get("X-Poweur-Identity")
	if headerIdentity == "" || headerIdentity != identity {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing or mismatch")
		return false
	}
	_, ok := s.authenticateCaller(w, r, true)
	return ok
}

// authenticateCaller proves who is calling: the identity in
// X-Poweur-Identity signed a one-shot challenge with its identity key or a
// live session key. With requireLocal, only identities hosted here may sign
// with their identity key; otherwise a visitor from another relay may, and
// its key is resolved like any peer's.
func (s *Server) authenticateCaller(w http.ResponseWriter, r *http.Request, requireLocal bool) (string, bool) {
	identity := r.Header.Get("X-Poweur-Identity")
	if identity == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing")
		return "", false
	}
	signature := r.Header.Get("X-Poweur-Signature")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature header missing")
		return "", false
	}
	sessionID := r.Header.Get("X-Poweur-Session-Id")

	challenge, ok := s.consumeChallenge(identity, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge missing or expired")
		return "", false
	}

	var publicKey ed25519.PublicKey
	if sessionID != "" {
		session, ok := s.sessions.Get(sessionID)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "session expired or not found")
			return "", false
		}
		if session.Identity != identity {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session does not belong to identity")
			return "", false
		}
		publicKey = session.PublicKeyBytes
	} else {
		if requireLocal && !s.isLocalIdentity(r.Context(), identity) {
			writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
			return "", false
		}
		pub, err := s.resolveIdentityPublicKey(r.Context(), identity)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
			return "", false
		}
		s.warmIdentityCache(identity, pub)
		publicKey = pub
	}

	if err := crypto.VerifySignature(publicKey, challenge.Value, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge signature invalid")
		return "", false
	}
	verifiedActor(r, identity)
	return identity, true
}

func (s *Server) handleMessagesGet(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if !s.authorizeInboxRead(w, r, identity) {
		return
	}

	// Two pickup modes (EPIC-009 E09-T1). `?since=` reads without forgetting
	// and hands back a cursor the client acknowledges once it has the messages
	// safely; without the parameter this is the original drain-on-read, kept
	// for clients that have not moved yet. Deleting at read time loses a
	// message to a dropped connection exactly as a restart used to.
	if r.URL.Query().Has("since") {
		cursor := r.URL.Query().Get("since")
		messages, nextCursor := s.inbox.Since(identity, cursor)
		acks, nextAckCursor := s.acks.Since(identity, cursor)
		writeJSON(w, http.StatusOK, map[string]any{
			"messages":   messages,
			"acks":       acks,
			"cursor":     nextCursor,
			"ack_cursor": nextAckCursor,
			"pending":    s.inbox.Pending(identity),
		})
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

// handleMessagesConsume forgets everything through the cursor a client says it
// has (EPIC-009 E09-T1). Authenticated exactly like the pickup it follows —
// forgetting someone's mail needs at least the proof that reading it does.
func (s *Server) handleMessagesConsume(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if !s.authorizeInboxRead(w, r, identity) {
		return
	}
	var body struct {
		Through    string `json:"through"`
		AckThrough string `json:"ack_through"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed body")
		return
	}
	removed := s.inbox.Consume(identity, body.Through)
	ackRemoved := 0
	if body.AckThrough != "" {
		ackRemoved = s.acks.Consume(identity, body.AckThrough)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"consumed":      removed,
		"acks_consumed": ackRemoved,
		"pending":       s.inbox.Pending(identity),
	})
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
	identityPub, err := s.resolveIdentityPublicKey(ctx, sender)
	if err != nil {
		return storage.Session{}, errors.New("cannot resolve identity key: " + err.Error())
	}
	// One implementation of the proof chain, shared with the sign-in
	// verifier (EPIC-008 E08-T1): completeness, RFC3339 timestamps, expiry,
	// the 24h TTL cap, key encoding and the identity signature over
	// CanonicalSessionRegistration.
	verified, err := idpkg.VerifySessionProof(identityPub, sender, idpkg.SignInSessionProof{
		SessionPublicKey:  proof.SessionPublicKey,
		IssuedAt:          proof.IssuedAt,
		ExpiresAt:         proof.ExpiresAt,
		Nonce:             proof.Nonce,
		IdentitySignature: proof.IdentitySignature,
	}, time.Now().UTC())
	if err != nil {
		return storage.Session{}, err
	}

	sess := storage.Session{
		ID:                sessionID,
		Identity:          sender,
		PublicKey:         verified.PublicKey,
		PublicKeyBytes:    verified.PublicKeyBytes,
		IssuedAt:          verified.IssuedAt,
		ExpiresAt:         verified.ExpiresAt,
		IssuedAtRaw:       proof.IssuedAt,
		ExpiresAtRaw:      proof.ExpiresAt,
		Nonce:             proof.Nonce,
		IdentitySignature: proof.IdentitySignature,
	}
	s.sessions.Put(sess)
	return sess, nil
}

// resolveIdentityPublicKey returns the Ed25519 public key for identity.
// Order: local store → web-first identity.Resolve (HTTPS then DNS TXT).
func (s *Server) resolveIdentityPublicKey(ctx context.Context, identity string) (ed25519.PublicKey, error) {
	if entry, ok := s.identities.Get(identity); ok {
		return entry.PublicKeyBytes, nil
	}
	opts := idpkg.ResolveOptions{
		Scheme:       s.cfg.RelayScheme,
		AllowPrivate: s.cfg.ResolverAllowPrivate,
		TXT:          s.resolver,
	}
	if s.cfg.ResolverAllowPrivate && s.cfg.RelayAddress != "" {
		opts.HTTPClient = s.virtualHostClient()
	}
	res, err := idpkg.Resolve(ctx, identity, opts)
	if err != nil {
		return nil, fmt.Errorf("identity public key not found for %s: %w", identity, err)
	}
	pub, err := idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// virtualHostClient dials this relay's address while preserving the request Host,
// so well-known fetches work in tests (and any setup) where the identity FQDN
// is not in system DNS but is Host-routed on this relay.
func (s *Server) virtualHostClient() *http.Client {
	dialAddr := s.cfg.RelayAddress
	return &http.Client{
		Timeout: idpkg.DefaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("redirects are not allowed when resolving identity documents")
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: idpkg.DefaultTimeout}).DialContext(ctx, network, dialAddr)
			},
		},
	}
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

func (s *Server) forwardMessage(ctx context.Context, msg Message) (result error) {
	defer func() {
		outcome := "success"
		if result != nil {
			outcome = "failure"
		}
		s.event(ctx, "message.forward", outcome)
	}()
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
func (s *Server) forwardAck(ctx context.Context, ack Ack) (result error) {
	defer func() {
		outcome := "success"
		if result != nil {
			outcome = "failure"
		}
		s.event(ctx, "ack.forward", outcome)
	}()
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
// already resolved from DNS/web. Subsequent requests hit the store fast path.
// No-op if the identity was registered (already present with a document).
func (s *Server) warmIdentityCache(identity string, pub ed25519.PublicKey) {
	s.identities.Warm(storage.Identity{
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
	if tw, ok := w.(interface{ telemetryError(string) }); ok {
		tw.telemetryError(code)
	}
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
