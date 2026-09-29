package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	idpkg "github.com/poweur/identity"
)

// Remote groups as share members (E20-T7). A group identity hosted on
// another relay cannot be read from here, but its roster is self-verifying:
// a member presents it (X-Poweur-Group-Roster, base64url of the signed
// group.json) with any drive request. This relay checks the group's
// signature, that the caller is on the roster, and that the roster's epoch
// is the one the group's own relay reports now (GET /groups/{group}/epoch,
// public). A member dropped from the roster is refused as soon as the epoch
// moves, and the entry is rechecked at least every remoteRosterTTL. The last
// roster verified per group is kept under relay/group-rosters/, so a newer
// one can be diffed and departed members revoked like local ones.

const (
	groupRosterHeader = "X-Poweur-Group-Roster"
	remoteRosterTTL   = time.Minute
	maxRosterHeader   = 64 << 10
)

var errStaleRoster = errors.New("group roster is not the group's current epoch")

// handleGroupEpoch tells anyone the current membership epoch of a group
// identity hosted here — 0 for an identity that is not a group, so the
// answer does not reveal which identities are groups.
func (s *Server) handleGroupEpoch(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("group")))
	if !s.identities.Exists(name) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted here")
		return
	}
	epoch := 0
	if gr, err := s.groupIdentity(r.Context(), name); err == nil {
		epoch = gr.Epoch
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]int{"epoch": epoch})
}

// remoteGroupEpoch asks a group's relay for its current epoch.
func (s *Server) remoteGroupEpoch(ctx context.Context, group string) (int, error) {
	host, err := s.resolveRelayHost(ctx, group)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s://%s/groups/%s/epoch", s.cfg.RelayScheme, host, group), nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("group relay answered %d", resp.StatusCode)
	}
	var body struct {
		Epoch int `json:"epoch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
		return 0, err
	}
	return body.Epoch, nil
}

func rosterKey(group string) string {
	return "relay/group-rosters/" + sanitizedOrEmpty(group) + ".json"
}

// storedRoster is the last roster this relay verified for a remote group.
func (s *Server) storedRoster(ctx context.Context, group string) (idpkg.ShareGroup, bool) {
	if s.drive == nil || sanitizedOrEmpty(group) == "" {
		return idpkg.ShareGroup{}, false
	}
	obj, err := s.drive.Get(ctx, rosterKey(group), nil)
	if err != nil {
		return idpkg.ShareGroup{}, false
	}
	gr, err := idpkg.ParseShareGroup(obj.Data)
	return gr, err == nil
}

// presentRoster accepts a remote group's roster offered by actor.
func (s *Server) presentRoster(ctx context.Context, header, actor string) error {
	if len(header) > maxRosterHeader {
		return errors.New("group roster too large")
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return errors.New("group roster must be base64url")
	}
	gr, err := idpkg.ParseShareGroup(raw)
	if err != nil {
		return err
	}
	group := strings.ToLower(gr.Group)
	if s.identities.Exists(group) {
		return nil // hosted here: the local roster is authoritative
	}
	if !gr.IsGroupIdentity() || !strings.EqualFold(gr.Owner, group) {
		return errors.New("not a group identity's roster")
	}
	if !gr.HasMember(actor) && !gr.HasAdmin(actor) {
		return errors.New("the caller is not on this roster")
	}
	key, err := s.resolveIdentityPublicKey(ctx, group)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", group, err)
	}
	if err := gr.VerifySignature(key); err != nil {
		return err
	}
	current, err := s.remoteGroupEpoch(ctx, group)
	if err != nil {
		return fmt.Errorf("cannot reach %s's relay: %w", group, err)
	}
	if gr.Epoch != current {
		return errStaleRoster
	}
	return s.adoptRemoteRoster(ctx, group, gr, raw)
}

// adoptRemoteRoster caches a verified current roster and, when it replaces
// an older one, revokes the members it dropped.
func (s *Server) adoptRemoteRoster(ctx context.Context, group string, gr idpkg.ShareGroup, raw []byte) error {
	previous, known := s.storedRoster(ctx, group)
	if known && gr.Epoch < previous.Epoch {
		return errStaleRoster
	}
	entry := rosterEntry{members: lowerAll(gr.Members), admins: lowerAll(gr.Admins), group: true, remote: true, epoch: gr.Epoch, checked: time.Now()}
	s.rosters.mu.Lock()
	s.rosters.entries[group] = entry
	s.rosters.mu.Unlock()
	if known && gr.Epoch == previous.Epoch {
		return nil
	}
	if s.drive != nil {
		if _, err := s.drive.Put(ctx, rosterKey(group), raw); err != nil {
			return err
		}
	}
	if known {
		still := map[string]bool{}
		for _, name := range append(append([]string(nil), entry.members...), entry.admins...) {
			still[name] = true
		}
		var removed []string
		for _, name := range lowerAll(append(append([]string(nil), previous.Members...), previous.Admins...)) {
			if !still[name] {
				removed = append(removed, name)
				still[name] = true
			}
		}
		s.revokeOnDrives(ctx, group, removed)
	}
	return nil
}

// loadRemoteRoster restores a remote group's last verified roster after a
// restart, if its relay still reports that epoch.
func (s *Server) loadRemoteRoster(ctx context.Context, group string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entry := rosterEntry{}
	if gr, ok := s.storedRoster(ctx, group); ok {
		if current, err := s.remoteGroupEpoch(ctx, group); err == nil && current == gr.Epoch {
			entry = rosterEntry{members: lowerAll(gr.Members), admins: lowerAll(gr.Admins), group: true, remote: true, epoch: gr.Epoch, checked: time.Now()}
		}
	}
	s.rosters.mu.Lock()
	// A roster a member presented meanwhile wins over this restore.
	if existing, cached := s.rosters.entries[group]; !cached || !existing.group && entry.group {
		s.rosters.entries[group] = entry
	}
	delete(s.rosters.loading, group)
	s.rosters.mu.Unlock()
}

// recheckRemoteRoster confirms a cached remote roster is still current; a
// moved epoch drops it until a member presents the new one.
func (s *Server) recheckRemoteRoster(ctx context.Context, group string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	current, err := s.remoteGroupEpoch(ctx, group)
	s.rosters.mu.Lock()
	defer s.rosters.mu.Unlock()
	delete(s.rosters.loading, group)
	entry := s.rosters.entries[group]
	switch {
	case err != nil:
		// Unreachable: keep serving what we verified; try again next time.
	case current != entry.epoch:
		s.rosters.entries[group] = rosterEntry{remote: true, epoch: entry.epoch}
	default:
		entry.checked = time.Now()
		s.rosters.entries[group] = entry
	}
}
