package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

func operatorServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.Config{
		RelayAddress:     "relay.test",
		RelayScheme:      "http",
		DataDir:          t.TempDir(),
		HostedDomains:    []string{"poweur.net"},
		RegistrationGate: RegistrationGatePow,
		NamePolicy:       idpkg.NamePolicy{MinLen: 6, MaxLen: 24},
		OperatorToken:    token,
		RateLimits:       config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")
	return server, ts
}

// registerHosted posts a correctly signed hosted registration for name,
// with the operator token header when token is not empty.
func registerHosted(t *testing.T, server *Server, ts *httptest.Server, name, token string) (int, ErrorResponse) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + name
	relayAddr := server.cfg.RelayAddress
	canon := crypto.CanonicalIdentityRegistration(name, pubB64, "", relayAddr, issued, nonce)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))
	doc := idpkg.NewDocument(name, idpkg.FormatEd25519PublicKey(pub), "", relayAddr, nil)
	doc.UpdatedAt = issued
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)
	body, _ := json.Marshal(IdentityRequest{
		Identity: name, PublicKey: pubB64, IssuedAt: issued, Nonce: nonce,
		IdentitySignature: sig, IdentityDocument: docRaw,
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/identities", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(operatorTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var er ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	return resp.StatusCode, er
}

func TestOperatorRegistersNamesThePolicyHoldsBack(t *testing.T) {
	const token = "operator-secret-0123456789abcdef"
	server, ts := operatorServer(t, token)

	// Without the token: reserved and too short are refused, as for anyone.
	if code, er := registerHosted(t, server, ts, "support.poweur.net", ""); code == http.StatusCreated {
		t.Fatalf("reserved name registered without a token: %+v", er)
	}
	if code, _ := registerHosted(t, server, ts, "hello.poweur.net", ""); code == http.StatusCreated {
		t.Fatal("short name registered without a token")
	}

	// With it: both go through, with no proof-of-work, and are then reachable.
	for _, name := range []string{"support.poweur.net", "hello.poweur.net"} {
		if code, er := registerHosted(t, server, ts, name, token); code != http.StatusCreated {
			t.Fatalf("%s: operator registration %d %+v", name, code, er)
		}
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/poweur/id.json", nil)
		req.Host = name
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: well-known %d", name, resp.StatusCode)
		}
	}

	// Its door offers sign-in: the name exists, so it is taken, not reserved.
	resp, err := http.Get(ts.URL + "/hosted/availability?handle=support&domain=poweur.net")
	if err != nil {
		t.Fatal(err)
	}
	var verdict struct{ Reason string }
	_ = json.NewDecoder(resp.Body).Decode(&verdict)
	resp.Body.Close()
	if verdict.Reason != string(idpkg.ReasonTaken) {
		t.Fatalf("availability reason = %q, want taken", verdict.Reason)
	}

	// Still one owner per name.
	if code, _ := registerHosted(t, server, ts, "support.poweur.net", token); code != http.StatusConflict {
		t.Fatalf("second registration: %d, want 409", code)
	}
	// Still only hosted domains, and still well-formed names.
	if code, _ := registerHosted(t, server, ts, "support.evil.net", token); code == http.StatusCreated {
		t.Fatal("operator registered a foreign domain")
	}
	if code, _ := registerHosted(t, server, ts, "bad_name.poweur.net", token); code == http.StatusCreated {
		t.Fatal("operator registered a malformed name")
	}
}

func TestOperatorTokenMustMatch(t *testing.T) {
	server, ts := operatorServer(t, "operator-secret-0123456789abcdef")
	code, er := registerHosted(t, server, ts, "support.poweur.net", "wrong")
	if code != http.StatusUnauthorized || er.Error != "invalid_operator_token" {
		t.Fatalf("wrong token: %d %+v", code, er)
	}

	// A relay without OPERATOR_TOKEN accepts no token at all.
	server, ts = operatorServer(t, "")
	code, er = registerHosted(t, server, ts, "support.poweur.net", "anything")
	if code != http.StatusUnauthorized || er.Error != "invalid_operator_token" {
		t.Fatalf("no token configured: %d %+v", code, er)
	}
}
