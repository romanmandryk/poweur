package drive

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	identity "github.com/poweur/identity"
	"golang.org/x/crypto/curve25519"
	"os"
	"strings"
	"testing"
)

func sampleRecord() AppendRecord {
	return AppendRecord{Format: 1, Drive: "alice.example", Node: strings.Repeat("a", 32), Author: "bob.example", Generation: 1, Sequence: 1, Chunks: []ChunkRef{{strings.Repeat("b", 64), 4136}}}
}

func TestAppendRecord(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{6}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	record := sampleRecord()
	if err := record.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := record.VerifyNext(pub, 0, ""); err != nil {
		t.Fatal(err)
	}
	hash, err := record.Hash()
	if err != nil {
		t.Fatal(err)
	}
	next := record
	next.Sequence = 2
	next.Previous = hash
	if err := next.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := next.VerifyNext(pub, 1, hash); err != nil {
		t.Fatal(err)
	}
	nextHash, _ := next.Hash()
	if err := next.VerifyNext(pub, 2, nextHash); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := record.VerifyNext(pub, 2, nextHash); err == nil {
		t.Fatal("reordered record accepted")
	}
	if err := next.VerifyNext(pub, 0, ""); err == nil {
		t.Fatal("gap accepted")
	}
	if err := next.VerifyNext(pub, 1, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong chain accepted")
	}
	mutations := map[string]func(*AppendRecord){
		"drive":      func(r *AppendRecord) { r.Drive = "carol.example" },
		"node":       func(r *AppendRecord) { r.Node = strings.Repeat("c", 32) },
		"author":     func(r *AppendRecord) { r.Author = "carol.example" },
		"generation": func(r *AppendRecord) { r.Generation++ },
		"chunk":      func(r *AppendRecord) { r.Chunks = []ChunkRef{{strings.Repeat("c", 64), 4136}} },
		"size":       func(r *AppendRecord) { r.Chunks = []ChunkRef{{strings.Repeat("b", 64), 8232}} },
		"signature":  func(r *AppendRecord) { r.Signature = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := record
			mutate(&r)
			if err := r.Verify(pub); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
	if err := record.Verify(pub[:31]); err == nil {
		t.Fatal("invalid public key accepted")
	}
	if err := record.Sign(priv[:32]); err == nil {
		t.Fatal("invalid private key accepted")
	}
}

func TestAppendRecordValidation(t *testing.T) {
	tests := map[string]func(*AppendRecord){
		"format":            func(r *AppendRecord) { r.Format = 2 },
		"uppercase":         func(r *AppendRecord) { r.Author = "BOB.example" },
		"whitespace":        func(r *AppendRecord) { r.Drive = " alice.example" },
		"bad node":          func(r *AppendRecord) { r.Node = "../escape" },
		"generation":        func(r *AppendRecord) { r.Generation = 0 },
		"unsafe sequence":   func(r *AppendRecord) { r.Sequence = MaxCounter + 1 },
		"previous on first": func(r *AppendRecord) { r.Previous = strings.Repeat("a", 64) },
		"missing previous":  func(r *AppendRecord) { r.Sequence = 2 },
		"empty":             func(r *AppendRecord) { r.Chunks = nil },
		"bad size":          func(r *AppendRecord) { r.Chunks = []ChunkRef{{strings.Repeat("b", 64), 4137}} },
		"too many":          func(r *AppendRecord) { r.Chunks = make([]ChunkRef, 1025) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := sampleRecord()
			mutate(&r)
			if _, err := r.Canonical(); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}

func TestVectors_DriveRecords(t *testing.T) {
	type vector struct {
		Record      AppendRecord `json:"record"`
		Private     string       `json:"private_key"`
		Public      string       `json:"public_key"`
		Canonical   string       `json:"canonical"`
		Hash        string       `json:"hash"`
		NodePrivate string       `json:"node_private,omitempty"`
	}
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{6}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	record := sampleRecord()
	enc := base64.RawURLEncoding.EncodeToString
	var vectors []vector
	for i := 0; i < 3; i++ {
		if err := record.Sign(priv); err != nil {
			t.Fatal(err)
		}
		canonical, err := record.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		hash, err := record.Hash()
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{record, enc(priv.Seed()), enc(pub), enc(canonical), hash, ""})
		record.Sequence++
		record.Previous = hash
	}
	nodePrivate := bytes.Repeat([]byte{8}, 32)
	nodePublic, err := curve25519.X25519(nodePrivate, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	// A fixed stream keeps the vector byte-identical across runs.
	if err := record.sealContent(nodePublic, []byte("sealed contribution"), bytes.NewReader(bytes.Repeat([]byte{11}, 64))); err != nil {
		t.Fatal(err)
	}
	if err := record.Sign(priv); err != nil {
		t.Fatal(err)
	}
	canonical, err := record.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := record.Hash()
	if err != nil {
		t.Fatal(err)
	}
	vectors = append(vectors, vector{record, enc(priv.Seed()), enc(pub), enc(canonical), hash, enc(nodePrivate)})
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../testdata/vectors/drive-records.json", append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSealedRecord(t *testing.T) {
	pub, priv, err := identity.GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	record := sampleRecord()
	if err := record.SealContent(pub, []byte("drop box secret")); err != nil {
		t.Fatal(err)
	}
	got, err := record.OpenContent(priv)
	if err != nil || string(got) != "drop box secret" {
		t.Fatalf("open %q %v", got, err)
	}
	changed := record
	changed.Author = "carol.example"
	if _, err := changed.OpenContent(priv); err == nil {
		t.Fatal("author substitution accepted")
	}
	changed = record
	changed.Sequence = 2
	changed.Previous = strings.Repeat("a", 64)
	if _, err := changed.OpenContent(priv); err == nil {
		t.Fatal("sequence substitution accepted")
	}
	changed = record
	changed.Chunks = sampleRecord().Chunks
	if err := changed.Validate(); err == nil {
		t.Fatal("ambiguous chunk/sealed record")
	}
	for _, payload := range []identity.SealedPayload{
		{EphemeralPublicKey: "!", Nonce: record.Sealed.Nonce, Ciphertext: record.Sealed.Ciphertext},
		{EphemeralPublicKey: record.Sealed.EphemeralPublicKey, Nonce: "", Ciphertext: record.Sealed.Ciphertext},
		{EphemeralPublicKey: record.Sealed.EphemeralPublicKey, Nonce: record.Sealed.Nonce, Ciphertext: "AA"},
	} {
		changed = record
		changed.Sealed = &payload
		if err := changed.Validate(); err == nil {
			t.Fatal("bad seal accepted")
		}
	}
	if err := record.SealContent(pub, make([]byte, MaxPlaintext+1)); err == nil {
		t.Fatal("oversized sealed record")
	}
	if _, err := sampleRecord().OpenContent(priv); err == nil {
		t.Fatal("opened chunk record as sealed")
	}
}
