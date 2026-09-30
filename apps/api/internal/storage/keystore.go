package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

// Keystore holds wrapped master-seed copies, one per enrolled authenticator
// (EPIC-011 E11-T1).
//
// Two properties define this store:
//
//  1. **The relay cannot read what it holds.** Wrapped is opaque ciphertext;
//     the wrapping secret never leaves the authenticator or device.
//  2. **It lives outside the DAV tree.** Entries need a read path the identity
//     key cannot provide (that is the whole point — recovering *from* nothing),
//     and a blob in the user's file tree would be one misplaced delete away
//     from destroying their recovery.

// ErrEnrollmentNotFound is returned when an enrollment id is not present.
var ErrEnrollmentNotFound = errors.New("enrollment not found")

// KeystoreEntry is one authenticator's wrapped copy of the master seed.
type KeystoreEntry struct {
	EnrollmentID string `json:"enrollment_id"`
	// Kind: passkey | hardware-key | cli-passphrase | recovery-kit | native.
	Kind string `json:"kind"`
	// Wrap: prf | passphrase | native.
	Wrap string `json:"wrap"`
	// CredentialID is the WebAuthn credential, for passkey-backed enrollments.
	CredentialID string `json:"credential_id,omitempty"`
	// CredentialPublicKey is SPKI DER (base64url) as returned by WebAuthn's
	// getPublicKey(). SPKI rather than raw COSE so verification stays in the
	// standard library instead of a CBOR/COSE parser.
	CredentialPublicKey string `json:"credential_public_key,omitempty"`
	// CredentialAlg is the COSE algorithm id (-7 ES256, -8 EdDSA, -257 RS256).
	CredentialAlg int `json:"credential_alg,omitempty"`
	// Wrapped is opaque ciphertext: {iv, ciphertext, salt?}. Never interpreted.
	Wrapped json.RawMessage `json:"wrapped"`
	Label   string          `json:"label,omitempty"`
	// Role: device | recovery-master (enforced by clients; E11-T2).
	Role       string `json:"role,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// KeystoreStore is the per-identity collection of enrollments.
type KeystoreStore struct {
	mu      sync.RWMutex
	byID    map[string][]KeystoreEntry
	objects Objects // nil = memory-only
}

const keystorePrefix = "relay/keystore/"

// NewKeystoreStore returns an in-memory keystore (no durability).
func NewKeystoreStore() *KeystoreStore {
	return &KeystoreStore{byID: make(map[string][]KeystoreEntry)}
}

// OpenKeystoreStore loads the keystore kept in objects under relay/keystore/.
func OpenKeystoreStore(objects Objects) (*KeystoreStore, error) {
	s := &KeystoreStore{
		byID:    make(map[string][]KeystoreEntry),
		objects: objects,
	}
	if objects == nil {
		return s, nil
	}
	return s, s.loadAll()
}

func (s *KeystoreStore) loadAll() error {
	return listAll(s.objects, keystorePrefix, func(info provider.Info) error {
		name := strings.TrimPrefix(info.Key, keystorePrefix)
		if strings.Contains(name, "/") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		raw, err := getObject(s.objects, info.Key)
		if err != nil {
			return err
		}
		var stored []KeystoreEntry
		if err := json.Unmarshal(raw, &stored); err != nil {
			return fmt.Errorf("keystore %s: %w", name, err)
		}
		s.byID[identityFromKey(strings.TrimSuffix(name, ".json"))] = stored
		return nil
	})
}

func (s *KeystoreStore) persistLocked(identity string) error {
	if s.objects == nil {
		return nil
	}
	segment, err := identityKey(identity)
	if err != nil {
		return err
	}
	key := keystorePrefix + segment + ".json"
	if len(s.byID[identity]) == 0 {
		return deleteObject(s.objects, key)
	}
	raw, err := json.MarshalIndent(s.byID[identity], "", "  ")
	if err != nil {
		return err
	}
	return putObject(s.objects, key, raw)
}

func (s *KeystoreStore) Put(identity string, entry KeystoreEntry) error {
	identity = strings.ToLower(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.byID[identity]
	replaced := false
	for i := range entries {
		if entries[i].EnrollmentID == entry.EnrollmentID {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].EnrollmentID < entries[j].EnrollmentID
	})
	s.byID[identity] = entries
	return s.persistLocked(identity)
}

// List returns every enrollment for an identity.
func (s *KeystoreStore) List(identity string) []KeystoreEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := s.byID[strings.ToLower(identity)]
	out := make([]KeystoreEntry, len(entries))
	copy(out, entries)
	return out
}

// Get returns one enrollment by id.
func (s *KeystoreStore) Get(identity, enrollmentID string) (KeystoreEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.byID[strings.ToLower(identity)] {
		if e.EnrollmentID == enrollmentID {
			return e, true
		}
	}
	return KeystoreEntry{}, false
}

// FindByCredential returns the enrollment holding a WebAuthn credential id.
func (s *KeystoreStore) FindByCredential(identity, credentialID string) (KeystoreEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.byID[strings.ToLower(identity)] {
		if e.CredentialID != "" && e.CredentialID == credentialID {
			return e, true
		}
	}
	return KeystoreEntry{}, false
}

// Remove deletes an enrollment. Removing the wrapped copy denies that
// authenticator the bootstrap read; it does NOT protect against an attacker
// who already extracted the seed — that is what rotation is for.
func (s *KeystoreStore) Remove(identity, enrollmentID string) error {
	identity = strings.ToLower(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.byID[identity]
	for i, e := range entries {
		if e.EnrollmentID == enrollmentID {
			s.byID[identity] = append(entries[:i:i], entries[i+1:]...)
			return s.persistLocked(identity)
		}
	}
	return ErrEnrollmentNotFound
}

// TouchLastUsed records a successful bootstrap read, for the inventory UI.
func (s *KeystoreStore) TouchLastUsed(identity, enrollmentID string, at time.Time) {
	identity = strings.ToLower(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.byID[identity]
	for i := range entries {
		if entries[i].EnrollmentID == enrollmentID {
			entries[i].LastUsedAt = at.UTC().Format(time.RFC3339)
			_ = s.persistLocked(identity)
			return
		}
	}
}
