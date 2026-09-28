package relay

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	idpkg "github.com/poweur/identity"
)

// Groups as share members (E20-T7). A share may name a group identity
// hosted on this relay; its verified roster decides who the share reaches.
// The drive engine asks rosterCache, which never touches a drive (the
// engine calls it with a drive locked): entries are loaded in the
// background — at start-up and on first use — and replaced synchronously
// whenever the relay accepts a new group.json. When a roster loses members,
// every drive that shares with the group journals a group revocation, so the
// keys those members held are rotated before new content. The drives to
// visit are indexed under relay/group-shares/<group>/<drive>.

type rosterEntry struct {
	members, admins []string
	group           bool
}

type rosterCache struct {
	mu      sync.Mutex
	entries map[string]rosterEntry
	loading map[string]bool
}

func newRosterCache() *rosterCache {
	return &rosterCache{entries: map[string]rosterEntry{}, loading: map[string]bool{}}
}

// resolve answers from the cache; a miss for a local identity starts a load
// and answers "not a group" until it lands.
func (s *Server) resolveGroup(group string) (members, admins []string, ok bool) {
	group = strings.ToLower(group)
	c := s.rosters
	c.mu.Lock()
	entry, cached := c.entries[group]
	start := !cached && !c.loading[group] && s.identities.Exists(group)
	if start {
		c.loading[group] = true
	}
	c.mu.Unlock()
	if start {
		go s.loadRoster(context.Background(), group)
	}
	return entry.members, entry.admins, cached && entry.group
}

// loadRoster reads and verifies a group identity's roster into the cache.
func (s *Server) loadRoster(ctx context.Context, group string) rosterEntry {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entry := rosterEntry{}
	if gr, err := s.groupIdentity(ctx, group); err == nil {
		entry = rosterEntry{members: lowerAll(gr.Members), admins: lowerAll(gr.Admins), group: true}
	}
	c := s.rosters
	c.mu.Lock()
	c.entries[group] = entry
	delete(c.loading, group)
	c.mu.Unlock()
	return entry
}

func lowerAll(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = strings.ToLower(strings.TrimSpace(name))
	}
	return out
}

// warmRosters loads every hosted group identity's roster in the background.
func (s *Server) warmRosters() {
	for _, name := range s.identities.Names() {
		select {
		case <-s.stop:
			return
		default:
		}
		s.resolveGroupSync(name)
	}
}

func (s *Server) resolveGroupSync(group string) {
	s.rosters.mu.Lock()
	_, cached := s.rosters.entries[group]
	s.rosters.mu.Unlock()
	if !cached {
		s.loadRoster(context.Background(), group)
	}
}

// groupRosterChanged runs after the relay stored (or deleted) a group
// identity's group.json: refresh the cache, then revoke departed members on
// every drive that shares with the group, and on the group's own drive.
func (s *Server) groupRosterChanged(ctx context.Context, group string) {
	group = strings.ToLower(group)
	s.rosters.mu.Lock()
	old := s.rosters.entries[group]
	s.rosters.mu.Unlock()
	updated := s.loadRoster(ctx, group)
	if s.engine == nil {
		return
	}
	still := map[string]bool{}
	for _, name := range append(append([]string(nil), updated.members...), updated.admins...) {
		still[name] = true
	}
	var removed []string
	for _, name := range append(append([]string(nil), old.members...), old.admins...) {
		if !still[name] {
			removed = append(removed, name)
			still[name] = true // once
		}
	}
	if len(removed) == 0 {
		return
	}
	drives := map[string]bool{group: true}
	if s.drive != nil {
		prefix := "relay/group-shares/" + sanitizedOrEmpty(group) + "/"
		for cursor := ""; ; {
			page, err := s.drive.List(ctx, prefix, cursor, 1000)
			if err != nil {
				break
			}
			for _, obj := range page.Objects {
				drives[strings.ReplaceAll(strings.TrimPrefix(obj.Key, prefix), "__", ".")] = true
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	for driveID := range drives {
		if !s.identities.Exists(driveID) {
			continue
		}
		if err := s.engine.RevokeGroupMembers(ctx, driveID, group, removed); err != nil {
			log.Printf("group %s: revoking departed members on a drive failed: %v", group, err)
		}
	}
}

// noteGroupShare indexes that driveID shares with member, if member is an
// identity hosted here (it may be, or become, a group).
func (s *Server) noteGroupShare(member, driveID string) {
	if s.drive == nil || member == "" || strings.HasPrefix(member, "link:") || !s.identities.Exists(member) {
		return
	}
	key := sanitizedOrEmpty(member) + "/" + sanitizedOrEmpty(driveID)
	s.rosters.mu.Lock()
	if s.groupShareNoted == nil {
		s.groupShareNoted = map[string]bool{}
	}
	seen := s.groupShareNoted[key]
	s.groupShareNoted[key] = true
	s.rosters.mu.Unlock()
	if seen || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = s.drive.Put(ctx, "relay/group-shares/"+key, []byte("{}"))
	}()
}

func sanitizedOrEmpty(identity string) string {
	name, err := idpkg.SanitizeIdentityDirName(identity)
	if err != nil {
		return ""
	}
	return name
}
