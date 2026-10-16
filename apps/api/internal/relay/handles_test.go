package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/poweur/api/internal/storage"
)

const (
	handlesDID = "did:plc:z72i7hdynmk6r22z27h6tvur"
	handlesKey = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"
)

func handlesGet(t *testing.T, ts *httptest.Server, host, path string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestHandlesServeBlueskyAndNostr(t *testing.T) {
	s := newIdentityPageServer(t)
	s.identities.Add(storage.Identity{Identity: "bob.poweur.net"})
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// Nothing is served before an ID opts in.
	if code, _, _ := handlesGet(t, ts, pageIdentity, "/.well-known/atproto-did"); code != http.StatusNotFound {
		t.Fatalf("atproto-did without handles = %d", code)
	}
	if code, _, _ := handlesGet(t, ts, "poweur.net", "/.well-known/nostr.json?name=alice"); code != http.StatusNotFound {
		t.Fatalf("nostr.json without handles = %d", code)
	}

	if err := s.sysFiles.Write(t.Context(), pageIdentity, handlesPath,
		[]byte(`{"version":1,"atproto_did":"`+handlesDID+`","nostr_pubkey":"`+handlesKey+`","nostr_relays":["wss://relay.example"]}`)); err != nil {
		t.Fatal(err)
	}

	code, body, hdr := handlesGet(t, ts, pageIdentity, "/.well-known/atproto-did")
	if code != http.StatusOK || strings.TrimSpace(body) != handlesDID || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("atproto-did = %d %q %q", code, body, hdr.Get("Content-Type"))
	}

	type nip05 struct {
		Names  map[string]string   `json:"names"`
		Relays map[string][]string `json:"relays"`
	}
	for _, tc := range []struct{ host, query, name string }{
		{pageIdentity, "_", "_"},         // _@alice.poweur.net
		{"poweur.net", "alice", "alice"}, // alice@poweur.net, the email address too
		{"poweur.net", "Alice", "alice"},
	} {
		code, body, hdr := handlesGet(t, ts, tc.host, "/.well-known/nostr.json?name="+tc.query)
		var got nip05
		_ = json.Unmarshal([]byte(body), &got)
		if code != http.StatusOK || got.Names[tc.name] != handlesKey || len(got.Relays[handlesKey]) != 1 {
			t.Fatalf("%s ?name=%s = %d %s", tc.host, tc.query, code, body)
		}
		if hdr.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("NIP-05 needs CORS for web clients")
		}
		if len(got.Names) != 1 {
			t.Fatalf("answer lists more than the name asked for: %s", body)
		}
	}

	// Never a listing, never someone else's key, never a foreign or nested name.
	for _, tc := range []struct{ host, path string }{
		{"poweur.net", "/.well-known/nostr.json"},
		{"poweur.net", "/.well-known/nostr.json?name=bob"},
		{"poweur.net", "/.well-known/nostr.json?name=_"},
		{"poweur.net", "/.well-known/nostr.json?name=alice.poweur"},
		{pageIdentity, "/.well-known/nostr.json?name=alice"},
		{"bob.poweur.net", "/.well-known/nostr.json?name=_"},
		{"bob.poweur.net", "/.well-known/atproto-did"},
		{"example.org", "/.well-known/nostr.json?name=alice"},
	} {
		if code, body, _ := handlesGet(t, ts, tc.host, tc.path); code != http.StatusNotFound {
			t.Fatalf("%s%s = %d %s", tc.host, tc.path, code, body)
		}
	}
}

func TestHandlesWebFingerRedirectsToTheLinkedAccount(t *testing.T) {
	s := newIdentityPageServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	const path = "/.well-known/webfinger?resource=acct:alice@poweur.net"
	if code, _, _ := handlesGet(t, ts, "poweur.net", path); code != http.StatusNotFound {
		t.Fatalf("webfinger without a linked account = %d", code)
	}
	if err := s.sysFiles.Write(t.Context(), pageIdentity, handlesPath,
		[]byte(`{"version":1,"fediverse_acct":"alice@mastodon.social","fediverse_actor":"https://mastodon.social/users/alice"}`)); err != nil {
		t.Fatal(err)
	}
	code, body, hdr := handlesGet(t, ts, "poweur.net", path)
	var jrd struct {
		Subject string `json:"subject"`
		Links   []struct{ Rel, Type, Href string }
	}
	_ = json.Unmarshal([]byte(body), &jrd)
	if code != http.StatusOK || jrd.Subject != "acct:alice@mastodon.social" || len(jrd.Links) != 1 ||
		jrd.Links[0].Rel != "self" || jrd.Links[0].Href != "https://mastodon.social/users/alice" ||
		hdr.Get("Content-Type") != "application/jrd+json" {
		t.Fatalf("webfinger = %d %s", code, body)
	}
	for _, tc := range []struct{ host, path string }{
		{"poweur.net", "/.well-known/webfinger?resource=acct:alice@example.org"},
		{"poweur.net", "/.well-known/webfinger?resource=https://alice.poweur.net"},
		{"poweur.net", "/.well-known/webfinger"},
		{pageIdentity, path},
	} {
		if code, body, _ := handlesGet(t, ts, tc.host, tc.path); code != http.StatusNotFound {
			t.Fatalf("%s%s = %d %s", tc.host, tc.path, code, body)
		}
	}
}

func TestHandlesWriteIsValidated(t *testing.T) {
	validate := sysValidators[handlesPath]
	if validate == nil {
		t.Fatal("handles.json has no write validator")
	}
	if err := validate([]byte(`{"version":1,"nostr_pubkey":"npub1xyz"}`)); err == nil {
		t.Fatal("an npub was accepted where hex is required")
	}
	if err := validate([]byte(`{"version":1,"atproto_did":"` + handlesDID + `"}`)); err != nil {
		t.Fatal(err)
	}
}
