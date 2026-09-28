package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// System files (EPIC-020 E20-T6) are the small documents in an identity's
// drive that the relay itself reads or writes: the public identity files it
// serves, the settings it enforces and the state it records. They live under
// `.poweur/` and nowhere else; everything outside `.poweur/` is end-to-end
// encrypted and never read by the relay.
//
// Storage v2 will back SystemFiles with the drive. Until then a relay with
// POWEUR_DATA keeps them as plain files under each identity's home
// (fileSystemFiles), at the same `.poweur/...` paths, so moving them into
// the drive is a copy. A relay without persistence keeps them in memory.
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

// fileSystemFiles keeps system documents as files under each identity's
// home directory, written atomically.
type fileSystemFiles struct {
	home func(identity string) (string, error)
}

func newFileSystemFiles(home func(identity string) (string, error)) *fileSystemFiles {
	return &fileSystemFiles{home: home}
}

func (f *fileSystemFiles) file(identity, path string) (string, error) {
	if !validSysPath(path) {
		return "", fmt.Errorf("invalid system file path %q", path)
	}
	home, err := f.home(strings.ToLower(strings.TrimSpace(identity)))
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errSystemFilesUnavailable
	}
	return filepath.Join(home, filepath.FromSlash(path)), nil
}

func (f *fileSystemFiles) Read(_ context.Context, identity, path string) ([]byte, error) {
	name, err := f.file(identity, path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errSysFileNotFound
	}
	return raw, err
}

func (f *fileSystemFiles) Write(_ context.Context, identity, path string, data []byte) error {
	name, err := f.file(identity, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

// Delete removes a document; removing an absent one is errSysFileNotFound.
func (f *fileSystemFiles) Delete(_ context.Context, identity, path string) error {
	name, err := f.file(identity, path)
	if err != nil {
		return err
	}
	if err := os.Remove(name); errors.Is(err, os.ErrNotExist) {
		return errSysFileNotFound
	} else {
		return err
	}
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
