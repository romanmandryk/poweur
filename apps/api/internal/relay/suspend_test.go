package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/drive/provider/fs"
	idpkg "github.com/poweur/identity"
)

// refreshSuspensionsEveryRequest makes a relay see a changed list at once.
func refreshSuspensionsEveryRequest(t *testing.T) {
	t.Helper()
	prev := suspensionInterval
	suspensionInterval = 0
	t.Cleanup(func() { suspensionInterval = prev })
}

func suspendIdentity(t *testing.T, store provider.Store, identity string, sp Suspension) {
	t.Helper()
	if _, err := EditSuspensions(context.Background(), store, func(doc map[string]Suspension) error {
		doc[identity] = sp
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func getStatus(t *testing.T, ts *httptest.Server, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	for k, v := range hdr {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestParseSuspensions(t *testing.T) {
	got, err := parseSuspensions([]byte(`{"Spam.Poweur.net": {"reason": "spam", "at": "2026-10-01T10:00:00Z"}, "gone.poweur.net": {"reason": "x", "at": "", "deleted": true}}`))
	if err != nil || got["spam.poweur.net"].Reason != "spam" || !got["gone.poweur.net"].Deleted {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{`[]`, `not json`, `{"not an identity": {"reason": "x"}}`, `{"a/b.poweur.net": {}}`} {
		if _, err := parseSuspensions([]byte(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestEditSuspensionsRefusesAConflictAndABadName(t *testing.T) {
	store, err := fs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := EditSuspensions(ctx, store, func(doc map[string]Suspension) error {
		doc["a.poweur.net"] = Suspension{Reason: "first"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Another operator writes between our read and our write.
	_, err = EditSuspensions(ctx, store, func(doc map[string]Suspension) error {
		suspendIdentity(t, store, "b.poweur.net", Suspension{Reason: "raced"})
		doc["c.poweur.net"] = Suspension{Reason: "mine"}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed while editing") {
		t.Fatalf("conflict = %v", err)
	}
	if _, err := EditSuspensions(ctx, store, func(doc map[string]Suspension) error {
		doc["not an id"] = Suspension{}
		return nil
	}); err == nil {
		t.Fatal("a malformed identity was written")
	}
	// Nothing bad reached the store: the list is still readable.
	doc, err := EditSuspensions(ctx, store, func(map[string]Suspension) error { return nil })
	if err != nil || len(doc) != 2 {
		t.Fatalf("doc = %+v, %v", doc, err)
	}
}

func TestSuspendedIdentityIsBlockedAndLiftable(t *testing.T) {
	refreshSuspensionsEveryRequest(t)
	server, ts := newTestRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")
	registerTestIdentity(t, server, ts, "bobby.poweur.net")

	if code, _ := getStatus(t, ts, "/identities/alice.poweur.net", nil); code != http.StatusOK {
		t.Fatalf("before = %d", code)
	}
	suspendIdentity(t, server.Drive(), "alice.poweur.net", Suspension{Reason: "spam", At: "2026-10-01T10:00:00Z"})

	for _, tc := range []struct {
		name   string
		path   string
		hdr    map[string]string
		status int
	}{
		{"resolving the ID", "/identities/alice.poweur.net", nil, http.StatusGone},
		{"its drive", "/drive/alice.poweur.net", nil, http.StatusGone},
		{"its inbox", "/messages/alice.poweur.net", nil, http.StatusGone},
		{"its own page", "/", map[string]string{"Host": "alice.poweur.net", "Accept": "text/html"}, http.StatusGone},
		{"a call made as it", "/messages/bobby.poweur.net", map[string]string{"X-Poweur-Identity": "Alice.Poweur.net"}, http.StatusForbidden},
		{"another ID is unaffected", "/identities/bobby.poweur.net", nil, http.StatusOK},
	} {
		code, body := getStatus(t, ts, tc.path, tc.hdr)
		if code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, code, body, tc.status)
		}
		if tc.status != http.StatusOK && !strings.Contains(body, "identity_suspended") {
			t.Errorf("%s: body %s", tc.name, body)
		}
	}
	if code, _ := getStatus(t, ts, "/health", map[string]string{"X-Poweur-Identity": "alice.poweur.net"}); code != http.StatusOK {
		t.Errorf("health = %d", code)
	}

	if _, err := EditSuspensions(context.Background(), server.Drive(), func(doc map[string]Suspension) error {
		delete(doc, "alice.poweur.net")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := getStatus(t, ts, "/identities/alice.poweur.net", nil); code != http.StatusOK {
		t.Fatalf("after lifting = %d %s", code, body)
	}
}

func TestHeldNameCannotBeClaimedAgain(t *testing.T) {
	refreshSuspensionsEveryRequest(t)
	server, ts := newTestRelay(t)
	suspendIdentity(t, server.Drive(), "ghost.poweur.net", Suspension{Reason: "deleted by the operator", At: "2026-10-01T10:00:00Z", Deleted: true})

	code, body := getStatus(t, ts, "/hosted/availability?handle=ghost", nil)
	if code != http.StatusOK || !strings.Contains(body, `"reason":"reserved"`) || strings.Contains(body, `"available":true`) {
		t.Fatalf("availability = %d %s", code, body)
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	issued := time.Now().UTC().Format(time.RFC3339)
	canon := crypto.CanonicalIdentityRegistration("ghost.poweur.net", pubB64, "", server.cfg.RelayAddress, issued, "n1")
	doc := idpkg.NewDocument("ghost.poweur.net", idpkg.FormatEd25519PublicKey(pub), "", server.cfg.RelayAddress, nil)
	doc.UpdatedAt = issued
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)
	reqBody, _ := json.Marshal(IdentityRequest{
		Identity: "ghost.poweur.net", PublicKey: pubB64, IssuedAt: issued, Nonce: "n1",
		IdentitySignature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon))), IdentityDocument: docRaw,
	})
	resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "name_held") {
		t.Fatalf("register = %d %s", resp.StatusCode, raw)
	}
}

func TestRejectHeldNamesEitherEnd(t *testing.T) {
	refreshSuspensionsEveryRequest(t)
	server, _ := newTestRelay(t)
	suspendIdentity(t, server.Drive(), "alice.poweur.net", Suspension{Reason: "spam"})
	rec := httptest.NewRecorder()
	if !server.rejectHeld(rec, "bobby.poweur.net", "ALICE.poweur.net") || rec.Code != http.StatusGone {
		t.Fatalf("recipient side = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	if server.rejectHeld(rec, "bobby.poweur.net") {
		t.Fatal("an unsuspended ID was rejected")
	}
}

func TestDeleteIdentityDataErasesOnlyThatIdentity(t *testing.T) {
	store, err := fs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice, bob := "alice__poweur__net", "bobby__poweur__net"
	keys := func(id string) []string {
		return []string{
			"relay/identities/" + id + ".json",
			"relay/keystore/" + id + ".json",
			"relay/spool/messages/" + id + "/00000000000000000001.json",
			"relay/spool/acks/" + id + "/00000000000000000001.json",
			"drives/" + id + "/journal/00000000000000000001.json",
			"drives/" + id + "/chunks/abc",
		}
	}
	for _, id := range []string{alice, bob} {
		for _, key := range keys(id) {
			if _, err := store.Put(ctx, key, []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := EditQuotas(ctx, store, func(doc map[string]json.RawMessage) error {
		doc["alice.poweur.net"] = json.RawMessage(`"2GiB"`)
		doc["bobby.poweur.net"] = json.RawMessage(`"1GiB"`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := DeleteIdentityData(ctx, store, "Alice.poweur.net")
	if err != nil || removed != 6 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	for _, key := range keys(alice) {
		if _, err := store.Get(ctx, key, nil); err == nil {
			t.Errorf("%s survived", key)
		}
	}
	for _, key := range keys(bob) {
		if _, err := store.Get(ctx, key, nil); err != nil {
			t.Errorf("%s was lost: %v", key, err)
		}
	}
	quotas, _ := EditQuotas(ctx, store, func(map[string]json.RawMessage) error { return nil })
	if _, ok := quotas["alice.poweur.net"]; ok || quotas["bobby.poweur.net"] == 0 {
		t.Fatalf("quotas = %v", quotas)
	}
	// Running it again finishes cleanly.
	if _, err := DeleteIdentityData(ctx, store, "alice.poweur.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteIdentityData(ctx, store, "not an identity"); err == nil {
		t.Fatal("a malformed identity was accepted")
	}
}
