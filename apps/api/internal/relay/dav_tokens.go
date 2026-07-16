package relay

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/files"
)

// davToken is an opaque bearer credential for the WebDAV bridge (E03-T3).
// Tokens live in memory only and are revocable; a relay restart invalidates
// all of them (clients re-mint from their session/identity key).
type davToken struct {
	Token     string
	Identity  string // requester (the authenticated principal)
	Audience  string // tree owner the token grants access to
	Scope     files.Scope
	ScopeRaw  string
	ExpiresAt time.Time
}

type davTokenStore struct {
	mu     sync.Mutex
	tokens map[string]davToken
}

func newDAVTokenStore() *davTokenStore {
	return &davTokenStore{tokens: make(map[string]davToken)}
}

func (st *davTokenStore) Put(t davToken) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.tokens[t.Token] = t
}

func (st *davTokenStore) Get(token string) (davToken, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.tokens[token]
	if !ok || time.Now().After(t.ExpiresAt) {
		delete(st.tokens, token)
		return davToken{}, false
	}
	return t, true
}

func (st *davTokenStore) Revoke(token string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.tokens[token]
	delete(st.tokens, token)
	return ok
}

func (st *davTokenStore) Prune() {
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, t := range st.tokens {
		if now.After(t.ExpiresAt) {
			delete(st.tokens, k)
		}
	}
}

// DAVTokenRequest is the body of POST /auth/dav-token. Signed by a
// registered session key (session_id set) or the long-lived identity key.
type DAVTokenRequest struct {
	Identity  string `json:"identity"`
	Audience  string `json:"audience,omitempty"` // default: identity (own tree)
	Scope     string `json:"scope,omitempty"`    // default: dav:full (owner) / dav:read (visitor)
	IssuedAt  string `json:"issued_at"`
	Nonce     string `json:"nonce"`
	SessionID string `json:"session_id,omitempty"`
	Signature string `json:"signature"`
}

type DAVTokenResponse struct {
	Token     string `json:"token"`
	Identity  string `json:"identity"`
	Audience  string `json:"audience"`
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"`
}

const defaultDAVTokenTTL = 24 * time.Hour

// handleDAVTokenPost mints a WebDAV bearer token (E03-T3/T4). Visitors may
// request tokens for someone else's tree (audience != identity); they are
// capped to read-only and still only get what the layout grants them.
func (s *Server) handleDAVTokenPost(w http.ResponseWriter, r *http.Request) {
	var req DAVTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.Identity == "" || req.IssuedAt == "" || req.Nonce == "" || req.Signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing required fields (identity, issued_at, nonce, signature)")
		return
	}
	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	audience := strings.ToLower(strings.TrimSpace(req.Audience))
	identity := strings.ToLower(strings.TrimSpace(req.Identity))
	if audience == "" {
		audience = identity
	}
	owner := audience == identity

	scopeRaw := strings.TrimSpace(req.Scope)
	if scopeRaw == "" {
		if owner {
			scopeRaw = "dav:full"
		} else {
			scopeRaw = "dav:read"
		}
	}
	scope, err := files.ParseScope(scopeRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	if !owner && scope.Write {
		writeError(w, http.StatusForbidden, "invalid_scope", "visitor tokens are read-only in v1 (grants land with sharing)")
		return
	}

	// The audience tree must be hosted here — this relay can only grant
	// access to trees it stores.
	if !s.identities.Exists(audience) {
		writeError(w, http.StatusNotFound, "not_found", "audience identity is not hosted on this relay")
		return
	}

	// Verify the request signature: session key when session_id is present,
	// otherwise the requester's long-lived identity key via the resolver
	// chain — so visitors hosted on other relays/domains work (E03-T4).
	canonical := crypto.CanonicalDAVToken(identity, audience, scopeRaw, req.IssuedAt, req.Nonce)
	expiresAt := time.Now().UTC().Add(defaultDAVTokenTTL)
	if req.SessionID != "" {
		session, ok := s.sessions.Get(req.SessionID)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "session expired or not found")
			return
		}
		if !strings.EqualFold(session.Identity, identity) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session does not belong to identity")
			return
		}
		if err := crypto.VerifySignature(session.PublicKeyBytes, canonical, req.Signature); err != nil {
			s.noteDAVAuthFailure(r, audience, identity, "dav-token session signature invalid")
			writeError(w, http.StatusUnauthorized, "unauthorized", "signature invalid")
			return
		}
		if session.ExpiresAt.Before(expiresAt) {
			expiresAt = session.ExpiresAt // token never outlives the session
		}
	} else {
		pub, err := s.resolveIdentityPublicKey(r.Context(), identity)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
			return
		}
		if err := crypto.VerifySignature(pub, canonical, req.Signature); err != nil {
			s.noteDAVAuthFailure(r, audience, identity, "dav-token identity signature invalid")
			writeError(w, http.StatusUnauthorized, "unauthorized", "signature invalid")
			return
		}
	}

	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_failed", "failed to generate token")
		return
	}
	s.davTokens.Put(davToken{
		Token:     "dav_" + token,
		Identity:  identity,
		Audience:  audience,
		Scope:     scope,
		ScopeRaw:  scopeRaw,
		ExpiresAt: expiresAt,
	})
	writeJSON(w, http.StatusCreated, DAVTokenResponse{
		Token:     "dav_" + token,
		Identity:  identity,
		Audience:  audience,
		Scope:     scopeRaw,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

// handleDAVTokenDelete revokes a token. Possession of the token is the
// credential (mirrors session bearer semantics; revoked tokens fail
// immediately).
func (s *Server) handleDAVTokenDelete(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing token")
		return
	}
	if !s.davTokens.Revoke(token) {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
