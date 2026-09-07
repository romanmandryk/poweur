package relay

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	"golang.org/x/crypto/curve25519"
)

// New-device enrollment ceremony (EPIC-011 E11-T3).
//
// The security claim under test: the short code authenticates a *public* key,
// so there is nothing for the relay or an eavesdropper to brute-force. What
// must hold is that the payload is sealed to the new device's ephemeral key,
// that only the identity owner can deliver it, and that a rendezvous is
// strictly single-use.

func newEphemeralKey(t *testing.T) (pub string, priv []byte) {
	t.Helper()
	priv = make([]byte, 32)
	for i := range priv {
		priv[i] = byte(i + 1)
	}
	pubBytes, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(pubBytes), priv
}

func (f *keystoreFixture) offer(t *testing.T, ephemeralPub string) EnrollOfferResponse {
	t.Helper()
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer",
		EnrollOfferRequest{EphemeralPublicKey: ephemeralPub, Label: "Firefox on Linux"})
	if code != http.StatusCreated {
		t.Fatalf("offer: %d %s", code, body)
	}
	var out EnrollOfferResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *keystoreFixture) signEnroll(t *testing.T, action, rid string) (string, string, string) {
	t.Helper()
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + action + rid
	canonical := crypto.CanonicalEnrollAction(action, f.identity, rid, issuedAt, nonce)
	return issuedAt, nonce, b64(ed25519.Sign(f.priv, []byte(canonical)))
}

// Both devices must derive the same code from the ephemeral key alone — the
// relay is never trusted to report it.
func TestEnroll_SASIsDerivedFromTheKey(t *testing.T) {
	f := newKeystoreFixture(t)
	pub, _ := newEphemeralKey(t)
	offer := f.offer(t, pub)

	if got := ComputeSAS(pub); got != offer.SAS {
		t.Fatalf("relay SAS %q != locally computed %q", offer.SAS, got)
	}
	if len(offer.SAS) != sasDigits {
		t.Fatalf("want %d digits, got %q", sasDigits, offer.SAS)
	}
	// A different key must produce a different code, or comparison is useless.
	other, _ := newEphemeralKey(t)
	if other != pub && ComputeSAS(other) == offer.SAS {
		t.Fatal("distinct keys produced the same SAS")
	}
}

func TestEnroll_FullCeremony(t *testing.T) {
	f := newKeystoreFixture(t)
	pub, _ := newEphemeralKey(t)
	offer := f.offer(t, pub)

	// The approving device sees what it is approving, and can check the code.
	issuedAt, nonce, sig := f.signEnroll(t, "enroll-fetch", offer.RendezvousID)
	code, body := f.do(t, http.MethodPost,
		"/identities/"+f.identity+"/enroll/"+offer.RendezvousID+"/fetch",
		EnrollFetchRequest{IssuedAt: issuedAt, Nonce: nonce, IdentitySignature: sig})
	if code != http.StatusOK {
		t.Fatalf("fetch: %d %s", code, body)
	}
	var fetched EnrollFetchResponse
	if err := json.Unmarshal(body, &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.EphemeralPublicKey != pub || fetched.SAS != offer.SAS {
		t.Fatalf("fetch returned a different offer: %+v", fetched)
	}
	if fetched.Label != "Firefox on Linux" {
		t.Fatalf("label lost: %q", fetched.Label)
	}

	// Nothing is available until the owner delivers.
	code, body = f.do(t, http.MethodGet, "/identities/"+f.identity+"/enroll/"+offer.RendezvousID, nil)
	if code != http.StatusOK {
		t.Fatalf("claim before delivery: %d %s", code, body)
	}
	var claim EnrollClaimResponse
	if err := json.Unmarshal(body, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Ready || claim.Sealed != "" {
		t.Fatal("payload available before the owner approved")
	}

	// Deliver, then claim.
	issuedAt, nonce, sig = f.signEnroll(t, "enroll-deliver", offer.RendezvousID)
	if code, body = f.do(t, http.MethodPost,
		"/identities/"+f.identity+"/enroll/"+offer.RendezvousID+"/deliver",
		EnrollDeliverRequest{
			Sealed: "c2VhbGVkLXNlZWQ", IssuedAt: issuedAt, Nonce: nonce, IdentitySignature: sig,
		}); code != http.StatusNoContent {
		t.Fatalf("deliver: %d %s", code, body)
	}

	code, body = f.do(t, http.MethodGet, "/identities/"+f.identity+"/enroll/"+offer.RendezvousID, nil)
	if code != http.StatusOK {
		t.Fatalf("claim: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &claim); err != nil {
		t.Fatal(err)
	}
	if !claim.Ready || claim.Sealed != "c2VhbGVkLXNlZWQ" {
		t.Fatalf("unexpected claim: %+v", claim)
	}

	// Single use: the entry is gone, so a captured id cannot be replayed.
	if code, _ = f.do(t, http.MethodGet, "/identities/"+f.identity+"/enroll/"+offer.RendezvousID, nil); code != http.StatusNotFound {
		t.Fatalf("rendezvous survived its claim: %d", code)
	}
}

// Only the identity owner may approve — an attacker who watched the code
// cannot deliver a seed of their choosing.
func TestEnroll_DeliverRequiresIdentityKey(t *testing.T) {
	f := newKeystoreFixture(t)
	pub, _ := newEphemeralKey(t)
	offer := f.offer(t, pub)

	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	code, _ := f.do(t, http.MethodPost,
		"/identities/"+f.identity+"/enroll/"+offer.RendezvousID+"/deliver",
		EnrollDeliverRequest{
			Sealed: "eA", IssuedAt: issuedAt, Nonce: "n",
			IdentitySignature: b64(ed25519.Sign(wrongPriv, []byte("nope"))),
		})
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

// An approval is bound to one rendezvous, so it cannot be redirected to
// another offer the attacker controls.
func TestEnroll_ApprovalIsBoundToItsRendezvous(t *testing.T) {
	f := newKeystoreFixture(t)
	pubA, _ := newEphemeralKey(t)
	offerA := f.offer(t, pubA)
	offerB := f.offer(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	issuedAt, nonce, sig := f.signEnroll(t, "enroll-deliver", offerA.RendezvousID)
	code, _ := f.do(t, http.MethodPost,
		"/identities/"+f.identity+"/enroll/"+offerB.RendezvousID+"/deliver",
		EnrollDeliverRequest{Sealed: "eA", IssuedAt: issuedAt, Nonce: nonce, IdentitySignature: sig})
	if code != http.StatusUnauthorized {
		t.Fatalf("signature for another rendezvous was accepted: %d", code)
	}
}

func TestEnroll_RejectsMalformedEphemeralKey(t *testing.T) {
	f := newKeystoreFixture(t)
	for _, bad := range []string{"", "not-base64!!", b64([]byte("too-short"))} {
		code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer",
			EnrollOfferRequest{EphemeralPublicKey: bad})
		if code != http.StatusBadRequest {
			t.Fatalf("key %q should be rejected, got %d", bad, code)
		}
	}
}

// The offer endpoint is necessarily unauthenticated — the new device has no
// key yet — so it is capped to stop a stranger flooding a public identity.
func TestEnroll_CapsOpenOffers(t *testing.T) {
	f := newKeystoreFixture(t)
	pub, _ := newEphemeralKey(t)
	for i := 0; i < storage.MaxOpenPerIdentity; i++ {
		f.offer(t, pub)
	}
	code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/enroll/offer",
		EnrollOfferRequest{EphemeralPublicKey: pub})
	if code != http.StatusTooManyRequests {
		t.Fatalf("want 429 past the cap, got %d", code)
	}
}

func TestEnroll_DeliverTwiceIsRefused(t *testing.T) {
	f := newKeystoreFixture(t)
	pub, _ := newEphemeralKey(t)
	offer := f.offer(t, pub)
	deliver := func() int {
		issuedAt, nonce, sig := f.signEnroll(t, "enroll-deliver", offer.RendezvousID)
		code, _ := f.do(t, http.MethodPost,
			"/identities/"+f.identity+"/enroll/"+offer.RendezvousID+"/deliver",
			EnrollDeliverRequest{Sealed: "eA", IssuedAt: issuedAt, Nonce: nonce, IdentitySignature: sig})
		return code
	}
	if code := deliver(); code != http.StatusNoContent {
		t.Fatalf("first deliver: %d", code)
	}
	if code := deliver(); code != http.StatusConflict {
		t.Fatalf("second deliver must conflict, got %d", code)
	}
}
