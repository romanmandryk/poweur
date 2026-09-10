package storage

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	idpkg "github.com/poweur/identity"
)

// Identity is a locally hosted Poweur ID. DocumentJSON is the signed id.json
// bytes when available (hosted / web identity). Sessions remain memory-only;
// only identity documents are durable when a data directory is configured.
type Identity struct {
	Identity            string
	PublicKey           string
	PublicKeyBytes      ed25519.PublicKey
	EncryptionPublicKey string
	Relay               string
	DocumentJSON        []byte
	CreatedAt           time.Time
}

type IdentityStore struct {
	mu         sync.RWMutex
	identities map[string]Identity
	dataDir    string // empty = memory-only
}

// NewIdentityStore returns an in-memory identity store (no durability).
func NewIdentityStore() *IdentityStore {
	return &IdentityStore{
		identities: make(map[string]Identity),
	}
}

// OpenIdentityStore opens a durable store under dataDir/identities/.
// If dataDir is empty, behaves like NewIdentityStore.
func OpenIdentityStore(dataDir string) (*IdentityStore, error) {
	s := &IdentityStore{
		identities: make(map[string]Identity),
		dataDir:    strings.TrimSpace(dataDir),
	}
	if s.dataDir == "" {
		return s, nil
	}
	if err := os.MkdirAll(s.identitiesRoot(), 0o700); err != nil {
		return nil, err
	}
	if err := s.loadAll(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *IdentityStore) identitiesRoot() string {
	return filepath.Join(s.dataDir, "identities")
}

func (s *IdentityStore) identityDocPath(identity string) (string, error) {
	dirName, err := idpkg.SanitizeIdentityDirName(identity)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.identitiesRoot(), dirName, "poweur-sys", "public", "id.json"), nil
}

func (s *IdentityStore) loadAll() error {
	entries, err := os.ReadDir(s.identitiesRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		docPath := filepath.Join(s.identitiesRoot(), e.Name(), "poweur-sys", "public", "id.json")
		raw, err := os.ReadFile(docPath)
		if err != nil {
			continue
		}
		doc, err := idpkg.ParseDocument(raw, true)
		if err != nil {
			return fmt.Errorf("load %s: %w", docPath, err)
		}
		ident, err := identityFromDocument(doc, raw)
		if err != nil {
			return err
		}
		s.identities[strings.ToLower(ident.Identity)] = ident
	}
	return nil
}

func identityFromDocument(doc idpkg.IdentityDocument, raw []byte) (Identity, error) {
	pubStr := idpkg.NormalizePublicKeyKey(doc.PublicKey)
	pubBytes, err := idpkg.ParseEd25519PublicKey(doc.PublicKey)
	if err != nil {
		return Identity{}, err
	}
	created := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, doc.UpdatedAt); err == nil {
		created = t
	}
	enc := doc.EncryptionPublicKey
	if strings.HasPrefix(enc, "x25519:") {
		enc = strings.TrimPrefix(enc, "x25519:")
	}
	return Identity{
		Identity:            strings.ToLower(doc.Identity),
		PublicKey:           pubStr,
		PublicKeyBytes:      pubBytes,
		EncryptionPublicKey: enc,
		Relay:               doc.Relay,
		DocumentJSON:        raw,
		CreatedAt:           created,
	}, nil
}

// Add inserts an identity if it does not already exist. When a data directory
// is configured, the signed document is written atomically to disk.
func (s *IdentityStore) Add(identity Identity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(identity.Identity)
	if _, exists := s.identities[key]; exists {
		return false
	}
	if s.dataDir != "" {
		if len(identity.DocumentJSON) == 0 {
			return false
		}
		if err := s.writeDocumentLocked(identity); err != nil {
			return false
		}
	}
	s.identities[key] = identity
	return true
}

// Put upserts an identity (used for encryption-key updates and DNS registrations
// that synthesize a document).
func (s *IdentityStore) Put(identity Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(identity.Identity)
	if s.dataDir != "" {
		if len(identity.DocumentJSON) == 0 {
			return fmt.Errorf("document required for durable store")
		}
		if err := s.writeDocumentLocked(identity); err != nil {
			return err
		}
	}
	s.identities[key] = identity
	return nil
}

func (s *IdentityStore) writeDocumentLocked(identity Identity) error {
	path, err := s.identityDocPath(identity.Identity)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, identity.DocumentJSON, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *IdentityStore) Get(name string) (Identity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.identities[strings.ToLower(name)]
	return identity, ok
}

func (s *IdentityStore) Exists(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.identities[strings.ToLower(name)]
	return ok
}

// DataDir returns the configured POWEUR_DATA root (may be empty).
func (s *IdentityStore) DataDir() string {
	return s.dataDir
}

// IdentityHomeDir returns the on-disk directory for an identity, or "" if
// the store is memory-only.
func (s *IdentityStore) IdentityHomeDir(identity string) (string, error) {
	if s.dataDir == "" {
		return "", nil
	}
	dirName, err := idpkg.SanitizeIdentityDirName(identity)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.identitiesRoot(), dirName), nil
}

// DocumentJSON returns the raw signed id.json for an identity, if present.
func (s *IdentityStore) DocumentJSON(name string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ident, ok := s.identities[strings.ToLower(name)]
	if !ok || len(ident.DocumentJSON) == 0 {
		return nil, false
	}
	return ident.DocumentJSON, true
}

// Warm caches a resolved public key in memory without writing to disk.
// Used after DNS/web resolution so subsequent requests hit the store.
func (s *IdentityStore) Warm(identity Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(identity.Identity)
	if existing, ok := s.identities[key]; ok {
		if len(existing.DocumentJSON) > 0 {
			return // do not overwrite durable hosted entries
		}
	}
	s.identities[key] = identity
}

// UpdateEncryptionKey updates the encryption public key and document on disk.
func (s *IdentityStore) UpdateEncryptionKey(name, encKey string, documentJSON []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(name)
	ident, ok := s.identities[key]
	if !ok {
		return false
	}
	ident.EncryptionPublicKey = encKey
	if len(documentJSON) > 0 {
		ident.DocumentJSON = documentJSON
		if s.dataDir != "" {
			if err := s.writeDocumentLocked(ident); err != nil {
				return false
			}
		}
	}
	s.identities[key] = ident
	return true
}

// Ensure document is valid JSON (test helper / sanity).
func ValidateDocumentJSON(raw []byte) error {
	var v json.RawMessage
	return json.Unmarshal(raw, &v)
}

// Names is a snapshot for aggregate operational gauges, never telemetry labels.
func (s *IdentityStore) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.identities))
	for id := range s.identities {
		out = append(out, id)
	}
	return out
}
