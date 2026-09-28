package drive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	identity "github.com/poweur/identity"
	"golang.org/x/crypto/chacha20poly1305"
)

func TestChunk(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	context, err := Context("alice.example", "node1", "content", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, 4092, 4093, MaxPlaintext} {
		plain := bytes.Repeat([]byte{42}, size)
		chunk, err := EncryptChunk(key, plain, context)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecryptChunk(key, chunk, context)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: %v", size, err)
		}
		other, _ := Context("alice.example", "node2", "content", 1)
		if _, err := DecryptChunk(key, chunk, other); err == nil {
			t.Fatal("node substitution accepted")
		}
		if _, err := DecryptChunk(key, chunk[:len(chunk)-1], context); err == nil {
			t.Fatal("truncation accepted")
		}
		chunk[len(chunk)-1] ^= 1
		if _, err := DecryptChunk(key, chunk, context); err == nil {
			t.Fatal("tamper accepted")
		}
	}
	if _, err := EncryptChunk(key, make([]byte, MaxPlaintext+1), context); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := EncryptChunk(key[:31], nil, context); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := EncryptChunk(key, nil, nil); err == nil {
		t.Fatal("missing context accepted")
	}
	if _, err := encryptChunk(key, nil, context, bytes.NewReader(nil)); err == nil {
		t.Fatal("entropy failure ignored")
	}
	// Authenticated but noncanonical payloads are still invalid.
	aead, _ := chacha20poly1305.NewX(key)
	nonce := make([]byte, 24)
	for _, padded := range [][]byte{make([]byte, PaddingBucket*2), append(make([]byte, PaddingBucket-1), 1)} {
		chunk := aead.Seal(append([]byte(nil), nonce...), nonce, padded, chunkAAD(context))
		if _, err := DecryptChunk(key, chunk, context); err == nil {
			t.Fatal("invalid padding accepted")
		}
	}
}

func TestKeysAndContext(t *testing.T) {
	pub, priv, err := identity.GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	context, _ := Context("alice.example", "node1", "node-key", 1)
	key := bytes.Repeat([]byte{5}, 32)
	sealed, err := SealKey(pub, key, context)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenKey(priv, sealed, context)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("open %v", err)
	}
	if _, err := SealKey(pub, key[:31], context); err == nil {
		t.Fatal("short key accepted")
	}
	short, _ := identity.Seal(pub, []byte("short"), identity.DriveSealDomain, context)
	if _, err := OpenKey(priv, short, context); err == nil {
		t.Fatal("short opened key accepted")
	}
	for _, field := range []string{"", "a\x00b", string([]byte{255}), string(make([]byte, 1025))} {
		if _, err := Context(field, "node", "purpose", 0); err == nil {
			t.Fatal("invalid field accepted")
		}
	}
	if _, err := Context("owner", "node", "purpose", MaxCounter+1); err == nil {
		t.Fatal("unsafe counter accepted")
	}
	a, _ := Context("a", "bc", "d", 1)
	b, _ := Context("ab", "c", "d", 1)
	if bytes.Equal(a, b) {
		t.Fatal("ambiguous fields")
	}
}

func TestVectors_DriveChunks(t *testing.T) {
	type vector struct {
		Size    int    `json:"size"`
		Key     string `json:"key"`
		Context string `json:"context"`
		Chunk   string `json:"chunk"`
		Hash    string `json:"hash"`
	}
	key := bytes.Repeat([]byte{1}, 32)
	context, _ := Context("alice.example", "node1", "content", 1)
	enc := base64.RawURLEncoding.EncodeToString
	var vectors []vector
	for _, size := range []int{0, 1, 4092, 4093} {
		chunk, err := encryptChunk(key, bytes.Repeat([]byte{42}, size), context, bytes.NewReader(bytes.Repeat([]byte{2}, 24)))
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{size, enc(key), enc(context), enc(chunk), ChunkID(chunk)})
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../testdata/vectors/drive-chunks.json", append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecryptChunk(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 4136))
	f.Fuzz(func(t *testing.T, chunk []byte) { _, _ = DecryptChunk(make([]byte, 32), chunk, []byte("context")) })
}
