package drive

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	identity "github.com/poweur/identity"
	"golang.org/x/crypto/curve25519"
)

func fixedPayload(tag byte) *identity.SealedPayload {
	return &identity.SealedPayload{
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 32)),
		Nonce:              base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag + 1}, 12)),
		Ciphertext:         base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag + 2}, 48)),
	}
}

func refs(n int) []ChunkRef {
	out := make([]ChunkRef, n)
	for i := range out {
		sum := fmt.Sprintf("%064x", i+1)
		out[i] = ChunkRef{ID: sum, Size: 40 + PaddingBucket*uint64(1+i%3)}
	}
	return out
}

const (
	mDrive  = "alice.example"
	mNode   = "0123456789abcdef0123456789abcdef"
	mFolder = "fedcba9876543210fedcba9876543210"
	mV1     = "11111111111111111111111111111111"
	mV2     = "22222222222222222222222222222222"
)

func fileCreate(t *testing.T, chunks []ChunkRef) (Manifest, []ChunkPage) {
	t.Helper()
	pages, hashes, err := SplitPages(mDrive, mNode, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if hashes == nil {
		hashes = []string{}
	}
	return Manifest{
		Format: 1, Drive: mDrive, Node: mNode, Version: mV1, Operation: OpCreate, Author: mDrive,
		Generation: 1, Kind: KindFile, Mode: ModeReplace, Folder: mFolder,
		Name: fixedPayload(1), NameHash: strings.Repeat("a", 64), NodeKey: fixedPayload(4), ContentKey: fixedPayload(7),
		Count: uint64(len(chunks)), Pages: hashes,
	}, pages
}

func TestManifestSignVerifyAndPages(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	m, pages := fileCreate(t, refs(2500))
	if len(pages) != 3 || len(pages[0].Chunks) != PageSize || len(pages[2].Chunks) != 452 {
		t.Fatalf("pages split %d/%d", len(pages), len(pages[2].Chunks))
	}
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(pub); err != nil {
		t.Fatal(err)
	}
	got, err := m.VerifyPages(pages)
	if err != nil || len(got) != 2500 || got[1024] != refs(2500)[1024] {
		t.Fatalf("pages: %d %v", len(got), err)
	}
	// Reordered pages, a page from another node and a dropped page all fail.
	if _, err := m.VerifyPages([]ChunkPage{pages[1], pages[0], pages[2]}); err == nil {
		t.Fatal("reordered pages accepted")
	}
	other := pages[0]
	other.Node = mFolder
	if _, err := m.VerifyPages([]ChunkPage{other, pages[1], pages[2]}); err == nil {
		t.Fatal("page from another node accepted")
	}
	if _, err := m.VerifyPages(pages[:2]); err == nil {
		t.Fatal("dropped page accepted")
	}
	mutations := map[string]func(*Manifest){
		"drive":      func(m *Manifest) { m.Drive = "carol.example" },
		"node":       func(m *Manifest) { m.Node = strings.Repeat("c", 32) },
		"version":    func(m *Manifest) { m.Version = mV2 },
		"author":     func(m *Manifest) { m.Author = "carol.example" },
		"generation": func(m *Manifest) { m.Generation = 2 },
		"mode":       func(m *Manifest) { m.Mode = ModeAppend },
		"folder":     func(m *Manifest) { m.Folder = strings.Repeat("d", 32) },
		"name":       func(m *Manifest) { m.Name = fixedPayload(20) },
		"name hash":  func(m *Manifest) { m.NameHash = strings.Repeat("b", 64) },
		"node key":   func(m *Manifest) { m.NodeKey = fixedPayload(30) },
		"content":    func(m *Manifest) { m.ContentKey = fixedPayload(40) },
		"page order": func(m *Manifest) { m.Pages[0], m.Pages[1] = m.Pages[1], m.Pages[0] },
		"count":      func(m *Manifest) { m.Count-- },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c := m
			c.Pages = append([]string(nil), m.Pages...)
			mutate(&c)
			if err := c.Verify(pub); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
	if err := m.Verify(pub[:31]); err == nil {
		t.Fatal("short public key accepted")
	}
}

func TestManifestOperationShapes(t *testing.T) {
	base, _ := fileCreate(t, refs(3))
	valid := map[string]func() Manifest{
		"file create": func() Manifest { return base },
		"empty file": func() Manifest {
			m := base
			m.Count, m.Pages = 0, []string{}
			return m
		},
		"folder create": func() Manifest {
			m := base
			m.Kind, m.Mode, m.ContentKey, m.Count, m.Pages = KindFolder, "", nil, 0, []string{}
			return m
		},
		"root create": func() Manifest {
			m := base
			m.Kind, m.Mode, m.ContentKey, m.Count, m.Pages = KindFolder, "", nil, 0, []string{}
			m.Folder, m.Name, m.NameHash = "", nil, ""
			return m
		},
		"replace": func() Manifest {
			m := base
			m.Operation, m.Parent, m.Version, m.Folder, m.Name, m.NameHash, m.NodeKey, m.ContentKey = OpReplace, mV1, mV2, "", nil, "", nil, nil
			return m
		},
		"move": func() Manifest {
			m := base
			m.Operation, m.Parent, m.Version, m.ContentKey = OpMove, mV1, mV2, nil
			return m
		},
		"rotate": func() Manifest {
			m := base
			m.Operation, m.Parent, m.Version, m.Folder, m.Name, m.NameHash, m.Generation = OpRotate, mV1, mV2, "", nil, "", 2
			return m
		},
		"remove": func() Manifest {
			m := base
			m.Operation, m.Parent, m.Version, m.Folder, m.Name, m.NameHash, m.NodeKey, m.ContentKey, m.Count, m.Pages = OpRemove, mV1, mV2, "", nil, "", nil, nil, 0, []string{}
			return m
		},
	}
	for name, build := range valid {
		if err := build().Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	invalid := map[string]func(*Manifest){
		"create with parent":       func(m *Manifest) { m.Parent = mV2 },
		"create without node key":  func(m *Manifest) { m.NodeKey = nil },
		"file without content key": func(m *Manifest) { m.ContentKey = nil },
		"name without hash":        func(m *Manifest) { m.NameHash = "" },
		"non-root without name":    func(m *Manifest) { m.Name, m.NameHash = nil, "" },
		"folder with content":      func(m *Manifest) { m.Kind, m.Mode, m.ContentKey = KindFolder, "", nil },
		"file without mode":        func(m *Manifest) { m.Mode = "" },
		"replace renames":          func(m *Manifest) { m.Operation, m.Parent, m.Version = OpReplace, mV1, mV2 },
		"remove keeps content": func(m *Manifest) {
			m.Operation, m.Parent, m.Version, m.Folder, m.Name, m.NameHash, m.NodeKey, m.ContentKey = OpRemove, mV1, mV2, "", nil, "", nil, nil
		},
		"unknown operation":  func(m *Manifest) { m.Operation = "append" },
		"count beyond pages": func(m *Manifest) { m.Count = PageSize + 1 },
		"missing page list":  func(m *Manifest) { m.Pages = nil },
		"folder is itself":   func(m *Manifest) { m.Folder = mNode },
		"parent is itself":   func(m *Manifest) { m.Operation, m.Parent = OpMove, mV1 },
		"uppercase node":     func(m *Manifest) { m.Node = strings.ToUpper(mNode) },
		"author with space":  func(m *Manifest) { m.Author = " alice.example" },
		"generation zero":    func(m *Manifest) { m.Generation = 0 },
		"short name hash":    func(m *Manifest) { m.NameHash = "abc" },
		"bad envelope": func(m *Manifest) {
			m.NodeKey = &identity.SealedPayload{EphemeralPublicKey: "x", Nonce: "y", Ciphertext: "z"}
		},
		"unsafe generation":         func(m *Manifest) { m.Generation = MaxCounter + 1 },
		"unsupported format":        func(m *Manifest) { m.Format = 2 },
		"page hash not hex":         func(m *Manifest) { m.Pages[0] = strings.Repeat("Z", 64) },
		"create names root of self": func(m *Manifest) { m.Folder = "" },
	}
	for name, mutate := range invalid {
		m := base
		m.Pages = append([]string(nil), base.Pages...)
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A create by a writer who holds only the folder's public key: the name is
// sealed to the folder, indexed under a random token, and the node key is
// sealed to the folder too. The owner opens both with the folder's private key.
func TestManifestSealedCreate(t *testing.T) {
	folderPriv := bytes.Repeat([]byte{3}, 32)
	folderPub, _ := curve25519.X25519(folderPriv, curve25519.Basepoint)
	nodePub, nodePriv, err := identity.GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := fileCreate(t, refs(1))
	token, err := NewNameToken()
	if err != nil {
		t.Fatal(err)
	}
	nameCtx, _ := m.EnvelopeContext(PurposeName)
	keyCtx, _ := m.EnvelopeContext(PurposeNodeKey)
	name, err := SealName(folderPub, "upload.pdf", nameCtx)
	if err != nil {
		t.Fatal(err)
	}
	nodeKey, err := SealKey(folderPub, nodePriv, keyCtx)
	if err != nil {
		t.Fatal(err)
	}
	contentCtx, _ := m.EnvelopeContext(PurposeContentKey)
	contentKey, err := SealKey(nodePub, bytes.Repeat([]byte{5}, 32), contentCtx)
	if err != nil {
		t.Fatal(err)
	}
	m.Name, m.NameHash, m.NodeKey, m.ContentKey = &name, token, &nodeKey, &contentKey
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenName(folderPriv, *m.Name, nameCtx); err != nil || got != "upload.pdf" {
		t.Fatalf("owner cannot read the name: %q %v", got, err)
	}
	if got, err := OpenKey(folderPriv, *m.NodeKey, keyCtx); err != nil || !bytes.Equal(got, nodePriv) {
		t.Fatalf("owner cannot open the node key: %v", err)
	}
	// An envelope moved onto another node does not open.
	other := m
	other.Node = strings.Repeat("e", 32)
	otherCtx, _ := other.EnvelopeContext(PurposeNodeKey)
	if _, err := OpenKey(folderPriv, *m.NodeKey, otherCtx); err == nil {
		t.Fatal("node key opened under another node's context")
	}
}

// TestVectors_DriveManifests pins page and manifest encodings for the
// TypeScript twin: canonical bytes, page hashes, signature and manifest hash.
func TestVectors_DriveManifests(t *testing.T) {
	type pageVector struct {
		Page      ChunkPage `json:"page"`
		Canonical string    `json:"canonical"`
		Hash      string    `json:"hash"`
	}
	type manifestVector struct {
		Name      string      `json:"name"`
		Manifest  Manifest    `json:"manifest"`
		Pages     []ChunkPage `json:"pages"`
		Seed      string      `json:"seed"`
		PublicKey string      `json:"public_key"`
		Canonical string      `json:"canonical"`
		Hash      string      `json:"hash"`
	}
	enc := base64.RawURLEncoding.EncodeToString
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	var out struct {
		Pages     []pageVector     `json:"pages"`
		Manifests []manifestVector `json:"manifests"`
	}
	pages, hashes, err := SplitPages(mDrive, mNode, refs(1030))
	if err != nil {
		t.Fatal(err)
	}
	for i, page := range pages {
		raw, _ := page.Canonical()
		out.Pages = append(out.Pages, pageVector{page, hex.EncodeToString(raw), hashes[i]})
	}
	create, _ := fileCreate(t, refs(1030))
	replace := create
	replace.Operation, replace.Parent, replace.Version, replace.Folder, replace.Name, replace.NameHash, replace.NodeKey, replace.ContentKey = OpReplace, mV1, mV2, "", nil, "", nil, nil
	root := create
	root.Kind, root.Mode, root.ContentKey, root.Count, root.Pages, root.Folder, root.Name, root.NameHash = KindFolder, "", nil, 0, []string{}, "", nil, ""
	for _, c := range []struct {
		name string
		m    Manifest
		p    []ChunkPage
	}{{"file-create", create, pages}, {"replace", replace, pages}, {"root-create", root, nil}} {
		m := c.m
		if err := m.Sign(priv); err != nil {
			t.Fatal(err)
		}
		raw, _ := m.Canonical()
		hash, err := m.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.VerifyPages(c.p); err != nil {
			t.Fatal(err)
		}
		out.Manifests = append(out.Manifests, manifestVector{c.name, m, c.p, enc(priv.Seed()), enc(pub), hex.EncodeToString(raw), hash})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../testdata/vectors/drive-manifests.json", append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
