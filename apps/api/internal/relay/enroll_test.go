package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	idpkg "github.com/poweur/identity"
	"golang.org/x/crypto/curve25519"
)

// New-device pairing, v2 (EPIC-011 E11-T8).
//
// What must hold, against a relay that is not trusted: the seed is sealed to
// a key the approver checked against a commitment made before its own nonce
// existed; only the identity key can deliver; only the new device can reveal
// or collect; steps happen once and in order.

type pairingDevice struct {
	pub, commitNonce, commitment string
	priv                         []byte
	code, token                  string
}

func newPairingDevice(t *testing.T) *pairingDevice {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pubBytes, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	d := &pairingDevice{pub: base64.RawURLEncoding.EncodeToString(pubBytes), priv: priv, commitNonce: randB64(t)}
	d.commitment = idpkg.PairingCommitment(d.pub, d.commitNonce)
	return d
}

func randB64(t *testing.T) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (f *keystoreFixture) doAuth(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, f.ts.URL+path, buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (f *keystoreFixture) offer(t *testing.T, d *pairingDevice) {
	t.Helper()
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer",
		EnrollOfferRequest{Commitment: d.commitment, Label: "Firefox on Linux"})
	if code != http.StatusCreated {
		t.Fatalf("offer: %d %s", code, body)
	}
	var out EnrollOfferResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := idpkg.NormalizeShortCode(out.RendezvousID); err != nil || len(out.RendezvousID) != idpkg.ShortCodeLen {
		t.Fatalf("code %q is not a short code", out.RendezvousID)
	}
	d.code, d.token = out.RendezvousID, out.ClaimToken
}

func (f *keystoreFixture) signEnroll(t *testing.T, action, rid string) (string, string, string) {
	t.Helper()
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := randB64(t)
	canonical := crypto.CanonicalEnrollAction(action, f.identity, rid, issuedAt, nonce)
	return issuedAt, nonce, b64(ed25519.Sign(f.priv, []byte(canonical)))
}

func (f *keystoreFixture) fetch(t *testing.T, rid, approverNonce string) (int, EnrollFetchResponse) {
	t.Helper()
	// Clients sign the canonical code, whatever the person typed.
	canonical, _ := idpkg.NormalizeShortCode(rid)
	at, n, sig := f.signEnroll(t, "enroll-fetch", canonical)
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+rid+"/fetch", EnrollFetchRequest{
		ApproverNonce: approverNonce, Mode: "compare", IssuedAt: at, Nonce: n, IdentitySignature: sig,
	})
	var out EnrollFetchResponse
	_ = json.Unmarshal(body, &out)
	return code, out
}

func (f *keystoreFixture) poll(t *testing.T, d *pairingDevice) (int, EnrollPollResponse) {
	t.Helper()
	code, body := f.doAuth(t, http.MethodGet, "/identities/"+f.identity+"/enroll/"+d.code, d.token, nil)
	var out EnrollPollResponse
	_ = json.Unmarshal(body, &out)
	return code, out
}

func (f *keystoreFixture) reveal(t *testing.T, d *pairingDevice, token string) int {
	t.Helper()
	code, _ := f.doAuth(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+d.code+"/reveal", token,
		EnrollRevealRequest{EphemeralPublicKey: d.pub, CommitNonce: d.commitNonce})
	return code
}

func (f *keystoreFixture) deliver(t *testing.T, rid, sealed string) int {
	t.Helper()
	at, n, sig := f.signEnroll(t, "enroll-deliver", rid)
	code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+rid+"/deliver",
		EnrollDeliverRequest{Sealed: sealed, IssuedAt: at, Nonce: n, IdentitySignature: sig})
	return code
}

func TestEnroll_FullCeremony(t *testing.T) {
	f := newKeystoreFixture(t)
	d := newPairingDevice(t)
	f.offer(t, d)

	if code, st := f.poll(t, d); code != 200 || st.State != "offered" {
		t.Fatalf("poll before approval = %d %+v", code, st)
	}
	// The approver sees only the commitment until the new device reveals.
	n := randB64(t)
	code, got := f.fetch(t, strings.ToLower(d.code[:4])+"-"+d.code[4:], n) // typed as a person would
	if code != 200 || got.Commitment != d.commitment || got.EphemeralPublicKey != "" || got.State != "nonce" || got.Label != "Firefox on Linux" {
		t.Fatalf("fetch = %d %+v", code, got)
	}
	_, st := f.poll(t, d)
	if st.State != "nonce" || st.ApproverNonce != n || st.Mode != "compare" {
		t.Fatalf("poll after approval = %+v", st)
	}
	if code := f.reveal(t, d, d.token); code != http.StatusNoContent {
		t.Fatalf("reveal = %d", code)
	}
	code, got = f.fetch(t, d.code, n)
	if code != 200 || got.State != "revealed" || got.EphemeralPublicKey != d.pub || got.CommitNonce != d.commitNonce {
		t.Fatalf("fetch after reveal = %d %+v", code, got)
	}
	if err := idpkg.VerifyPairingReveal(got.Commitment, got.EphemeralPublicKey, got.CommitNonce); err != nil {
		t.Fatal(err)
	}
	if code := f.deliver(t, d.code, `{"sealed":"blob"}`); code != http.StatusNoContent {
		t.Fatalf("deliver = %d", code)
	}
	if _, st := f.poll(t, d); st.State != "delivered" || !st.Ready || st.Sealed != `{"sealed":"blob"}` {
		t.Fatalf("collect = %+v", st)
	}
	// Single use: gone once collected.
	if code, _ := f.poll(t, d); code != http.StatusNotFound {
		t.Fatalf("second collect = %d", code)
	}
}

func TestEnroll_StepsHappenOnceAndInOrder(t *testing.T) {
	f := newKeystoreFixture(t)
	d := newPairingDevice(t)
	f.offer(t, d)

	// Revealing before the approver's nonce would let a relay see the key
	// before committing to a substitute.
	if code := f.reveal(t, d, d.token); code != http.StatusConflict {
		t.Fatalf("early reveal = %d", code)
	}
	n := randB64(t)
	f.fetch(t, d.code, n)
	if code := f.deliver(t, d.code, "x"); code != http.StatusConflict {
		t.Fatalf("deliver before reveal = %d", code)
	}
	// Another approver — or a replay with another nonce — cannot move the digits.
	if code, _ := f.fetch(t, d.code, randB64(t)); code != http.StatusConflict {
		t.Fatalf("second nonce = %d", code)
	}
	if code, _ := f.fetch(t, d.code, n); code != 200 {
		t.Fatalf("same nonce again = %d", code)
	}
	f.reveal(t, d, d.token)
	if code := f.reveal(t, d, d.token); code != http.StatusConflict {
		t.Fatalf("second reveal = %d", code)
	}
	f.deliver(t, d.code, "x")
	if code := f.deliver(t, d.code, "y"); code != http.StatusConflict {
		t.Fatalf("second deliver = %d", code)
	}
}

func TestEnroll_OnlyTheNewDeviceRevealsCollectsOrCancels(t *testing.T) {
	f := newKeystoreFixture(t)
	d := newPairingDevice(t)
	f.offer(t, d)
	f.fetch(t, d.code, randB64(t))

	for _, token := range []string{"", "wrong"} {
		if code := f.reveal(t, d, token); code != http.StatusUnauthorized {
			t.Errorf("reveal with %q = %d", token, code)
		}
		if code, _ := f.doAuth(t, http.MethodGet, "/identities/"+f.identity+"/enroll/"+d.code, token, nil); code != http.StatusUnauthorized {
			t.Errorf("poll with %q = %d", token, code)
		}
		if code, _ := f.doAuth(t, http.MethodDelete, "/identities/"+f.identity+"/enroll/"+d.code, token, nil); code != http.StatusNotFound {
			t.Errorf("cancel with %q = %d", token, code)
		}
	}
	// A reveal that does not open the commitment is refused.
	other := newPairingDevice(t)
	code, _ := f.doAuth(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+d.code+"/reveal", d.token,
		EnrollRevealRequest{EphemeralPublicKey: other.pub, CommitNonce: d.commitNonce})
	if code != http.StatusBadRequest {
		t.Fatalf("mismatched reveal = %d", code)
	}
	if code, _ := f.doAuth(t, http.MethodDelete, "/identities/"+f.identity+"/enroll/"+d.code, d.token, nil); code != http.StatusNoContent {
		t.Fatalf("cancel = %d", code)
	}
	if code, _ := f.poll(t, d); code != http.StatusNotFound {
		t.Fatalf("poll after cancel = %d", code)
	}
}

func TestEnroll_DeliverRequiresIdentityKeyAndItsRendezvous(t *testing.T) {
	f := newKeystoreFixture(t)
	a, b := newPairingDevice(t), newPairingDevice(t)
	f.offer(t, a)
	f.offer(t, b)
	for _, d := range []*pairingDevice{a, b} {
		n := randB64(t)
		f.fetch(t, d.code, n)
		f.reveal(t, d, d.token)
	}
	// A stranger's key cannot deliver.
	_, strangerPriv, _ := ed25519.GenerateKey(nil)
	at := time.Now().UTC().Format(time.RFC3339)
	nonce := randB64(t)
	sig := b64(ed25519.Sign(strangerPriv, []byte(crypto.CanonicalEnrollAction("enroll-deliver", f.identity, a.code, at, nonce))))
	if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+a.code+"/deliver",
		EnrollDeliverRequest{Sealed: "x", IssuedAt: at, Nonce: nonce, IdentitySignature: sig}); code != http.StatusUnauthorized {
		t.Fatalf("stranger deliver = %d", code)
	}
	// An approval signed for A does not land on B.
	at, nonce, sig = f.signEnroll(t, "enroll-deliver", a.code)
	if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/"+b.code+"/deliver",
		EnrollDeliverRequest{Sealed: "x", IssuedAt: at, Nonce: nonce, IdentitySignature: sig}); code != http.StatusUnauthorized {
		t.Fatalf("cross-rendezvous deliver = %d", code)
	}
}

func TestEnroll_RefusesV1AndMalformedOffers(t *testing.T) {
	f := newKeystoreFixture(t)
	pub := newPairingDevice(t).pub
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer", EnrollOfferRequest{EphemeralPublicKey: pub})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "pairing_v1") {
		t.Fatalf("v1 offer = %d %s", code, body)
	}
	for _, c := range []string{"", "short", pub + "x"} {
		if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer", EnrollOfferRequest{Commitment: c}); code != http.StatusBadRequest {
			t.Errorf("commitment %q = %d", c, code)
		}
	}
	d := newPairingDevice(t)
	f.offer(t, d)
	if code, _ := f.fetch(t, d.code, "short"); code != http.StatusBadRequest {
		t.Fatalf("short approver nonce = %d", code)
	}
}

func TestEnroll_CapsOpenOffers(t *testing.T) {
	f := newKeystoreFixture(t)
	for i := 0; i < 5; i++ {
		f.offer(t, newPairingDevice(t))
	}
	code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer",
		EnrollOfferRequest{Commitment: newPairingDevice(t).commitment})
	if code != http.StatusTooManyRequests {
		t.Fatalf("sixth offer = %d", code)
	}
}
