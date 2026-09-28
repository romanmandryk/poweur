package drive

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	identity "github.com/poweur/identity"
)

// ChunkRef names immutable stored bytes, including nonce, padding and AEAD tag.
type ChunkRef struct {
	ID   string `json:"id"`
	Size uint64 `json:"size"`
}

// AppendRecord is an author's signed contribution, before the relay assigns a
// total-order position. It contains either normal chunk refs or one public-key
// sealed payload, never both. Sequence and Previous form a per-author chain.
// Authorization and public-key resolution are responsibilities of the caller.
type AppendRecord struct {
	Format     int                     `json:"format"`
	Drive      string                  `json:"drive"`
	Node       string                  `json:"node"`
	Author     string                  `json:"author"`
	Generation uint64                  `json:"generation"`
	Sequence   uint64                  `json:"sequence"`
	Previous   string                  `json:"previous"`
	Chunks     []ChunkRef              `json:"chunks"`
	Sealed     *identity.SealedPayload `json:"sealed,omitempty"`
	Signature  string                  `json:"signature"`
}

func validHex(value string, size int) bool {
	if len(value) != size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validIdentity(value string) bool {
	return value == strings.TrimSpace(strings.ToLower(value)) && identity.ValidateIdentityName(value) == nil
}

func (r AppendRecord) Validate() error {
	if r.Format != 1 {
		return errors.New("unsupported append format")
	}
	if !validIdentity(r.Drive) || !validIdentity(r.Author) {
		return errors.New("invalid drive or author")
	}
	if !validHex(r.Node, 16) {
		return errors.New("invalid node ID")
	}
	if r.Generation == 0 || r.Generation > MaxCounter || r.Sequence == 0 || r.Sequence > MaxCounter {
		return errors.New("invalid generation or sequence")
	}
	if r.Sequence == 1 && r.Previous != "" || r.Sequence > 1 && !validHex(r.Previous, 32) {
		return errors.New("invalid previous record hash")
	}
	if r.Chunks == nil || len(r.Chunks) > 1024 || (len(r.Chunks) == 0) == (r.Sealed == nil) {
		return errors.New("record needs chunks or a sealed payload, exclusively")
	}
	for _, chunk := range r.Chunks {
		if !validHex(chunk.ID, 32) || chunk.Size < 40+PaddingBucket || chunk.Size > MaxChunkBytes || (chunk.Size-40)%PaddingBucket != 0 {
			return errors.New("invalid chunk reference")
		}
	}
	if r.Sealed != nil {
		if err := validatePayload(*r.Sealed); err != nil {
			return err
		}
	}
	return nil
}

func validatePayload(payload identity.SealedPayload) error {
	for _, field := range []struct {
		value string
		size  int
	}{{payload.EphemeralPublicKey, 32}, {payload.Nonce, 12}, {payload.Ciphertext, 0}} {
		if len(field.value) > base64.RawURLEncoding.EncodedLen(MaxChunkBytes) {
			return errors.New("sealed field too large")
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(field.value)
		if err != nil || base64.RawURLEncoding.EncodeToString(raw) != field.value {
			return errors.New("invalid sealed field encoding")
		}
		if field.size != 0 && len(raw) != field.size || field.size == 0 && (len(raw) < 16 || len(raw) > MaxChunkBytes) {
			return errors.New("invalid sealed field length")
		}
	}
	return nil
}

// Canonical encodes a domain prefix followed by length-prefixed UTF-8 fields:
// format, drive, node, author, generation, sequence, previous, chunk count,
// then each chunk's ID and stored size, then the sealed ephemeral key, nonce
// and ciphertext (three empty fields for chunk records). Numbers are decimal.
func (r AppendRecord) Canonical() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	out := []byte("poweur/drive/record-sign/v1\n")
	fields := []string{"1", r.Drive, r.Node, r.Author, strconv.FormatUint(r.Generation, 10), strconv.FormatUint(r.Sequence, 10), r.Previous, strconv.Itoa(len(r.Chunks))}
	for _, chunk := range r.Chunks {
		fields = append(fields, chunk.ID, strconv.FormatUint(chunk.Size, 10))
	}
	if r.Sealed == nil {
		fields = append(fields, "", "", "")
	} else {
		fields = append(fields, r.Sealed.EphemeralPublicKey, r.Sealed.Nonce, r.Sealed.Ciphertext)
	}
	for _, field := range fields {
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out, nil
}

func (r *AppendRecord) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid signing key length")
	}
	raw, err := r.Canonical()
	if err != nil {
		return err
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw))
	return nil
}

func (r AppendRecord) Verify(publicKey ed25519.PublicKey) error {
	raw, err := r.Canonical()
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(r.Signature)
	if err != nil || base64.RawURLEncoding.EncodeToString(sig) != r.Signature || len(publicKey) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey, raw, sig) {
		return errors.New("append signature verification failed")
	}
	return nil
}

// Hash identifies the signed record for chaining and idempotency. Call Verify
// with the resolved author's key before trusting a hash supplied by a relay.
func (r AppendRecord) Hash() (string, error) {
	raw, err := r.Canonical()
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(r.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(sig) != r.Signature {
		return "", errors.New("invalid signature encoding")
	}
	data := append([]byte("poweur/drive/record-hash/v1\n"), raw...)
	data = append(data, sig...)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyNext checks the expected chain for this (drive,node,author). The caller
// keeps a separate cursor per tuple and must not advance it on an error.
func (r AppendRecord) VerifyNext(publicKey ed25519.PublicKey, lastSequence uint64, lastHash string) error {
	if err := r.Verify(publicKey); err != nil {
		return err
	}
	if lastSequence > MaxCounter || lastSequence == 0 && lastHash != "" || lastSequence > 0 && !validHex(lastHash, 32) {
		return errors.New("invalid author cursor")
	}
	if r.Sequence <= lastSequence {
		return errors.New("duplicate or reordered author sequence")
	}
	if r.Sequence != lastSequence+1 {
		return fmt.Errorf("author sequence gap: have %d, got %d", lastSequence, r.Sequence)
	}
	if r.Previous != lastHash {
		return errors.New("author chain mismatch")
	}
	return nil
}

func (r AppendRecord) sealContext() ([]byte, error) {
	return Context(r.Drive, r.Node, "record:"+r.Author+":"+strconv.FormatUint(r.Sequence, 10)+":"+r.Previous, r.Generation)
}

// SealContent creates a write-only contribution encrypted to the node public
// key. Sign the record after sealing. The author does not need a read key.
func (r *AppendRecord) SealContent(nodePublic, plaintext []byte) error {
	return r.sealContent(nodePublic, plaintext, nil)
}

// sealContent seals with random, or crypto/rand when random is nil.
func (r *AppendRecord) sealContent(nodePublic, plaintext []byte, random io.Reader) error {
	if len(plaintext) > MaxPlaintext {
		return errors.New("sealed record exceeds 4 MiB")
	}
	context, err := r.sealContext()
	if err != nil {
		return err
	}
	var payload identity.SealedPayload
	if random == nil {
		payload, err = identity.Seal(nodePublic, plaintext, identity.DriveRecordDomain, context)
	} else {
		payload, err = identity.SealWithReader(nodePublic, plaintext, identity.DriveRecordDomain, context, random)
	}
	if err != nil {
		return err
	}
	candidate := *r
	candidate.Chunks = []ChunkRef{}
	candidate.Sealed = &payload
	candidate.Signature = ""
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// OpenContent opens a sealed contribution. Verify the signature and author
// role separately before applying its plaintext to application state.
func (r AppendRecord) OpenContent(nodePrivate []byte) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Sealed == nil {
		return nil, errors.New("record contains chunk references")
	}
	context, err := r.sealContext()
	if err != nil {
		return nil, err
	}
	plaintext, err := identity.OpenSeal(nodePrivate, *r.Sealed, identity.DriveRecordDomain, context)
	if err != nil {
		return nil, err
	}
	if len(plaintext) > MaxPlaintext {
		return nil, errors.New("sealed record exceeds 4 MiB")
	}
	return plaintext, nil
}
