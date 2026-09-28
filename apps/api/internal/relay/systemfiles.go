package relay

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/storage"
)

// System files (EPIC-020 E20-T6) are the small documents in an identity's
// drive that the relay itself reads or writes: the public identity files it
// serves, the settings it enforces and the state it records. They live under
// `.poweur/` and nowhere else; everything outside `.poweur/` is end-to-end
// encrypted and never read by the relay.
//
// A relay with a drive store keeps them in each identity's drive, in the
// journalled system zone (driveSystemFiles). A relay without persistence
// keeps them in memory.
const (
	// sysPublicDir is world-readable: identity documents, profile,
	// capabilities and the avatar, served under /.well-known/poweur/.
	sysPublicDir = ".poweur/public"
	// sysRelayDir is written by the owner and read by the relay: settings
	// the relay enforces (contacts, inbox policy, analytics consent, group
	// rosters).
	sysRelayDir = ".poweur/relay"
	// sysStateDir is written by the relay and read by the owner: records the
	// relay keeps on the owner's behalf (device registry).
	sysStateDir = ".poweur/state"
)

// Paths of the individual system documents.
const (
	inboxPolicyPath  = sysRelayDir + "/inbox-policy.json"
	contactsPath     = sysRelayDir + "/contacts.json"
	analyticsPath    = sysRelayDir + "/analytics.json"
	groupRosterPath  = sysRelayDir + "/group.json"
	profilePath      = sysPublicDir + "/profile.json"
	capabilitiesPath = sysPublicDir + "/capabilities.json"
	devicesDocPath   = sysStateDir + "/devices.json"
)

// errSysFileNotFound reports an absent system document. Callers treat it as
// "use the document's defaults", never as a failure.
var errSysFileNotFound = errors.New("system file not found")

// errSystemFilesUnavailable reports a relay that cannot persist system
// documents (storage v2 not configured).
var errSystemFilesUnavailable = errors.New("system files are not available on this relay")

// SystemFiles is the relay's access to identities' system documents.
type SystemFiles interface {
	// Read returns a document, or errSysFileNotFound when it is absent.
	Read(ctx context.Context, identity, path string) ([]byte, error)
	// Write replaces a document.
	Write(ctx context.Context, identity, path string, data []byte) error
	// Delete removes a document, or returns errSysFileNotFound.
	Delete(ctx context.Context, identity, path string) error
}

// noSystemFiles stores nothing: every document is absent and every write is
// refused. It backs tests that must prove a feature fails open.
type noSystemFiles struct{}

func (noSystemFiles) Read(context.Context, string, string) ([]byte, error) {
	return nil, errSysFileNotFound
}

func (noSystemFiles) Write(context.Context, string, string, []byte) error {
	return errSystemFilesUnavailable
}

// memSystemFiles keeps system documents in memory. Tests use it to stand in
// for the drive; it is also a valid backing for a relay without persistence.
type memSystemFiles struct {
	mu   sync.Mutex
	docs map[string][]byte
}

func newMemSystemFiles() *memSystemFiles {
	return &memSystemFiles{docs: map[string][]byte{}}
}

func memSysKey(identity, path string) string {
	return strings.ToLower(strings.TrimSpace(identity)) + "\x00" + path
}

func (m *memSystemFiles) Read(_ context.Context, identity, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.docs[memSysKey(identity, path)]
	if !ok {
		return nil, errSysFileNotFound
	}
	return append([]byte(nil), raw...), nil
}

func (m *memSystemFiles) Write(_ context.Context, identity, path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[memSysKey(identity, path)] = append([]byte(nil), data...)
	return nil
}

// mirrorIdentityDocument keeps the signed id.json in the identity's drive at
// `.poweur/public/id.json`, beside the rest of its public files, so a synced
// drive holds it. The relay's identity index (relay/identities/) stays the
// source it serves from; a failed mirror is repaired by the next write.
func (s *Server) mirrorIdentityDocument(ctx context.Context, identity storage.Identity) {
	if s.engine == nil || len(identity.DocumentJSON) == 0 {
		return
	}
	_ = s.sysFiles.Write(ctx, identity.Identity, sysPublicDir+"/id.json", identity.DocumentJSON)
}

// readSysJSON reads a system document for identity, capped at
// maxSysDocBytes. It returns nil when the document is absent, unreadable or
// oversized — absence must fail open to the document's defaults.
func (s *Server) readSysJSON(ctx context.Context, identity, path string) []byte {
	if s.sysFiles == nil {
		return nil
	}
	raw, err := s.sysFiles.Read(ctx, identity, path)
	if err != nil || len(raw) > maxSysDocBytes {
		return nil
	}
	return raw
}

// maxSysDocBytes caps any system document the relay reads as settings.
const maxSysDocBytes = 64 * 1024

// sysZones are the directories a system file may live in.
var sysZones = []string{sysPublicDir, sysRelayDir, sysStateDir}

// validSysPath accepts exactly `<zone>/<name>`: one of sysZones and one flat
// file name of lowercase letters, digits, '-', '_' and '.', not starting with
// a dot. Anything else — nesting, traversal, other roots — is refused before
// it can become a path on disk.
func validSysPath(path string) bool {
	for _, zone := range sysZones {
		name, ok := strings.CutPrefix(path, zone+"/")
		if !ok {
			continue
		}
		if name == "" || len(name) > 64 || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
			return false
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
		return true
	}
	return false
}

// Delete removes a document from memory.
func (m *memSystemFiles) Delete(_ context.Context, identity, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memSysKey(identity, path)
	if _, ok := m.docs[key]; !ok {
		return errSysFileNotFound
	}
	delete(m.docs, key)
	return nil
}

func (noSystemFiles) Delete(context.Context, string, string) error { return errSysFileNotFound }

// driveSystemFiles keeps system documents in each identity's drive (the
// journalled system zone), so they live wherever the drive lives — disk or
// S3 — and survive the relay losing everything else. The writer follows the
// zone: `.poweur/state/` is the relay's, everything else the owner's.
type driveSystemFiles struct {
	engine *engine.Engine
}

func sysWriter(path string) string {
	if strings.HasPrefix(path, sysStateDir+"/") {
		return engine.WriterRelay
	}
	return engine.WriterOwner
}

func sysFileError(err error) error {
	if errors.Is(err, engine.ErrNotFound) {
		return errSysFileNotFound
	}
	return err
}

func (d driveSystemFiles) Read(ctx context.Context, identity, path string) ([]byte, error) {
	raw, _, err := d.engine.SystemRead(ctx, identity, path)
	return raw, sysFileError(err)
}

func (d driveSystemFiles) Write(ctx context.Context, identity, path string, data []byte) error {
	_, err := d.engine.SystemWrite(ctx, identity, path, data, sysWriter(path), nil)
	return err
}

func (d driveSystemFiles) Delete(ctx context.Context, identity, path string) error {
	return sysFileError(d.engine.SystemDelete(ctx, identity, path, sysWriter(path), nil))
}
