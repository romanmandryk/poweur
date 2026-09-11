package relay

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
)

const signInGrantTTL = time.Hour

type SignInGrantRequest struct {
	Response string `json:"response"`
}

type SignInGrantResponse struct {
	Token     string `json:"token"`
	Identity  string `json:"identity"`
	Audience  string `json:"audience"`
	AppID     string `json:"app_id"`
	Scope     string `json:"scope"`
	Path      string `json:"path"`
	ExpiresAt string `json:"expires_at"`
	DAVURL    string `json:"dav_url"`
}

func (s *Server) handleSignInGrantPost(w http.ResponseWriter, r *http.Request) {
	if s.filesProvider == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this relay has no durable file layer")
		return
	}
	var body SignInGrantRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	approval, err := idpkg.DecodeSignInResponse(body.Response)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_approval", err.Error())
		return
	}
	identityName := strings.ToLower(strings.TrimSpace(approval.Identity))
	stored, ok := s.identities.Get(identityName)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "approval identity is not hosted on this relay")
		return
	}
	now := time.Now().UTC()
	requestShape := idpkg.SignInRequest{
		PoweurAuth: approval.PoweurAuth, RequestID: approval.RequestID,
		Audience: approval.Audience, Nonce: approval.Nonce,
		IssuedAt: approval.IssuedAt, ExpiresAt: approval.ExpiresAt,
		Action: approval.Action, Statement: approval.Statement, Scopes: approval.Scopes,
	}
	if err := requestShape.Validate(now); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_approval", err.Error())
		return
	}
	verificationKey := ed25519.PublicKey(stored.PublicKeyBytes)
	if sessionID := idpkg.SessionIDFromKeyID(approval.KeyID); sessionID != "" {
		if approval.SessionProof == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session-signed approval has no proof")
			return
		}
		verified, err := idpkg.VerifySessionProof(verificationKey, identityName, *approval.SessionProof, now)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session proof invalid: "+err.Error())
			return
		}
		verificationKey = verified.PublicKeyBytes
	} else if approval.KeyID != idpkg.SignInKeyIDIdentity {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown signing key")
		return
	}
	sig, err := idpkg.DecodeAnyBase64(approval.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize ||
		!ed25519.Verify(verificationKey, []byte(approval.Canonical()), sig) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "approval signature invalid")
		return
	}
	appID, err := idpkg.SignInAppID(approval.Audience)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_approval", err.Error())
		return
	}
	scopeRaw, ok := strongestDAVScope(approval.Scopes)
	if !ok {
		writeError(w, http.StatusBadRequest, "no_resource_scope", "approval contains no DAV resource scope")
		return
	}
	scope, err := files.ParseScope(scopeRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	path, _, _ := idpkg.SignInScopePath(scopeRaw)
	expires := now.Add(signInGrantTTL)
	app := idpkg.ConnectedApp{
		AppID: appID, Audience: approval.Audience, Scopes: approval.Scopes,
		GrantedAt: now.Format(time.RFC3339), ExpiresAt: expires.Format(time.RFC3339),
	}
	if err := s.upsertConnectedApp(r.Context(), identityName, app); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "connected app record: "+err.Error())
		return
	}
	// The app namespace itself is platform-owned scaffolding. Apps create
	// their own children through DAV after receiving the token.
	_ = s.filesProvider.Mkdir(r.Context(), identityName, path)
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_failed", err.Error())
		return
	}
	token = "app_" + token
	s.davTokens.Put(davToken{
		Token: token, Identity: identityName, Audience: identityName,
		Scope: scope, ScopeRaw: scopeRaw, ExpiresAt: expires, AppID: appID,
	})
	scheme := s.cfg.RelayScheme
	if scheme == "" {
		scheme = "https"
	}
	relayBase := scheme + "://" + s.cfg.RelayAddress
	writeJSON(w, http.StatusCreated, SignInGrantResponse{
		Token: token, Identity: identityName, Audience: approval.Audience,
		AppID: appID, Scope: scopeRaw, Path: path,
		ExpiresAt: expires.Format(time.RFC3339),
		DAVURL:    strings.TrimRight(relayBase, "/") + "/dav/" + identityName + "/" + path,
	})
}

func strongestDAVScope(scopes []string) (string, bool) {
	var dav []string
	for _, scope := range scopes {
		if strings.HasPrefix(scope, idpkg.ScopeDAVRWPfx) || strings.HasPrefix(scope, idpkg.ScopeDAVReadPfx) {
			dav = append(dav, scope)
		}
	}
	if len(dav) == 0 {
		return "", false
	}
	sort.SliceStable(dav, func(i, j int) bool {
		return strings.HasPrefix(dav[i], idpkg.ScopeDAVRWPfx) && !strings.HasPrefix(dav[j], idpkg.ScopeDAVRWPfx)
	})
	return dav[0], true
}

func (s *Server) readConnectedApps(ctx context.Context, owner string) (idpkg.ConnectedApps, error) {
	f, err := s.filesProvider.OpenFile(ctx, owner, idpkg.ConnectedAppsPath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return idpkg.ConnectedApps{Version: idpkg.ConnectedAppsVersion, Apps: []idpkg.ConnectedApp{}}, nil
		}
		return idpkg.ConnectedApps{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, idpkg.MaxDocumentBytes+1))
	if err != nil {
		return idpkg.ConnectedApps{}, err
	}
	if len(raw) > idpkg.MaxDocumentBytes {
		return idpkg.ConnectedApps{}, fmt.Errorf("connected-apps.json exceeds size limit")
	}
	return idpkg.ParseConnectedApps(raw)
}

func (s *Server) upsertConnectedApp(ctx context.Context, owner string, app idpkg.ConnectedApp) error {
	s.connectedMu.Lock()
	defer s.connectedMu.Unlock()
	doc, err := s.readConnectedApps(ctx, owner)
	if err != nil {
		return err
	}
	doc = doc.Upsert(app)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	f, err := s.filesProvider.OpenFile(ctx, owner, idpkg.ConnectedAppsPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}

func (s *Server) connectedAppActive(ctx context.Context, owner, appID string) bool {
	s.connectedMu.Lock()
	defer s.connectedMu.Unlock()
	doc, err := s.readConnectedApps(ctx, owner)
	if err != nil {
		return false
	}
	_, ok := doc.Active(appID, time.Now().UTC())
	return ok
}
