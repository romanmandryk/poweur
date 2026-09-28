package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// sysFileRequest calls the owner system-file API as identity, signing a fresh
// challenge with the key in home. System files live in the identity's drive
// (EPIC-020 E20-T6), so tests reach them the way clients do.
func sysFileRequest(t *testing.T, relayURL, home, identity, method, path string, body []byte) (int, []byte) {
	t.Helper()
	key := loadIdentityKey(t, home, identity)
	resp, err := http.Get(relayURL + "/auth/challenge?identity=" + identity)
	if err != nil {
		t.Fatal(err)
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
	req, _ := http.NewRequest(method, relayURL+"/identities/"+identity+"/system/"+path, reader)
	req.Header.Set("X-Poweur-Identity", identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(challenge.Challenge))))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// readRelaySysFile reads one of identity's system files.
func readRelaySysFile(t *testing.T, relayURL, home, identity, path string) []byte {
	t.Helper()
	status, raw := sysFileRequest(t, relayURL, home, identity, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("read %s %s: %d %s", identity, path, status, raw)
	}
	return raw
}

// writeRelaySysFile replaces one of identity's system files behind the
// client's back — tests use it to simulate a tampered or swapped document.
func writeRelaySysFile(t *testing.T, relayURL, home, identity, path string, raw []byte) {
	t.Helper()
	if status, out := sysFileRequest(t, relayURL, home, identity, http.MethodPut, path, raw); status != http.StatusOK {
		t.Fatalf("write %s %s: %d %s", identity, path, status, out)
	}
}
