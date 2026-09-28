// Package drive defines the canonical storage-v2 cryptographic primitives.
// Manifests and authorization are separate: decrypting bytes is not proof that
// their author was entitled to write them.
package drive

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	identity "github.com/poweur/identity"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	MaxPlaintext  = 4 * 1024 * 1024
	PaddingBucket = 4096
	MaxChunkBytes = chacha20poly1305.NonceSizeX + MaxPlaintext + PaddingBucket + chacha20poly1305.Overhead
	MaxCounter    = uint64(9007199254740991)
)

// Context binds an envelope to its drive, node, purpose and key generation.
// Each field is UTF-8 preceded by its uint32 big-endian byte length; generation
// is decimal ASCII. This avoids delimiter ambiguities across Go and JavaScript.
func Context(owner, node, purpose string, generation uint64) ([]byte, error) {
	if generation > MaxCounter {
		return nil, errors.New("invalid generation")
	}
	var out []byte
	for _, field := range []string{owner, node, purpose, strconv.FormatUint(generation, 10)} {
		if len(field) == 0 || len(field) > 1024 || !utf8.ValidString(field) || strings.ContainsRune(field, 0) {
			return nil, errors.New("invalid context field")
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out, nil
}

// SealKey wraps a private node key or content key. Callers use different
// purposes in Context (node-key or content-key), including the target node ID.
func SealKey(publicKey, key, context []byte) (identity.SealedPayload, error) {
	if len(key) != 32 {
		return identity.SealedPayload{}, errors.New("key must be 32 bytes")
	}
	return identity.Seal(publicKey, key, identity.DriveSealDomain, context)
}

func OpenKey(privateKey []byte, payload identity.SealedPayload, context []byte) ([]byte, error) {
	key, err := identity.OpenSeal(privateKey, payload, identity.DriveSealDomain, context)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid sealed key length")
	}
	return key, nil
}

// EncryptChunk produces nonce || XChaCha20-Poly1305(padded length || bytes).
func EncryptChunk(key, plaintext, context []byte) ([]byte, error) {
	return encryptChunk(key, plaintext, context, rand.Reader)
}

func encryptChunk(key, plaintext, context []byte, random io.Reader) ([]byte, error) {
	if len(context) == 0 {
		return nil, errors.New("chunk context required")
	}
	if len(plaintext) > MaxPlaintext {
		return nil, errors.New("chunk plaintext exceeds 4 MiB")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	size := (len(plaintext) + 4 + PaddingBucket - 1) / PaddingBucket * PaddingBucket
	padded := make([]byte, size)
	binary.BigEndian.PutUint32(padded, uint32(len(plaintext)))
	copy(padded[4:], plaintext)
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, padded, chunkAAD(context)), nil
}

// DecryptChunk validates bounds before invoking AEAD, then checks the internal
// length and zero padding. Malformed nonces/ciphertexts never panic.
func DecryptChunk(key, chunk, context []byte) ([]byte, error) {
	if len(context) == 0 {
		return nil, errors.New("chunk context required")
	}
	overhead := chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	if len(chunk) < overhead+PaddingBucket || len(chunk) > MaxChunkBytes || (len(chunk)-overhead)%PaddingBucket != 0 {
		return nil, errors.New("invalid chunk length")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	padded, err := aead.Open(nil, chunk[:aead.NonceSize()], chunk[aead.NonceSize():], chunkAAD(context))
	if err != nil {
		return nil, fmt.Errorf("chunk authentication: %w", err)
	}
	size := uint64(binary.BigEndian.Uint32(padded))
	if size > MaxPlaintext || size+4 > uint64(len(padded)) || (size+4+PaddingBucket-1)/PaddingBucket*PaddingBucket != uint64(len(padded)) {
		return nil, errors.New("invalid padded length")
	}
	for _, b := range padded[4+size:] {
		if b != 0 {
			return nil, errors.New("invalid padding")
		}
	}
	return padded[4 : 4+size], nil
}

func chunkAAD(context []byte) []byte {
	return append([]byte("poweur/drive/chunk/v1\n"), context...)
}

func ChunkID(chunk []byte) string {
	hash := sha256.Sum256(chunk)
	return hex.EncodeToString(hash[:])
}
