package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Handles elsewhere (INT-006-T5, PCP-0011): an ID that links a Bluesky DID or
// a Nostr key in .poweur/public/handles.json gets the discovery files those
// networks look for, served from its own name. Nothing is served for an ID
// that did not opt in.

// handlesPath is the owner's handles document.
const handlesPath = idpkg.HandlesPath

// readHandles returns an identity's handles, if it published valid ones and
// is not held by the operator.
func (s *Server) readHandles(ctx context.Context, identity string) (idpkg.Handles, bool) {
	if !s.identities.Exists(identity) {
		return idpkg.Handles{}, false
	}
	if _, held := s.held(identity); held {
		return idpkg.Handles{}, false
	}
	raw := s.readSysJSON(ctx, identity, handlesPath)
	if raw == nil {
		return idpkg.Handles{}, false
	}
	h, err := idpkg.ParseHandles(raw)
	return h, err == nil
}

// handleATProtoDID answers GET /.well-known/atproto-did on an identity's host:
// the DID Bluesky checks when someone sets the ID as their handle.
func (s *Server) handleATProtoDID(w http.ResponseWriter, r *http.Request) {
	h, ok := s.readHandles(r.Context(), requestHost(r))
	if !ok || h.ATProtoDID == "" {
		writeError(w, http.StatusNotFound, "not_found", "no atproto DID linked to this ID")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(h.ATProtoDID + "\n"))
}

// handleNostrJSON answers NIP-05 lookups (GET /.well-known/nostr.json?name=):
//
//   - on an identity's host, `_` names the identity itself (`_@alice.poweur.net`,
//     shown by clients as `alice.poweur.net`);
//   - on a hosted domain's apex, a label names the hosted ID under it, so
//     `alice@poweur.net` (the ID's email address, too) verifies as alice.poweur.net's key.
//
// Only the name asked for is answered: the relay never lists who opted in.
func (s *Server) handleNostrJSON(w http.ResponseWriter, r *http.Request) {
	host := requestHost(r)
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	identity := ""
	switch {
	case name == "_" && s.identities.Exists(host):
		identity = host
	case s.isHostedApex(host) && name != "" && name != "_":
		if !validNIP05Label(name) {
			break
		}
		identity = name + "." + host
	}
	names := map[string]string{}
	var relays map[string][]string
	if identity != "" {
		if h, ok := s.readHandles(r.Context(), identity); ok && h.NostrPubkey != "" {
			names[name] = h.NostrPubkey
			if len(h.NostrRelays) > 0 {
				relays = map[string][]string{h.NostrPubkey: h.NostrRelays}
			}
		}
	}
	if len(names) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no Nostr key linked to this name")
		return
	}
	body := map[string]any{"names": names}
	if relays != nil {
		body["relays"] = relays
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, body)
}

// handleWebFinger answers GET /.well-known/webfinger?resource=acct:<label>@<domain>
// on a hosted domain's apex for an ID that linked a Fediverse account. The
// subject is that account, which is WebFinger's redirect: Mastodon then
// confirms it at the account's own server, so searching `@alice@poweur.net`
// finds `@alice@mastodon.social` and nothing can be claimed that the linked
// server does not confirm.
func (s *Server) handleWebFinger(w http.ResponseWriter, r *http.Request) {
	host := requestHost(r)
	resource := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("resource")))
	user, domain, ok := strings.Cut(strings.TrimPrefix(resource, "acct:"), "@")
	if !ok || !strings.HasPrefix(resource, "acct:") || domain != host || !s.isHostedApex(host) || !validNIP05Label(user) {
		writeError(w, http.StatusNotFound, "not_found", "unknown resource")
		return
	}
	h, found := s.readHandles(r.Context(), user+"."+host)
	if !found || h.FediverseAcct == "" {
		writeError(w, http.StatusNotFound, "not_found", "unknown resource")
		return
	}
	body := map[string]any{
		"subject": "acct:" + h.FediverseAcct,
		"aliases": []string{h.FediverseActor},
		"links": []map[string]string{
			{"rel": "self", "type": "application/activity+json", "href": h.FediverseActor},
		},
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/jrd+json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// isHostedApex reports whether host is exactly one of the hosted domains.
func (s *Server) isHostedApex(host string) bool {
	for _, d := range s.cfg.HostedDomains {
		if strings.EqualFold(strings.TrimSuffix(d, "."), host) {
			return true
		}
	}
	return false
}

// validNIP05Label accepts one DNS label (what a hosted name is).
func validNIP05Label(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
