package identity

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func b64(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func x25519Pub(t testing.TB) string {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

var shortCodeCases = []struct {
	input string
	code  string // "" = invalid
}{
	{"K7QM4XP2", "K7QM4XP2"},
	{"k7qm-4xp2", "K7QM4XP2"},
	{" K7QM 4XP2 ", "K7QM4XP2"},
	{"K7QM–4XP2", "K7QM4XP2"}, // en dash from a phone keyboard
	{"O0IL-1234", "00111234"}, // O reads as 0, I and L as 1
	{"K7QM-4XP", ""},
	{"K7QM-4XP23", ""},
	{"K7QU-4XP2", ""}, // U is not in the alphabet
	{"K7QM-4XP!", ""},
	{"", ""},
}

func TestShortCodes(t *testing.T) {
	for _, tc := range shortCodeCases {
		got, err := NormalizeShortCode(tc.input)
		if tc.code == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", tc.input, got)
			}
			continue
		}
		if err != nil || got != tc.code {
			t.Errorf("%q = %q, %v; want %q", tc.input, got, err, tc.code)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := NewShortCode()
		if err != nil {
			t.Fatal(err)
		}
		if n, err := NormalizeShortCode(FormatShortCode(c)); err != nil || n != c || seen[c] {
			t.Fatalf("code %q: %q %v (seen %v)", c, n, err, seen[c])
		}
		seen[c] = true
	}
	if FormatShortCode("K7QM4XP2") != "K7QM-4XP2" {
		t.Fatal(FormatShortCode("K7QM4XP2"))
	}
}

func TestPairingCommitRevealAndDigits(t *testing.T) {
	k, r, n := x25519Pub(t), b64(32), b64(32)
	c := PairingCommitment(k, r)
	if err := CheckPairingCommitment(c); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPairingReveal(c, k, r); err != nil {
		t.Fatal(err)
	}
	// Another key, or another nonce, does not open it.
	if err := VerifyPairingReveal(c, x25519Pub(t), r); !errors.Is(err, ErrPairing) {
		t.Fatalf("swapped key = %v", err)
	}
	if err := VerifyPairingReveal(c, k, b64(32)); !errors.Is(err, ErrPairing) {
		t.Fatalf("swapped nonce = %v", err)
	}
	if err := VerifyPairingReveal(c, "short", r); !errors.Is(err, ErrPairing) {
		t.Fatalf("malformed key = %v", err)
	}
	sas := PairingSAS(c, k, r, n)
	if len(sas) != PairingSASDigits || strings.Trim(sas, "0123456789") != "" {
		t.Fatalf("sas = %q", sas)
	}
	if PairingSAS(c, k, r, b64(32)) == sas && PairingSAS(c, k, r, b64(32)) == sas {
		t.Fatal("the approver's nonce does not move the digits")
	}
}

// The attack v2 exists to stop. v1's digits were a function of the key
// alone, so a relay that saw the honest key could grind its own key to the
// same digits. Scaled down to 2 digits so the test runs in milliseconds: the
// grind succeeds against v1 and has nothing to aim at in v2.
func TestRelayCannotGrindAMatchingKey(t *testing.T) {
	v1 := func(key string) uint64 {
		sum := sha256.Sum256([]byte("poweur/v1/enroll-sas\n" + key))
		return uint64(binary.BigEndian.Uint32(sum[:4])) % 100
	}
	honest := x25519Pub(t)
	target := v1(honest)
	found := false
	for i := 0; i < 20000 && !found; i++ {
		found = v1(x25519Pub(t)) == target
	}
	if !found {
		t.Fatal("expected the v1 grind to succeed; the test's premise is wrong")
	}

	// v2: the relay must hand the approver a commitment before it sees the
	// approver's nonce, and the honest device reveals only after the nonce.
	// Whatever key the relay committed to, the digits it will produce on the
	// approver are fixed before n exists, and the honest device's digits
	// depend on its hidden (K, r) and n — so the relay can only guess.
	hits, trials := 0, 2000
	for i := 0; i < trials; i++ {
		kh, rh := x25519Pub(t), b64(32)
		ch := PairingCommitment(kh, rh)
		km, rm := x25519Pub(t), b64(32) // the relay's substitute, committed first
		cm := PairingCommitment(km, rm)
		n := b64(32) // the approver's nonce
		if PairingSAS(ch, kh, rh, n)[4:] == PairingSAS(cm, km, rm, n)[4:] {
			hits++
		}
	}
	if hits > trials/20 { // ~1% expected at 2 digits
		t.Fatalf("substitute matched %d/%d times", hits, trials)
	}
}

func TestPairingLinks(t *testing.T) {
	c := PairingCommitment(x25519Pub(t), b64(32))
	link := PairingLink("https://alice.poweur.net/app/", "Alice.Poweur.net", "K7QM4XP2", c)
	if link != "https://alice.poweur.net/app/#pair=K7QM4XP2."+c+"&id=alice.poweur.net" {
		t.Fatal(link)
	}
	app := PairingAppLink("Alice.Poweur.net", "K7QM4XP2", c)
	if app != "poweur://pair?pair=K7QM4XP2."+c+"&id=alice.poweur.net" {
		t.Fatal(app)
	}
	for in, id := range map[string]string{link: "alice.poweur.net", app: "alice.poweur.net", "#pair=K7QM4XP2." + c: "", "k7qm-4xp2." + c: "", "pair=K7QM4XP2." + c: ""} {
		got, err := ParsePairingLink(in)
		if err != nil || got != (PairingLinkParts{Code: "K7QM4XP2", Commitment: c, Identity: id}) {
			t.Errorf("%q = %+v %v", in, got, err)
		}
	}
	for _, in := range []string{"https://alice.poweur.net/app/", "K7QM4XP2", "K7QM4XP2.short", "BAD!.x" + c} {
		if _, err := ParsePairingLink(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

type pairingVector struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	CommitNonce        string `json:"commit_nonce"`
	ApproverNonce      string `json:"approver_nonce"`
	Commitment         string `json:"commitment"`
	SAS                string `json:"sas"`
}

type shortCodeVector struct {
	Input string `json:"input"`
	Code  string `json:"code"`
	Valid bool   `json:"valid"`
}

// Fixed inputs so TypeScript (and every other client) can reproduce them.
func TestVectors_Pairing(t *testing.T) {
	var cases []pairingVector
	for i := 0; i < 4; i++ {
		fill := func(tag string, n int) string {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", tag, i)))
			return base64.RawURLEncoding.EncodeToString(sum[:n])
		}
		v := pairingVector{EphemeralPublicKey: fill("key", 32), CommitNonce: fill("r", 32), ApproverNonce: fill("n", 32)}
		v.Commitment = PairingCommitment(v.EphemeralPublicKey, v.CommitNonce)
		v.SAS = PairingSAS(v.Commitment, v.EphemeralPublicKey, v.CommitNonce, v.ApproverNonce)
		cases = append(cases, v)
	}
	var codes []shortCodeVector
	for _, tc := range shortCodeCases {
		codes = append(codes, shortCodeVector{Input: tc.input, Code: tc.code, Valid: tc.code != ""})
	}
	WriteVectors(t, vectorsDir, "pairing", map[string]any{"pairings": cases, "short_codes": codes})
}

var signInReferenceCases = map[string]string{
	"https://oauth.poweur.org/r/K7QM4XP2":                                        "https://oauth.poweur.org/r/K7QM4XP2",
	"  https://oauth.poweur.org/r/K7QM4XP2 ":                                     "https://oauth.poweur.org/r/K7QM4XP2",
	"poweur://auth?request_uri=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2":    "https://oauth.poweur.org/r/K7QM4XP2",
	"https://poweur.net/app/?auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2": "https://oauth.poweur.org/r/K7QM4XP2",
	"http://oauth.localhost:8090/r/K7QM4XP2":                                     "http://oauth.localhost:8090/r/K7QM4XP2",
	"poweur://auth?request=eyJ4IjoxfQ":                                           "",
	"https://poweur.net/app/?auth=eyJ4IjoxfQ":                                    "",
	"eyJwb3dldXJfYXV0aCI6IjEifQ":                                                 "",
	"poweur://auth?request_uri=javascript%3Aalert(1)":                            "",
	"ftp://oauth.poweur.org/r/K7QM4XP2":                                          "",
}

var signInOriginCases = []struct {
	URI      string `json:"uri"`
	Audience string `json:"audience"`
	OK       bool   `json:"ok"`
}{
	{"https://oauth.poweur.org/r/K7QM4XP2", "https://oauth.poweur.org", true},
	{"https://oauth.poweur.org:443/r/K7QM4XP2", "https://OAUTH.poweur.org/", true},
	{"http://oauth.localhost:8090/r/K7QM4XP2", "http://oauth.localhost:8090", true},
	{"https://evil.example/r/K7QM4XP2", "https://oauth.poweur.org", false},
	{"http://oauth.poweur.org/r/K7QM4XP2", "https://oauth.poweur.org", false},
	{"https://oauth.poweur.org.evil.example/r/K7QM4XP2", "https://oauth.poweur.org", false},
}

func TestSignInRequestByReference(t *testing.T) {
	for in, want := range signInReferenceCases {
		got, ok := SignInRequestURI(in)
		if (want == "") == ok || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, ok, want)
		}
	}
	if SignInReferenceDeepLink("https://oauth.poweur.org/r/K7QM4XP2") != "poweur://auth?request_uri=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2" {
		t.Fatal(SignInReferenceDeepLink("https://oauth.poweur.org/r/K7QM4XP2"))
	}
	for _, tc := range signInOriginCases {
		err := CheckSignInRequestURI(tc.URI, SignInRequest{Audience: tc.Audience})
		if (err == nil) != tc.OK {
			t.Errorf("%s at %s: %v", tc.URI, tc.Audience, err)
		}
	}
}

func TestVectors_SignInReference(t *testing.T) {
	type form struct {
		Input string `json:"input"`
		URI   string `json:"uri"`
		OK    bool   `json:"ok"`
	}
	var forms []form
	for in, uri := range signInReferenceCases {
		forms = append(forms, form{Input: in, URI: uri, OK: uri != ""})
	}
	sort.Slice(forms, func(i, j int) bool { return forms[i].Input < forms[j].Input })
	WriteVectors(t, vectorsDir, "signin-reference", map[string]any{"forms": forms, "origins": signInOriginCases})
}

func TestFetchSignInRequest(t *testing.T) {
	var audience string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "want json", 400)
			return
		}
		switch r.URL.Path {
		case "/r/GONE":
			http.Error(w, "gone", http.StatusGone)
			return
		case "/r/JUNK":
			fmt.Fprint(w, `{"nope":1}`)
			return
		}
		enc, _ := EncodeSignInRequest(SignInRequest{
			PoweurAuth: "1", RequestID: "rq", Domain: "x", Audience: audience, Nonce: "nnnnnnnnnnnnnnnnnnnnnn",
			IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T00:05:00Z", Action: "signin",
		})
		fmt.Fprintf(w, `{"request":%q}`, enc)
	}))
	defer srv.Close()

	audience = srv.URL
	enc, req, err := FetchSignInRequest(context.Background(), srv.Client(), srv.URL+"/r/K7QM4XP2")
	if err != nil || req.Audience != srv.URL || enc == "" {
		t.Fatalf("fetch = %q %+v %v", enc, req, err)
	}
	// A link that serves a request for somebody else is refused.
	audience = "https://bank.example"
	if _, _, err := FetchSignInRequest(context.Background(), srv.Client(), srv.URL+"/r/K7QM4XP2"); !errors.Is(err, ErrSignInMalformed) {
		t.Fatalf("foreign audience = %v", err)
	}
	audience = srv.URL
	if _, _, err := FetchSignInRequest(context.Background(), srv.Client(), srv.URL+"/r/GONE"); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("gone = %v", err)
	}
	if _, _, err := FetchSignInRequest(context.Background(), srv.Client(), srv.URL+"/r/JUNK"); !errors.Is(err, ErrSignInMalformed) {
		t.Fatalf("junk = %v", err)
	}
}
