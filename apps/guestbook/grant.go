package guestbook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// The second step: "connect your home" (EPIC-008 E08-T4).
//
// Login costs this relying party one signature check and no network call to
// anyone but the resolver. Writing into the user's storage is a *separate*
// exchange against a *different* server — the user's own relay — where the
// user's signed approval is the authorization grant and the relay is the
// resource server. The RP never holds a Poweur key and never sees the user's
// credentials; what it gets back is a bearer token confined to one path.

// Grant is a relay-minted, path-scoped credential for one user's home.
type Grant struct {
	Token     string    `json:"token"`
	Identity  string    `json:"identity"`
	Relay     string    `json:"relay"`
	Scope     string    `json:"scope"`
	Path      string    `json:"path"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GrantRequest is the body of POST /auth/grant at the user's relay. The whole
// request is the approval the user already signed: no client secret, no
// registration, nothing the RP could have forged.
type GrantRequest struct {
	Response string `json:"response"`
}

// GrantResponse is what the relay answers with.
type GrantResponse struct {
	Token     string `json:"token"`
	Identity  string `json:"identity"`
	Audience  string `json:"audience"`
	AppID     string `json:"app_id"`
	Scope     string `json:"scope"`
	Path      string `json:"path"`
	ExpiresAt string `json:"expires_at"`
	DAVURL    string `json:"dav_url"`
}

func (s *Server) httpClient() *http.Client {
	if s.cfg.HTTPClient != nil {
		return s.cfg.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// exchangeGrant presents the user's approval at their relay and receives a
// token scoped to this app's directory and nothing else.
//
// `encoded` is the approval exactly as it arrived. It is deliberately *not*
// re-serialized from the parsed object: the relay checks a signature over
// bytes, and round-tripping through a struct is how implementations drift.
func (s *Server) exchangeGrant(ctx context.Context, result *signin.Result, encoded string) (*Grant, error) {
	relay := strings.TrimRight(strings.TrimSpace(result.Relay), "/")
	if relay == "" {
		return nil, fmt.Errorf("identity %s publishes no relay", result.Identity)
	}
	if !strings.HasPrefix(relay, "http://") && !strings.HasPrefix(relay, "https://") {
		scheme := "https://"
		if strings.HasPrefix(s.cfg.Origin, "http://") {
			scheme = "http://"
		}
		relay = scheme + relay
	}
	body, err := json.Marshal(GrantRequest{Response: encoded})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relay+"/auth/grant", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("relay refused the grant (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out GrantResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("relay grant response is not JSON: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("relay returned no token")
	}
	// Trust but verify: the relay is the resource server, but a token that
	// claims a path outside this app's namespace is a bug (or a hostile
	// relay), and using it would make this RP the confused deputy.
	if err := identity.CheckSignInScopeNamespace(out.Scope, s.AppID()); err != nil {
		return nil, fmt.Errorf("relay minted a token outside this app's namespace: %w", err)
	}
	expires, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		expires = s.now().Add(time.Hour)
	}
	return &Grant{
		Token:     out.Token,
		Identity:  out.Identity,
		Relay:     relay,
		Scope:     out.Scope,
		Path:      out.Path,
		ExpiresAt: expires,
	}, nil
}

// writeToHome stores one entry in the user's own tree under this app's
// namespace, and returns the path it wrote to.
//
// This is the data-portability demonstration: the guestbook's copy is a
// cache. The record belongs to the user, sits in their storage next to
// everything else they own, and survives this site going away. Revoking the
// grant costs them the app, not the writing.
func (s *Server) writeToHome(ctx context.Context, sess *Session, entry Entry) (string, error) {
	grant := sess.Grant
	if grant == nil {
		return "", fmt.Errorf("no storage grant")
	}
	if s.now().After(grant.ExpiresAt) {
		return "", fmt.Errorf("storage grant expired — sign in again")
	}
	name := entryFileName(entry.At)
	path := strings.Trim(grant.Path, "/") + "/entries/" + name
	if err := s.ensureHomeCollection(ctx, grant, strings.Trim(grant.Path, "/")+"/entries"); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", err
	}
	url := joinURL(grant.Relay, "/dav/"+grant.Identity+"/"+path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+grant.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("relay refused the write (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return "/" + path, nil
}

func (s *Server) ensureHomeCollection(ctx context.Context, grant *Grant, path string) error {
	u := joinURL(grant.Relay, "/dav/"+grant.Identity+"/"+strings.Trim(path, "/"))
	req, err := http.NewRequestWithContext(ctx, "MKCOL", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+grant.Token)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusMethodNotAllowed {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return fmt.Errorf("relay refused app directory (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// entryFileName keeps entries sortable by time and confined to one path
// segment: nothing user-supplied reaches the filename.
func entryFileName(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t = time.Now().UTC()
	}
	return t.UTC().Format("20060102T150405Z") + ".json"
}
