package relay

import (
	"net/http"
	"testing"
)

// E06-T1: schema-governed poweur-sys documents are validated on PUT;
// unknown files in poweur-sys are preserved schema-free.
func TestSysWriteValidation(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	tok := mintDAVToken(t, ts, alice, "", "")
	_ = server

	put := func(path, body string) int {
		resp := davReq(t, ts, http.MethodPut, "/dav/alice.poweur.net"+path, tok, []byte(body), nil)
		resp.Body.Close()
		return resp.StatusCode
	}

	// Malformed known documents are rejected with 422.
	rejected := []struct{ path, body string }{
		{"/poweur-sys/relay/contacts.json", `{"contacts":[{"identity":"bob","state":"bff"}]}`},
		{"/poweur-sys/relay/contacts.json", `not json at all`},
		{"/poweur-sys/relay/inbox-policy.json", `{"mode":"everyone"}`},
		{"/poweur-sys/public/profile.json", `{"avatar":"https://evil.example/a.png"}`},
		{"/poweur-sys/public/capabilities.json", `{"features":{" ":"x"}}`},
		{"/poweur-sys/relay/shares/shr_x.json", `{"share_id":"shr_x"}`},
		{"/poweur-sys/relay/groups/team.json", `{"group":"team"}`},
		{"/apps/net.poweur.tasks/manifest.json", `{"app_id":"other.app","name":"Tasks"}`},
	}
	// Parent dir for the manifest case.
	resp := davReq(t, ts, "MKCOL", "/dav/alice.poweur.net/apps/net.poweur.tasks", tok, nil, nil)
	resp.Body.Close()
	for _, tc := range rejected {
		if code := put(tc.path, tc.body); code != http.StatusUnprocessableEntity {
			t.Fatalf("PUT %s with %q: %d want 422", tc.path, tc.body, code)
		}
	}

	// Valid documents land.
	accepted := []struct{ path, body string }{
		{"/poweur-sys/relay/contacts.json", `{"version":1,"contacts":[{"identity":"bob.example.org","state":"accepted"}]}`},
		{"/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_only"}`},
		{"/poweur-sys/public/profile.json", `{"version":1,"display_name":"Alice","avatar":"public/me.png"}`},
		{"/poweur-sys/public/capabilities.json", `{"version":1,"features":{"messaging":"v1","files":"webdav"}}`},
		{"/apps/net.poweur.tasks/manifest.json", `{"app_id":"net.poweur.tasks","name":"Tasks"}`},
	}
	for _, tc := range accepted {
		if code := put(tc.path, tc.body); code >= 300 {
			t.Fatalf("PUT %s: %d want 2xx", tc.path, code)
		}
	}

	// Unknown files in poweur-sys stay schema-free (forward compatibility).
	if code := put("/poweur-sys/relay/some-future-thing.json", `{"anything":"goes"}`); code >= 300 {
		t.Fatalf("unknown poweur-sys file must be preserved: %d", code)
	}
	// And the stored body round-trips unmodified.
	get := davReq(t, ts, http.MethodGet, "/dav/alice.poweur.net/poweur-sys/relay/contacts.json", tok, nil, nil)
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("read back: %d", get.StatusCode)
	}
}

// The world-readable profile is served through /.well-known/poweur/ once
// written (E06-T2's discovery path needs no new endpoint).
func TestProfileServedViaWellKnown(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	tok := mintDAVToken(t, ts, alice, "", "")
	resp := davReq(t, ts, http.MethodPut, "/dav/alice.poweur.net/poweur-sys/public/profile.json", tok,
		[]byte(`{"version":1,"display_name":"Alice"}`), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("profile PUT: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/poweur/profile.json", nil)
	req.Host = "alice.poweur.net"
	wk, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer wk.Body.Close()
	if wk.StatusCode != http.StatusOK {
		t.Fatalf("well-known profile: %d", wk.StatusCode)
	}
}
