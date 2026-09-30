package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Group keys (EPIC-024 E24-T3): the relay stores a group's keyring and
// public key, checks them against the group's own signature and current
// roster, and serves the keyring to members. It sees only sealed keys.

// maxGroupKeysBytes fits a sealed key for 1000 members and admins plus 128
// earlier epochs.
const maxGroupKeysBytes = 256 * 1024

// validateOwnGroupKeyring accepts a keyring for this group identity, signed
// with its key, at the roster's current epoch, sealed to exactly the group,
// its members and its admins.
func (s *Server) validateOwnGroupKeyring(ctx context.Context, identity string, raw []byte) error {
	ring, err := idpkg.ParseGroupKeyring(raw)
	if err != nil {
		return err
	}
	roster, err := s.ownGroupRoster(ctx, identity, ring.Group)
	if err != nil {
		return err
	}
	if err := s.verifyAsIdentity(identity, ring.VerifySignature); err != nil {
		return err
	}
	if ring.Epoch != roster.Epoch {
		return errors.New("the group keyring must be issued at the roster's current epoch")
	}
	want := map[string]bool{strings.ToLower(identity): true}
	for _, m := range roster.Members {
		want[strings.ToLower(strings.TrimSpace(m))] = true
	}
	for _, a := range roster.Admins {
		want[strings.ToLower(strings.TrimSpace(a))] = true
	}
	if len(ring.Sealed) != len(want) {
		return errors.New("the group key must be sealed to exactly the group, its members and its admins")
	}
	for id := range ring.Sealed {
		if !want[id] {
			return errors.New("the group key is sealed to someone outside the group")
		}
	}
	return nil
}

// validateOwnGroupPublicKey accepts the public key of the stored keyring.
func (s *Server) validateOwnGroupPublicKey(ctx context.Context, identity string, raw []byte) error {
	doc, err := idpkg.ParseGroupPublicKey(raw)
	if err != nil {
		return err
	}
	if _, err := s.ownGroupRoster(ctx, identity, doc.Group); err != nil {
		return err
	}
	if err := s.verifyAsIdentity(identity, doc.VerifySignature); err != nil {
		return err
	}
	stored, err := s.sysFiles.Read(ctx, identity, idpkg.GroupKeysDoc)
	if err != nil {
		return errors.New("write the group keyring before its public key")
	}
	ring, err := idpkg.ParseGroupKeyring(stored)
	if err != nil || ring.Epoch != doc.Epoch || ring.Public != doc.Public {
		return errors.New("the public key must be the stored keyring's current key")
	}
	return nil
}

// ownGroupRoster is this identity's own group.json, which must name it.
func (s *Server) ownGroupRoster(ctx context.Context, identity, named string) (idpkg.ShareGroup, error) {
	if !strings.EqualFold(named, identity) {
		return idpkg.ShareGroup{}, errors.New("a group key document must describe this identity")
	}
	raw, err := s.sysFiles.Read(ctx, identity, groupRosterPath)
	if err != nil {
		return idpkg.ShareGroup{}, errors.New("only a group identity has group keys")
	}
	roster, err := idpkg.ParseShareGroup(raw)
	if err != nil || !roster.IsGroupIdentity() {
		return idpkg.ShareGroup{}, errors.New("only a group identity has group keys")
	}
	return roster, nil
}

func (s *Server) verifyAsIdentity(identity string, verify func(ed25519.PublicKey) error) error {
	id, ok := s.identities.Get(identity)
	if !ok {
		return errors.New("identity not hosted here")
	}
	return verify(id.PublicKeyBytes)
}

// handleGroupKeysGet serves the keyring to a member or admin, with the same
// authentication and the same not-found answer to anyone else as the roster.
func (s *Server) handleGroupKeysGet(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("group")))
	caller, ok := s.authorizeGroupReader(w, r)
	if !ok {
		return
	}
	group, ok := s.groupMembership(w, r, name)
	if !ok {
		return
	}
	if !group.HasMember(caller) && !group.HasAdmin(caller) && !strings.EqualFold(caller, name) {
		writeError(w, http.StatusNotFound, "not_found", "no group identity here")
		return
	}
	raw, err := s.sysFiles.Read(r.Context(), name, idpkg.GroupKeysDoc)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "this group has no group key yet")
		return
	}
	requestAction(r, "group.keys.read")
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// handleGroupPublicKey serves a group's current public key without
// authentication — the same document as /.well-known/poweur/group-key.json
// on the group's host, reachable from a browser, which cannot set Host.
func (s *Server) handleGroupPublicKey(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("group")))
	if !s.identities.Exists(name) {
		writeError(w, http.StatusNotFound, "not_found", "no group key here")
		return
	}
	raw, err := s.sysFiles.Read(r.Context(), name, idpkg.GroupPublicKeyDoc)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no group key here")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(raw)
}
