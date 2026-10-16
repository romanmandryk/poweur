// Integration coverage for handles on other networks (INT-006-T5, PCP-0011):
// an owner links a Bluesky DID, a Nostr key and a Mastodon account through
// the system-file API, and a stranger reads them back in each network's own
// discovery format from a real hosted relay.
package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestINT_HANDLES_01_BlueskyNostrAndMastodon(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	zone.SetHost("hdalice.poweur.net", addr)
	home := t.TempDir()
	runCLI(t, home, "identity", "create", "hdalice.poweur.net", "--hosted", "--relay", ts.URL, "--json")

	get := func(host, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, _ := get("hdalice.poweur.net", "/.well-known/atproto-did"); code != http.StatusNotFound {
		t.Fatalf("served before opting in: %d", code)
	}

	const key = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"
	putPublicDoc(t, ts.URL, home, "hdalice.poweur.net", "handles.json", map[string]any{
		"version":         1,
		"atproto_did":     "did:plc:z72i7hdynmk6r22z27h6tvur",
		"nostr_pubkey":    key,
		"fediverse_acct":  "hdalice@mastodon.example",
		"fediverse_actor": "https://mastodon.example/users/hdalice",
	})

	if code, body := get("hdalice.poweur.net", "/.well-known/atproto-did"); code != http.StatusOK || strings.TrimSpace(body) != "did:plc:z72i7hdynmk6r22z27h6tvur" {
		t.Fatalf("atproto-did = %d %q", code, body)
	}
	var nip05 struct{ Names map[string]string }
	code, body := get("poweur.net", "/.well-known/nostr.json?name=hdalice")
	_ = json.Unmarshal([]byte(body), &nip05)
	if code != http.StatusOK || nip05.Names["hdalice"] != key {
		t.Fatalf("nostr.json = %d %s", code, body)
	}
	if code, body := get("poweur.net", "/.well-known/webfinger?resource=acct:hdalice@poweur.net"); code != http.StatusOK || !strings.Contains(body, `"acct:hdalice@mastodon.example"`) {
		t.Fatalf("webfinger = %d %s", code, body)
	}
}
