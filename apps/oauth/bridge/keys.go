package bridge

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Key lifetimes. A retired key stays in the JWKS long enough for every token
// it signed to expire and for relying parties that cache the JWKS for a day
// to refetch it.
const (
	idTokenTTL      = 5 * time.Minute
	accessTokenTTL  = 10 * time.Minute
	codeTTL         = time.Minute
	retiredKeyGrace = 24*time.Hour + 2*idTokenTTL
	rsaKeyBits      = 2048
)

// KeyRing holds the issuer's signing keys. The newest active key signs; every
// key that may still verify a live token is published.
type KeyRing struct {
	mu    sync.RWMutex
	store *Store
	kek   []byte
	now   func() time.Time
	keys  []*signingKey // newest first
}

type signingKey struct {
	kid       string
	priv      *rsa.PrivateKey
	createdAt time.Time
	retiredAt time.Time
}

// LoadKeyRing loads the keys from store, creating the first one if there is
// none. kek is the 32-byte key-encryption key; keys are stored AES-256-GCM
// sealed and are useless without it.
func LoadKeyRing(ctx context.Context, store *Store, kek []byte, now func() time.Time) (*KeyRing, error) {
	if len(kek) != 32 {
		return nil, errors.New("key-encryption key must be 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	kr := &KeyRing{store: store, kek: kek, now: now}
	if err := kr.reload(ctx); err != nil {
		return nil, err
	}
	if len(kr.active()) == 0 {
		if _, err := kr.Rotate(ctx); err != nil {
			return nil, err
		}
	}
	return kr, nil
}

// NewMemoryKeyRing is a ring that is never persisted. Tests.
func NewMemoryKeyRing(now func() time.Time) (*KeyRing, error) {
	if now == nil {
		now = time.Now
	}
	priv, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, err
	}
	return NewMemoryKeyRingFromKey(priv, now)
}

// NewMemoryKeyRingFromKey wraps an existing key. Tests that build many
// bridges share one key instead of generating RSA keys each time.
func NewMemoryKeyRingFromKey(priv *rsa.PrivateKey, now func() time.Time) (*KeyRing, error) {
	if now == nil {
		now = time.Now
	}
	k, err := newSigningKey(priv, now())
	if err != nil {
		return nil, err
	}
	return &KeyRing{now: now, keys: []*signingKey{k}}, nil
}

func newSigningKey(priv *rsa.PrivateKey, created time.Time) (*signingKey, error) {
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(der)
	return &signingKey{
		kid:       base64.RawURLEncoding.EncodeToString(sum[:12]),
		priv:      priv,
		createdAt: created.UTC(),
	}, nil
}

func (kr *KeyRing) reload(ctx context.Context) error {
	rows, err := kr.store.db.QueryContext(ctx,
		`SELECT kid, created_at, retired_at, data FROM signing_keys ORDER BY created_at DESC, kid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var keys []*signingKey
	for rows.Next() {
		var kid string
		var created, retired int64
		var sealed []byte
		if err := rows.Scan(&kid, &created, &retired, &sealed); err != nil {
			return err
		}
		der, err := kr.open(sealed, kid)
		if err != nil {
			return fmt.Errorf("signing key %s: %w (is OAUTH_KEY_ENCRYPTION_KEY right?)", kid, err)
		}
		pk, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return fmt.Errorf("signing key %s: %w", kid, err)
		}
		priv, ok := pk.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("signing key %s is not RSA", kid)
		}
		k := &signingKey{kid: kid, priv: priv, createdAt: time.Unix(created, 0).UTC()}
		if retired != 0 {
			k.retiredAt = time.Unix(retired, 0).UTC()
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	kr.mu.Lock()
	kr.keys = keys
	kr.mu.Unlock()
	return nil
}

// Rotate creates a new signing key and retires the previous ones. Retired
// keys stay published for retiredKeyGrace.
func (kr *KeyRing) Rotate(ctx context.Context) (string, error) {
	if kr.store == nil {
		return "", errors.New("memory key ring cannot rotate")
	}
	priv, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return "", err
	}
	now := kr.now().UTC()
	k, err := newSigningKey(priv, now)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	sealed, err := kr.seal(der, k.kid)
	if err != nil {
		return "", err
	}
	tx, err := kr.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE signing_keys SET retired_at = ? WHERE retired_at = 0`, now.Unix()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO signing_keys(kid, created_at, retired_at, data) VALUES(?, ?, 0, ?)`,
		k.kid, now.Unix(), sealed); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM signing_keys WHERE retired_at != 0 AND retired_at < ?`,
		now.Add(-retiredKeyGrace).Unix()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return k.kid, kr.reload(ctx)
}

// Reload picks up a rotation done by another process (the CLI).
func (kr *KeyRing) Reload(ctx context.Context) error {
	if kr.store == nil {
		return nil
	}
	return kr.reload(ctx)
}

func (kr *KeyRing) active() []*signingKey {
	kr.mu.RLock()
	defer kr.mu.RUnlock()
	var out []*signingKey
	for _, k := range kr.keys {
		if k.retiredAt.IsZero() {
			out = append(out, k)
		}
	}
	return out
}

// signer returns the key that signs now.
func (kr *KeyRing) signer() (*signingKey, error) {
	active := kr.active()
	if len(active) == 0 {
		return nil, errors.New("no active signing key")
	}
	return active[0], nil
}

// published returns every key a relying party may still need.
func (kr *KeyRing) published() []*signingKey {
	kr.mu.RLock()
	defer kr.mu.RUnlock()
	now := kr.now()
	var out []*signingKey
	for _, k := range kr.keys {
		if k.retiredAt.IsZero() || now.Before(k.retiredAt.Add(retiredKeyGrace)) {
			out = append(out, k)
		}
	}
	return out
}

// KeyInfo describes a key for the operator CLI.
type KeyInfo struct {
	KID       string
	CreatedAt time.Time
	RetiredAt time.Time
	Published bool
}

// List describes every stored key.
func (kr *KeyRing) List() []KeyInfo {
	published := map[string]bool{}
	for _, k := range kr.published() {
		published[k.kid] = true
	}
	kr.mu.RLock()
	defer kr.mu.RUnlock()
	out := make([]KeyInfo, 0, len(kr.keys))
	for _, k := range kr.keys {
		out = append(out, KeyInfo{KID: k.kid, CreatedAt: k.createdAt, RetiredAt: k.retiredAt, Published: published[k.kid]})
	}
	return out
}

// JWK is a public key in JSON Web Key form.
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	Kid string `json:"kid,omitempty"`
	// RSA
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`
	// EC / OKP
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	// Private members, recognised only so a pasted private key is refused.
	D string `json:"d,omitempty"`
	P string `json:"p,omitempty"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns the published key set.
func (kr *KeyRing) JWKS() JWKS {
	set := JWKS{Keys: []JWK{}}
	for _, k := range kr.published() {
		pub := k.priv.PublicKey
		set.Keys = append(set.Keys, JWK{
			Kty: "RSA", Use: "sig", Alg: "RS256", Kid: k.kid,
			N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		})
	}
	return set
}

// Sign produces a compact RS256 JWS over claims.
func (kr *KeyRing) Sign(claims any) (string, error) {
	k, err := kr.signer()
	if err != nil {
		return "", err
	}
	return signJWT(k, claims)
}

func signJWT(k *signingKey, claims any) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.kid}
	signingInput, err := jwsSigningInput(header, claims)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// --- Sealing ---------------------------------------------------------------------

func (kr *KeyRing) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(kr.kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts plaintext, binding it to kid so a row cannot be swapped.
func (kr *KeyRing) seal(plaintext []byte, kid string) ([]byte, error) {
	aead, err := kr.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, []byte("poweur-oauth-key\n"+kid)), nil
}

func (kr *KeyRing) open(sealed []byte, kid string) ([]byte, error) {
	aead, err := kr.aead()
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("sealed key too short")
	}
	return aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("poweur-oauth-key\n"+kid))
}

// keyCount is used by the operator CLI and tests.
func (s *Store) keyCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM signing_keys`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
