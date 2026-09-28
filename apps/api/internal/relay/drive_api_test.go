package relay

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	drivepkg "github.com/poweur/api/internal/drive"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

func driveReq(t *testing.T, ts *httptest.Server, method string, as hostedID, path string, body any) (int, map[string]any, *http.Response) {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		raw, _ = json.Marshal(b)
	}
	resp := httpReq(t, ts, method, path, "", raw, ownerAuth(t, ts, as))
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp
}

func randHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func testSealed(tag byte) *idpkg.SealedPayload {
	return &idpkg.SealedPayload{
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 32)),
		Nonce:              base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 12)),
		Ciphertext:         base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 48)),
	}
}

func testChunk(t *testing.T, owner, text string) []byte {
	t.Helper()
	ctx, _ := drive.Context(owner, strings.Repeat("0", 32), drive.PurposeContent, 1)
	data, err := drive.EncryptChunk(bytes.Repeat([]byte{7}, 32), []byte(text), ctx)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// sseEvents reads `drive.changed` events from the owner's event stream.
func sseEvents(t *testing.T, ts *httptest.Server, as hostedID) <-chan streamEvent {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/events/"+as.name, nil)
	for k, v := range ownerAuth(t, ts, as) {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("event stream: %v %v", err, resp)
	}
	t.Cleanup(func() { resp.Body.Close() })
	out := make(chan streamEvent, 16)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event streamEvent
			if json.Unmarshal([]byte(line[6:]), &event) == nil && event.Type == "drive.changed" {
				out <- event
			}
		}
	}()
	return out
}

func nextDriveEvent(t *testing.T, events <-chan streamEvent) *driveEvent {
	t.Helper()
	select {
	case event := <-events:
		return event.Drive
	case <-time.After(5 * time.Second):
		t.Fatal("no drive.changed event")
		return nil
	}
}

func TestDriveAPIOwnerFlow(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "drivealice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "drivebob.poweur.net")
	base := "/drive/" + alice.name
	events := sseEvents(t, ts, alice)

	commit := func(m drive.Manifest, pages []drive.ChunkPage) (int, map[string]any) {
		t.Helper()
		if err := m.Sign(alice.priv); err != nil {
			t.Fatal(err)
		}
		status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "manifest": m, "pages": pages})
		return status, out
	}

	root := randHex(16)
	if status, out := commit(drive.Manifest{Format: 1, Drive: alice.name, Node: root, Version: randHex(16), Operation: drive.OpCreate,
		Author: alice.name, Generation: 1, Kind: drive.KindFolder, NodeKey: testSealed(1), Pages: []string{}}, nil); status != http.StatusOK {
		t.Fatalf("create root: %d %v", status, out)
	}
	if event := nextDriveEvent(t, events); event.Node != root || event.Operation != drive.OpCreate {
		t.Fatalf("root event: %+v", event)
	}
	status, info, _ := driveReq(t, ts, http.MethodGet, alice, base, nil)
	if status != http.StatusOK || info["root"] != root {
		t.Fatalf("drive info: %d %v", status, info)
	}

	// Upload: ask what is missing, then PUT through the relay (fs store).
	data := testChunk(t, alice.name, "hello drive")
	ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))}
	status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/chunks/missing", map[string]any{"chunks": []drive.ChunkRef{ref}})
	missing, _ := out["missing"].([]any)
	if status != http.StatusOK || len(missing) != 1 {
		t.Fatalf("missing: %d %v", status, out)
	}
	upload := missing[0].(map[string]any)["upload"].(map[string]any)
	if upload["url"] != base+"/chunks/"+ref.ID {
		t.Fatalf("upload target: %v", upload)
	}
	if status, out, _ := driveReq(t, ts, http.MethodPut, alice, base+"/chunks/"+strings.Repeat("0", 64), data); status != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched hash: %d %v", status, out)
	}
	if status, out, _ := driveReq(t, ts, http.MethodPut, alice, base+"/chunks/"+ref.ID, data); status != http.StatusOK {
		t.Fatalf("upload: %d %v", status, out)
	}

	file := randHex(16)
	pages, hashes, _ := drive.SplitPages(alice.name, file, []drive.ChunkRef{ref})
	v1 := randHex(16)
	if status, out := commit(drive.Manifest{Format: 1, Drive: alice.name, Node: file, Version: v1, Operation: drive.OpCreate, Author: alice.name,
		Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: root, Name: testSealed(2), NameHash: strings.Repeat("ab", 32),
		NodeKey: testSealed(3), ContentKey: testSealed(4), Count: 1, Pages: hashes}, pages); status != http.StatusOK {
		t.Fatalf("create file: %d %v", status, out)
	}
	nextDriveEvent(t, events)

	// Read it back the way a client would: children → node → version → page → chunk.
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+root+"/children", nil)
	if children, _ := out["children"].([]any); status != http.StatusOK || len(children) != 1 {
		t.Fatalf("children: %d %v", status, out)
	}
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+file+"/versions/"+v1, nil)
	if status != http.StatusOK || out["version"] != v1 {
		t.Fatalf("version: %d %v", status, out)
	}
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+file+"/versions/"+v1+"/pages/"+hashes[0], nil)
	if status != http.StatusOK {
		t.Fatalf("page: %d %v", status, out)
	}
	resp := httpReq(t, ts, http.MethodGet, base+"/nodes/"+file+"/versions/"+v1+"/chunks/"+ref.ID, "", nil, ownerAuth(t, ts, alice))
	got := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || got != string(data) || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("chunk: %d len %d", resp.StatusCode, len(got))
	}

	// A stale base is a 409 that names the winning head.
	replace := func(parent string) (int, map[string]any) {
		return commit(drive.Manifest{Format: 1, Drive: alice.name, Node: file, Version: randHex(16), Parent: parent, Operation: drive.OpReplace,
			Author: alice.name, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Count: 1, Pages: hashes}, nil)
	}
	status, out = replace(v1)
	if status != http.StatusOK {
		t.Fatalf("replace: %d %v", status, out)
	}
	head := out["head"]
	nextDriveEvent(t, events)
	if status, out = replace(v1); status != http.StatusConflict || out["head"] != head {
		t.Fatalf("stale replace: %d %v", status, out)
	}
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+file+"/history", nil)
	if versions, _ := out["versions"].([]any); status != http.StatusOK || len(versions) != 2 {
		t.Fatalf("history: %d %v", status, out)
	}
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/changes?cursor=2", nil)
	if changes, _ := out["changes"].([]any); status != http.StatusOK || len(changes) != 2 || out["cursor"] != "4" {
		t.Fatalf("changes: %d %v", status, out)
	}

	// Appends: positions come back on commit and ride the event inline.
	log := randHex(16)
	if status, out := commit(drive.Manifest{Format: 1, Drive: alice.name, Node: log, Version: randHex(16), Operation: drive.OpCreate, Author: alice.name,
		Generation: 1, Kind: drive.KindFile, Mode: drive.ModeAppend, Folder: root, Name: testSealed(5), NameHash: strings.Repeat("cd", 32),
		NodeKey: testSealed(6), ContentKey: testSealed(7), Pages: []string{}}, nil); status != http.StatusOK {
		t.Fatalf("create log: %d %v", status, out)
	}
	nextDriveEvent(t, events)
	record := drive.AppendRecord{Format: 1, Drive: alice.name, Node: log, Author: alice.name, Generation: 1, Sequence: 1, Chunks: []drive.ChunkRef{ref}}
	if err := record.Sign(alice.priv); err != nil {
		t.Fatal(err)
	}
	status, out, _ = driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "records": []drive.AppendRecord{record}})
	if status != http.StatusOK || fmt.Sprint(out["positions"]) != "[1]" {
		t.Fatalf("append: %d %v", status, out)
	}
	if event := nextDriveEvent(t, events); event.Operation != "append" || len(event.Records) != 1 || event.Records[0].Position != 1 {
		t.Fatalf("append event: %+v", event)
	}
	status, out, _ = driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+log+"/records?from=1", nil)
	if records, _ := out["records"].([]any); status != http.StatusOK || len(records) != 1 || out["next"] != float64(2) {
		t.Fatalf("records: %d %v", status, out)
	}
	resp = httpReq(t, ts, http.MethodGet, base+"/nodes/"+log+"/chunks/"+ref.ID, "", nil, ownerAuth(t, ts, alice))
	if readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("record chunk: %d", resp.StatusCode)
	}

	// Nobody else reads or writes; a caller cannot commit someone else's manifest.
	if status, _, _ := driveReq(t, ts, http.MethodGet, bob, base+"/nodes/"+file, nil); status != http.StatusForbidden {
		t.Fatalf("visitor read: %d", status)
	}
	if resp := httpReq(t, ts, http.MethodGet, base+"/nodes/"+file, "", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d", resp.StatusCode)
	}
	bobManifest := drive.Manifest{Format: 1, Drive: alice.name, Node: randHex(16), Version: randHex(16), Operation: drive.OpCreate, Author: bob.name,
		Generation: 1, Kind: drive.KindFolder, Folder: root, Name: testSealed(8), NameHash: strings.Repeat("ef", 32), NodeKey: testSealed(9), Pages: []string{}}
	_ = bobManifest.Sign(bob.priv)
	if status, _, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "manifest": bobManifest}); status != http.StatusForbidden {
		t.Fatalf("replayed foreign manifest: %d", status)
	}
	// A chunk is not readable by hash through an unrelated node.
	if status, _, _ := driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+root+"/chunks/"+ref.ID, nil); status != http.StatusNotFound {
		t.Fatalf("chunk through unrelated node: %d", status)
	}
}

func TestDriveAPIUnavailableWithoutStore(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "nodrive.poweur.net")
	server.engine = nil
	if status, _, _ := driveReq(t, ts, http.MethodGet, alice, "/drive/"+alice.name, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("no store: %d", status)
	}
}

// With presigning on, `chunks/missing` hands out a direct upload to the
// bucket bound to the chunk's hash, and the commit accepts the result.
// Runs against an existing bucket (POWEUR_TEST_S3_ENDPOINT/BUCKET/…).
func TestDriveAPIPresignedUpload(t *testing.T) {
	endpoint, bucket := os.Getenv("POWEUR_TEST_S3_ENDPOINT"), os.Getenv("POWEUR_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" || os.Getenv("POWEUR_TEST_S3_PRESIGN") == "0" {
		t.Skip("set POWEUR_TEST_S3_ENDPOINT and POWEUR_TEST_S3_BUCKET on a store that verifies presigned checksums")
	}
	region := os.Getenv("POWEUR_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	server, ts := newTestRelay(t)
	store, err := drivepkg.Open(config.Config{StorageProvider: config.StorageS3, S3Endpoint: endpoint, S3Bucket: bucket, S3Region: region,
		S3Prefix: "relay-test/" + randHex(8), S3AccessKey: os.Getenv("POWEUR_TEST_S3_ACCESS_KEY"), S3SecretKey: os.Getenv("POWEUR_TEST_S3_SECRET_KEY"),
		S3Secure: os.Getenv("POWEUR_TEST_S3_SECURE") != "0", S3Presign: true})
	if err != nil {
		t.Fatal(err)
	}
	server.drive, server.engine = store, newDriveEngine(store, server)
	alice := registerTestIdentity(t, server, ts, "presign.poweur.net")
	base := "/drive/" + alice.name

	root := randHex(16)
	m := drive.Manifest{Format: 1, Drive: alice.name, Node: root, Version: randHex(16), Operation: drive.OpCreate, Author: alice.name,
		Generation: 1, Kind: drive.KindFolder, NodeKey: testSealed(1), Pages: []string{}}
	_ = m.Sign(alice.priv)
	if status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "manifest": m}); status != http.StatusOK {
		t.Fatalf("root: %d %v", status, out)
	}
	data := testChunk(t, alice.name, "direct to the bucket")
	ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))}
	status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/chunks/missing", map[string]any{"chunks": []drive.ChunkRef{ref}})
	missing, _ := out["missing"].([]any)
	if status != http.StatusOK || len(missing) != 1 {
		t.Fatalf("missing: %d %v", status, out)
	}
	upload := missing[0].(map[string]any)["upload"].(map[string]any)
	put := func(body []byte) int {
		req, _ := http.NewRequest(http.MethodPut, upload["url"].(string), bytes.NewReader(body))
		for k, v := range upload["headers"].(map[string]any) {
			req.Header.Set(k, v.(string))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-1] ^= 1
	if status := put(tampered); status < 400 {
		t.Fatalf("tampered presigned upload accepted: %d", status)
	}
	if status := put(data); status != http.StatusOK {
		t.Fatalf("presigned upload: %d", status)
	}
	file := randHex(16)
	pages, hashes, _ := drive.SplitPages(alice.name, file, []drive.ChunkRef{ref})
	m = drive.Manifest{Format: 1, Drive: alice.name, Node: file, Version: randHex(16), Operation: drive.OpCreate, Author: alice.name,
		Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: root, Name: testSealed(2), NameHash: strings.Repeat("ab", 32),
		NodeKey: testSealed(3), ContentKey: testSealed(4), Count: 1, Pages: hashes}
	_ = m.Sign(alice.priv)
	if status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "manifest": m, "pages": pages}); status != http.StatusOK {
		t.Fatalf("commit: %d %v", status, out)
	}
}

// An S3-only relay (no POWEUR_DATA) keeps its identities, system files and
// spool in the bucket: a new process over the same prefix serves them.
func TestRelayRestartsFromBucketOnly(t *testing.T) {
	endpoint, bucket := os.Getenv("POWEUR_TEST_S3_ENDPOINT"), os.Getenv("POWEUR_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("set POWEUR_TEST_S3_ENDPOINT and POWEUR_TEST_S3_BUCKET")
	}
	region := os.Getenv("POWEUR_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg := config.Config{
		ListenAddr: ":0", RelayAddress: "relay.test", RelayScheme: "http", DNSTTL: time.Minute, ChallengeTTL: time.Minute,
		Version: "test", HostedDomains: []string{"poweur.net"}, ResolverAllowPrivate: true,
		RateLimits:      config.RateLimits{PerMinute: 100000, PerHour: 100000, PerDay: 100000},
		StorageProvider: config.StorageS3, S3Endpoint: endpoint, S3Bucket: bucket, S3Region: region,
		S3Prefix: "relay-restart/" + randHex(8), S3AccessKey: os.Getenv("POWEUR_TEST_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("POWEUR_TEST_S3_SECRET_KEY"), S3Secure: os.Getenv("POWEUR_TEST_S3_SECURE") != "0",
		S3Presign: os.Getenv("POWEUR_TEST_S3_PRESIGN") != "0",
	}
	start := func() (*Server, *httptest.Server) {
		server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
		if server.DriveError() != nil {
			t.Fatal(server.DriveError())
		}
		ts := httptest.NewServer(server.Router())
		t.Cleanup(ts.Close)
		server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")
		return server, ts
	}
	server, ts := start()
	alice := registerTestIdentity(t, server, ts, "bucketalice.poweur.net")
	resp := sysReq(t, ts, http.MethodPut, alice, alice.name, inboxPolicyPath, []byte(`{"version":1,"mode":"contacts_only"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("policy: %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()
	server.inbox.Add(alice.name, storage.StoredMessage{ID: "m1", Sender: "someone.poweur.net", Recipient: alice.name, Payload: "x"}, 0)

	restarted, _ := start()
	if !restarted.identities.Exists(alice.name) {
		t.Fatal("identity index lost")
	}
	if p, _ := restarted.recipientPolicy(t.Context(), alice.name); p.Mode != idpkg.InboxContactsOnly {
		t.Fatalf("policy lost: %+v", p)
	}
	if pending, _ := restarted.inbox.Since(alice.name, ""); len(pending) != 1 {
		t.Fatalf("spool lost: %+v", pending)
	}
}

func TestDriveLinks(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "linkalice.poweur.net")
	base := "/drive/" + alice.name
	sign := func(m drive.Manifest) drive.Manifest { _ = m.Sign(alice.priv); return m }
	commit := func(body map[string]any) {
		t.Helper()
		body["id"] = randHex(16)
		if status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", body); status != http.StatusOK {
			t.Fatalf("commit: %d %v", status, out)
		}
	}
	root, docs, file := randHex(16), randHex(16), randHex(16)
	commit(map[string]any{"manifest": sign(drive.Manifest{Format: 1, Drive: alice.name, Node: root, Version: randHex(16), Operation: drive.OpCreate,
		Author: alice.name, Generation: 1, Kind: drive.KindFolder, NodeKey: testSealed(1), Pages: []string{}})})
	commit(map[string]any{"manifest": sign(drive.Manifest{Format: 1, Drive: alice.name, Node: docs, Version: randHex(16), Operation: drive.OpCreate,
		Author: alice.name, Generation: 1, Kind: drive.KindFolder, Folder: root, Name: testSealed(2), NameHash: strings.Repeat("ab", 32), NodeKey: testSealed(3), Pages: []string{}})})
	data := testChunk(t, alice.name, "shared by link")
	ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))}
	driveReq(t, ts, http.MethodPut, alice, base+"/chunks/"+ref.ID, data)
	pages, hashes, _ := drive.SplitPages(alice.name, file, []drive.ChunkRef{ref})
	v1 := randHex(16)
	commit(map[string]any{"pages": pages, "manifest": sign(drive.Manifest{Format: 1, Drive: alice.name, Node: file, Version: v1, Operation: drive.OpCreate,
		Author: alice.name, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: docs, Name: testSealed(4), NameHash: strings.Repeat("cd", 32),
		NodeKey: testSealed(5), ContentKey: testSealed(6), Count: 1, Pages: hashes})})

	link := func(verifier string, caps drive.Caps) string {
		id, _ := drive.NewShareID()
		s := drive.Share{Format: 1, Drive: alice.name, ID: randHex(16), Node: docs, Link: id, Role: drive.RoleRead, Generation: 1, NodeKey: testSealed(7),
			NodePublic: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Caps: caps, Issuer: alice.name, Issued: time.Now().UTC().Format(time.RFC3339)}
		if verifier != "" {
			s.KDF, s.Salt, s.VerifierHash = drive.ShareKDF, base64.RawURLEncoding.EncodeToString(make([]byte, 16)), drive.VerifierHash([]byte(verifier))
		}
		_ = s.Sign(alice.priv)
		commit(map[string]any{"share": s})
		return id
	}
	asLink := func(id, verifier, method, path string) (int, map[string]any) {
		hdr := map[string]string{"X-Poweur-Link": id}
		if verifier != "" {
			hdr["X-Poweur-Link-Verifier"] = base64.RawURLEncoding.EncodeToString([]byte(verifier))
		}
		resp := httpReq(t, ts, method, path, "", nil, hdr)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	protected := link("correct horse", drive.Caps{Downloads: 5})

	// Anyone with the ID learns how to open it, and nothing else.
	status, out := asLink("", "", http.MethodGet, base+"/links/"+protected)
	if status != http.StatusOK || out["password"] != true || out["salt"] == nil || out["remaining_opens"] != float64(5) {
		t.Fatalf("link info: %d %v", status, out)
	}
	if status, _ := asLink("", "", http.MethodGet, base+"/links/"+randHex(16)); status != http.StatusNotFound {
		t.Fatalf("unknown link info: %d", status)
	}
	if status, _ := asLink(protected, "", http.MethodGet, base+"/shares"); status != http.StatusUnauthorized {
		t.Fatalf("no password: %d", status)
	}
	status, out = asLink(protected, "correct horse", http.MethodGet, base+"/shares")
	if shares, _ := out["shares"].([]any); status != http.StatusOK || len(shares) != 1 {
		t.Fatalf("open: %d %v", status, out)
	}
	for _, path := range []string{"/nodes/" + file, "/nodes/" + docs + "/children", "/nodes/" + file + "/versions/" + v1 + "/chunks/" + ref.ID} {
		if status, _ := asLink(protected, "correct horse", http.MethodGet, base+path); status != http.StatusOK {
			t.Fatalf("link read %s: %d", path, status)
		}
	}
	// The changes feed shows only what the link can read.
	if status, out := asLink(protected, "correct horse", http.MethodGet, base+"/changes"); status != http.StatusOK {
		t.Fatalf("link changes: %d", status)
	} else {
		for _, c := range out["changes"].([]any) {
			if node := c.(map[string]any)["node"]; node != docs && node != file {
				t.Fatalf("link sees a change outside the share: %v", c)
			}
		}
	}
	for _, path := range []string{"/nodes/" + root, ""} {
		if status, _ := asLink(protected, "correct horse", http.MethodGet, base+path); status < 400 {
			t.Fatalf("link read outside the share %q: %d", path, status)
		}
	}
	if status, _ := asLink(protected, "correct horse", http.MethodGet, base+"/links/"+protected); status != http.StatusOK {
		t.Fatalf("info after one open: %d", status)
	}
	// Guessing the password is throttled per link.
	for i := 0; i < maxLinkPasswordFailures; i++ {
		asLink(protected, "guess", http.MethodGet, base+"/shares")
	}
	if status, _ := asLink(protected, "correct horse", http.MethodGet, base+"/shares"); status != http.StatusTooManyRequests {
		t.Fatalf("after many wrong passwords: %d", status)
	}
	// A link's own hourly cap.
	busy := link("", drive.Caps{PerHour: 2})
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if status, _ := asLink(busy, "", http.MethodGet, base+"/nodes/"+file); status != want {
			t.Fatalf("request %d: %d want %d", i, status, want)
		}
	}
}
