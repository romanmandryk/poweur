package relay

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The relay and state zones are plaintext to the relay but must never reach
// anyone except the owner. Every route that touches system files is probed
// anonymously and as another hosted identity, including well-known names
// that collide with relay/state documents and traversal attempts; no
// response may carry the documents' contents.
func TestRelayAndStateZonesNeverLeak(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "zonealice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "zonebob.poweur.net")

	const marker = "zone-secret-7f3a"
	secrets := map[string]string{
		contactsPath:                         `{"version":1,"contacts":[{"identity":"` + marker + `.poweur.net","state":"accepted"}]}`,
		inboxPolicyPath:                      `{"version":1,"mode":"contacts_only","note":"` + marker + `"}`,
		analyticsPath:                        `{"version":1,"note":"` + marker + `"}`,
		sysRelayDir + "/connected-apps.json": `{"note":"` + marker + `"}`,
		sysRelayDir + "/notes.json":          `{"note":"` + marker + `"}`,
		devicesDocPath:                       `{"version":1,"devices":[{"id":"dev_` + marker + `"}]}`,
		sysStateDir + "/anything-else.json":  `{"note":"` + marker + `"}`,
		sysPublicDir + "/profile.json":       `{"version":1,"display_name":"Zone Alice"}`,
	}
	for path, body := range secrets {
		setSysFile(t, server, alice.name, path, body)
	}

	type probe struct {
		method, path, host string
		as                 *hostedID
	}
	var probes []probe
	for _, as := range []*hostedID{nil, &bob} {
		// Well-known serves .poweur/public only, whatever the name.
		for _, name := range []string{"contacts.json", "inbox-policy.json", "analytics.json", "connected-apps.json",
			"notes.json", "devices.json", "anything-else.json", "state/devices.json", "relay/contacts.json",
			"../state/devices.json", "..%2Fstate%2Fdevices.json", "%2e%2e/relay/contacts.json", ".poweur/state/devices.json"} {
			probes = append(probes, probe{http.MethodGet, "/.well-known/poweur/" + name, alice.name, as})
		}
		for path := range secrets {
			if strings.HasPrefix(path, sysPublicDir) {
				continue
			}
			probes = append(probes, probe{http.MethodGet, "/identities/" + alice.name + "/system/" + path, "", as})
		}
		probes = append(probes,
			probe{http.MethodGet, "/identities/" + alice.name + "/system/.poweur/relay/../state/devices.json", "", as},
			probe{http.MethodGet, "/drive/" + alice.name, "", as},
			probe{http.MethodGet, "/drive/" + alice.name + "/changes", "", as},
			probe{http.MethodGet, "/devices/" + alice.name, "", as},
			probe{http.MethodGet, "/events/" + alice.name, "", as},
			probe{http.MethodPost, "/identities/" + alice.name + "/export", "", as},
			probe{http.MethodGet, "/groups/" + alice.name, "", as},
			probe{http.MethodGet, "/identities/" + alice.name, "", as},
		)
	}

	for _, p := range probes {
		req, err := http.NewRequest(p.method, ts.URL+p.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		if p.host != "" {
			req.Host = p.host
		}
		who := "anonymous"
		if p.as != nil {
			who = p.as.name
			for k, v := range ownerAuth(t, ts, *p.as) {
				req.Header.Set(k, v)
			}
		}
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readLimited(resp, 64<<10)
		if strings.Contains(body, marker) {
			t.Errorf("%s %s as %s leaked a relay/state document (%d): %s", p.method, p.path, who, resp.StatusCode, body)
		}
		// Well-known answers 404 or redirects a traversal to a cleaned path;
		// every authenticated route must refuse (or redirect a traversal). The public
		// identity lookup is the one intended success.
		public := strings.HasPrefix(p.path, "/.well-known/") || p.path == "/identities/"+alice.name
		// A redirect to the cleaned path discloses nothing: that path is
		// probed on its own and requires the owner.
		if !public && resp.StatusCode < 300 {
			t.Errorf("%s %s as %s: %d, want a refusal", p.method, p.path, who, resp.StatusCode)
		}
		if public && resp.StatusCode < 300 && strings.HasPrefix(p.path, "/.well-known/") {
			t.Errorf("%s %s as %s: %d, want 404 or a redirect", p.method, p.path, who, resp.StatusCode)
		}
	}

	// The public zone itself is still served, and the owner still reads hers.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/poweur/profile.json", nil)
	req.Host = alice.name
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(readLimited(resp, 4096), "Zone Alice") {
		t.Fatalf("public profile: %v %v", err, resp)
	}
	resp = sysReq(t, ts, http.MethodGet, alice, alice.name, devicesDocPath, nil, nil)
	if got := readAll(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(got, marker) {
		t.Fatalf("owner read of state: %d %s", resp.StatusCode, got)
	}
}

func readLimited(resp *http.Response, limit int64) string {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return string(raw)
}

// noRedirects reports a redirect instead of following it, so a probe sees
// what the relay answered for exactly the path it sent.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
