package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestEmailAddressOf(t *testing.T) {
	const id = "alice.poweur.net"
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	doc := NewDocument(id, FormatEd25519PublicKey(pub), "", "relay.poweur.net", nil)
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docJSON, _ := json.Marshal(doc)
	signedOptIn := func(key ed25519.PrivateKey) string {
		o, err := NewEmailOptIn(id, "mail.example.org", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Sign(key); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(o)
		return string(raw)
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)

	cases := []struct {
		name      string
		emailJSON string // "" = absent
		caps      string // "" = absent
		want      string
		ok        bool
	}{
		{name: "on by default through the relay's bridge", caps: `{"version":1,"endpoints":{"email_bridge":"email.poweur.net"},"features":{"email_receive":"on"}}`, want: "alice@poweur.net", ok: true},
		{name: "opted out", caps: `{"version":1,"endpoints":{"email_bridge":"email.poweur.net"},"features":{"email_receive":"off"}}`},
		{name: "relay offers no bridge", caps: `{"version":1,"features":{"messaging":"v1"}}`},
		{name: "no capabilities at all"},
		{name: "a signed opt-in names the address", emailJSON: signedOptIn(priv), want: "alice@poweur.net", ok: true},
		{name: "an opt-in signed by another key is ignored", emailJSON: signedOptIn(otherKey), caps: `{"version":1,"endpoints":{"email_bridge":"email.poweur.net"},"features":{"email_receive":"on"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, closeFn := testOptions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/poweur/id.json":
					_, _ = w.Write(docJSON)
				case WellKnownEmailPath:
					if tc.emailJSON == "" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = w.Write([]byte(tc.emailJSON))
				case WellKnownCapabilitiesPath:
					if tc.caps == "" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = w.Write([]byte(tc.caps))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer closeFn()
			opts.SkipDNS = true
			got, ok, err := EmailAddressOf(context.Background(), id, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %q %v, want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
