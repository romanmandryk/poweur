package integration_test

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// driveClient talks to one relay's /drive API as one identity, signing a
// fresh challenge for every request like the SDKs do.
type driveClient struct {
	t        *testing.T
	relay    string
	identity string
	key      ed25519.PrivateKey
}

func (c driveClient) do(method, path string, body []byte) (*http.Response, []byte) {
	c.t.Helper()
	resp, err := http.Get(c.relay + "/auth/challenge?identity=" + c.identity)
	if err != nil {
		c.t.Fatal(err)
	}
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&challenge)
	resp.Body.Close()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, c.relay+path, reader)
	req.Header.Set("X-Poweur-Identity", c.identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.key, []byte(challenge.Challenge))))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func (c driveClient) sign(challenge string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.key, []byte(challenge)))
}

func (c driveClient) json(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	resp, out := c.do(method, path, raw)
	var decoded map[string]any
	_ = json.Unmarshal(out, &decoded)
	return resp.StatusCode, decoded
}

// events opens the owner's event stream and yields drive.changed payloads.
func (c driveClient) events() <-chan map[string]any {
	c.t.Helper()
	resp, _ := http.Get(c.relay + "/auth/challenge?identity=" + c.identity)
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&challenge)
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, c.relay+"/events/"+c.identity, nil)
	req.Header.Set("X-Poweur-Identity", c.identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.key, []byte(challenge.Challenge))))
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		c.t.Fatalf("event stream: %v", err)
	}
	c.t.Cleanup(func() { stream.Body.Close() })
	out := make(chan map[string]any, 16)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			var event struct {
				Type  string         `json:"type"`
				Drive map[string]any `json:"drive"`
			}
			if strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(line[6:]), &event) == nil && event.Type == "drive.changed" {
				out <- event.Drive
			}
		}
	}()
	return out
}

func (c driveClient) commit(body map[string]any) (int, map[string]any) {
	c.t.Helper()
	return c.commitTo(c.identity, body)
}

// commitTo commits to another identity's drive, as a member.
func (c driveClient) commitTo(driveID string, body map[string]any) (int, map[string]any) {
	c.t.Helper()
	body["id"] = randomHex(16)
	return c.json(http.MethodPost, "/drive/"+driveID+"/commit", body)
}

func (c driveClient) signed(m drive.Manifest) drive.Manifest {
	c.t.Helper()
	if err := m.Sign(c.key); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// sealedFor seals plaintext to an X25519 drive key, the way node keys, names
// and content keys are wrapped (the relay never opens them).
func sealedFor(t *testing.T, recipient []byte, purpose string, plaintext []byte) *idpkg.SealedPayload {
	t.Helper()
	ctx, err := drive.Context("drivealice.poweur.net", strings.Repeat("0", 32), purpose, 1)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := drive.SealKey(recipient, plaintext, ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &sealed
}

func sealedName(t *testing.T, parent []byte, name string) *idpkg.SealedPayload {
	t.Helper()
	ctx, _ := drive.Context("drivealice.poweur.net", strings.Repeat("0", 32), drive.PurposeName, 1)
	sealed, err := drive.SealName(parent, name, ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &sealed
}

func waitDriveEvent(t *testing.T, events <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("no drive.changed event")
		return nil
	}
}

// INT_DRIVE_01: an owner uploads, resumes, commits, reads back, conflicts,
// appends and follows live events on a real relay; a visitor homed on another
// relay authenticates but may not touch the drive; a fresh relay process over
// the same data serves the same drive; nothing on disk holds plaintext.
func TestINT_DRIVE_01_OwnerFlowAcrossRelays(t *testing.T) {
	zone := newZone(t)
	dataA := t.TempDir()
	tsA, addrA := newHostedRelay(t, zone, dataA)
	addrB, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	zone.SetHost("drivealice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "drivealice.poweur.net", "--hosted", "--relay", tsA.URL, "--json")
	alice := driveClient{t: t, relay: tsA.URL, identity: "drivealice.poweur.net", key: loadIdentityKey(t, aliceHome, "drivealice.poweur.net")}
	bobHome, bobName := newDNSIdentity(t, "drivebob", "visitors.test", addrB)
	bob := driveClient{t: t, relay: tsA.URL, identity: bobName, key: loadIdentityKey(t, bobHome, bobName)}
	alicePub, _, err := idpkg.GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	base := "/drive/" + alice.identity
	events := alice.events()

	const secretName, secretBody = "tax-return-2026.pdf", "the secret body nobody else may read"
	root := randomHex(16)
	if status, out := alice.commit(map[string]any{"manifest": alice.signed(drive.Manifest{Format: 1, Drive: alice.identity, Node: root, Version: randomHex(16),
		Operation: drive.OpCreate, Author: alice.identity, Generation: 1, Kind: drive.KindFolder,
		NodeKey: sealedFor(t, alicePub, "node-key", bytes.Repeat([]byte{1}, 32)), Pages: []string{}})}); status != http.StatusOK {
		t.Fatalf("create root: %d %v", status, out)
	}
	waitDriveEvent(t, events)

	// Two chunks; the first upload "fails" after one chunk and the client
	// resumes by asking which are still missing.
	contentKey := bytes.Repeat([]byte{9}, 32)
	file := randomHex(16)
	var refs []drive.ChunkRef
	var blobs [][]byte
	for _, part := range []string{secretBody, strings.Repeat("x", 5000)} {
		ctx, _ := drive.Context(alice.identity, file, drive.PurposeContent, 1)
		data, err := drive.EncryptChunk(contentKey, []byte(part), ctx)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))})
		blobs = append(blobs, data)
	}
	if resp, raw := alice.do(http.MethodPut, base+"/chunks/"+refs[0].ID, blobs[0]); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", resp.StatusCode, raw)
	}
	status, out := alice.json(http.MethodPost, base+"/chunks/missing", map[string]any{"chunks": refs})
	missing, _ := out["missing"].([]any)
	if status != http.StatusOK || len(missing) != 1 || missing[0].(map[string]any)["id"] != refs[1].ID {
		t.Fatalf("resume: %d %v", status, out)
	}
	upload := missing[0].(map[string]any)["upload"].(map[string]any)
	if resp, raw := alice.do(http.MethodPut, upload["url"].(string), blobs[1]); resp.StatusCode != http.StatusOK {
		t.Fatalf("resumed upload: %d %s", resp.StatusCode, raw)
	}

	name := sealedName(t, alicePub, secretName)
	pages, hashes, _ := drive.SplitPages(alice.identity, file, refs)
	v1 := randomHex(16)
	status, out = alice.commit(map[string]any{"pages": pages, "manifest": alice.signed(drive.Manifest{Format: 1, Drive: alice.identity, Node: file, Version: v1,
		Operation: drive.OpCreate, Author: alice.identity, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: root,
		Name: name, NameHash: randomHex(32), NodeKey: sealedFor(t, alicePub, "node-key", bytes.Repeat([]byte{2}, 32)),
		ContentKey: sealedFor(t, alicePub, "content-key", contentKey), Count: 2, Pages: hashes})})
	if status != http.StatusOK {
		t.Fatalf("commit file: %d %v", status, out)
	}
	if event := waitDriveEvent(t, events); event["node"] != file || event["version"] != v1 {
		t.Fatalf("create event: %v", event)
	}

	// Read back and decrypt, as a second device of Alice's would.
	resp, raw := alice.do(http.MethodGet, base+"/nodes/"+file+"/versions/"+v1+"/chunks/"+refs[0].ID, nil)
	ctx, _ := drive.Context(alice.identity, file, drive.PurposeContent, 1)
	if plain, err := drive.DecryptChunk(contentKey, raw, ctx); resp.StatusCode != http.StatusOK || err != nil || string(plain) != secretBody {
		t.Fatalf("read back: %d %v %q", resp.StatusCode, err, plain)
	}

	// Two devices edit from the same base: one wins, the other gets 409 + head.
	edit := func() (int, map[string]any) {
		return alice.commit(map[string]any{"manifest": alice.signed(drive.Manifest{Format: 1, Drive: alice.identity, Node: file, Version: randomHex(16),
			Parent: v1, Operation: drive.OpReplace, Author: alice.identity, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace,
			Count: 2, Pages: hashes})})
	}
	status, won := edit()
	if status != http.StatusOK {
		t.Fatalf("first edit: %d %v", status, won)
	}
	waitDriveEvent(t, events)
	if status, lost := edit(); status != http.StatusConflict || lost["head"] != won["head"] {
		t.Fatalf("second edit: %d %v", status, lost)
	}

	// An append file: positions are assigned in order and pushed inline.
	log := randomHex(16)
	if status, out := alice.commit(map[string]any{"manifest": alice.signed(drive.Manifest{Format: 1, Drive: alice.identity, Node: log, Version: randomHex(16),
		Operation: drive.OpCreate, Author: alice.identity, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeAppend, Folder: root,
		Name: sealedName(t, alicePub, "chat.jsonl"), NameHash: randomHex(32),
		NodeKey: sealedFor(t, alicePub, "node-key", bytes.Repeat([]byte{3}, 32)), ContentKey: sealedFor(t, alicePub, "content-key", contentKey), Pages: []string{}})}); status != http.StatusOK {
		t.Fatalf("create log: %d %v", status, out)
	}
	waitDriveEvent(t, events)
	previous := ""
	for seq := uint64(1); seq <= 3; seq++ {
		record := drive.AppendRecord{Format: 1, Drive: alice.identity, Node: log, Author: alice.identity, Generation: 1, Sequence: seq, Previous: previous,
			Chunks: []drive.ChunkRef{}}
		if err := record.SealContent(alicePub, []byte("a chat line")); err != nil {
			t.Fatal(err)
		}
		if err := record.Sign(alice.key); err != nil {
			t.Fatal(err)
		}
		previous, _ = record.Hash()
		status, out := alice.commit(map[string]any{"records": []drive.AppendRecord{record}})
		if positions, _ := out["positions"].([]any); status != http.StatusOK || len(positions) != 1 || positions[0] != float64(seq) {
			t.Fatalf("append %d: %d %v", seq, status, out)
		}
		event := waitDriveEvent(t, events)
		if records, _ := event["records"].([]any); len(records) != 1 || event["position"] != float64(seq) {
			t.Fatalf("append event %d: %v", seq, event)
		}
	}
	status, out = alice.json(http.MethodGet, base+"/nodes/"+log+"/records?from=2", nil)
	if records, _ := out["records"].([]any); status != http.StatusOK || len(records) != 2 {
		t.Fatalf("tail: %d %v", status, out)
	}

	// Bob is homed on another relay: relay A verifies who he is, then refuses.
	if status, out := bob.json(http.MethodGet, base+"/nodes/"+file, nil); status != http.StatusForbidden {
		t.Fatalf("visitor: %d %v", status, out)
	}

	// A new relay process over the same data: identical drive, no warm cache.
	tsA2, _ := newHostedRelay(t, zone, dataA)
	alice2 := alice
	alice2.relay = tsA2.URL
	_, before := alice.json(http.MethodGet, base+"/changes", nil)
	_, after := alice2.json(http.MethodGet, base+"/changes", nil)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatalf("restart changed the drive:\n%s\n%s", beforeRaw, afterRaw)
	}
	_, usage := alice.json(http.MethodGet, base, nil)
	_, usage2 := alice2.json(http.MethodGet, base, nil)
	if usage["used"] != usage2["used"] || usage["root"] != usage2["root"] {
		t.Fatalf("restart usage: %v vs %v", usage, usage2)
	}

	// Privacy: the relay's data holds neither the file name nor its body.
	_ = filepath.WalkDir(filepath.Join(dataA, "drives"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(path)
		if bytes.Contains(raw, []byte(secretName)) || bytes.Contains(raw, []byte(secretBody)) {
			t.Errorf("plaintext on relay disk: %s", path)
		}
		return nil
	})
}
