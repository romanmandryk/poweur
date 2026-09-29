package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// signedHeaders signs a request the one-round-trip way.
func signedHeaders(id hostedID, method, uri string, body []byte, at time.Time, nonce string) map[string]string {
	sum := sha256.Sum256(body)
	ts := fmt.Sprint(at.Unix())
	msg := signedRequestPrefix + "\n" + id.name + "\n" + method + "\n" + uri + "\n" + ts + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
	return map[string]string{
		"X-Poweur-Identity":  id.name,
		"X-Poweur-Timestamp": ts,
		"X-Poweur-Nonce":     nonce,
		"X-Poweur-Signature": base64.StdEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(msg))),
	}
}

func TestSignedRequestsNeedNoChallenge(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "signedalice.poweur.net")
	uri := "/drive/" + alice.name
	status := func(method, path string, body []byte, hdr map[string]string) int {
		resp := httpReq(t, ts, method, path, "", body, hdr)
		resp.Body.Close()
		return resp.StatusCode
	}
	now := time.Now()
	ok := signedHeaders(alice, http.MethodGet, uri, nil, now, randHex(16))
	if got := status(http.MethodGet, uri, nil, ok); got != http.StatusOK {
		t.Fatalf("signed request: %d", got)
	}
	if got := status(http.MethodGet, uri, nil, ok); got != http.StatusUnauthorized {
		t.Fatalf("replayed signed request: %d", got)
	}
	if got := status(http.MethodGet, uri, nil, signedHeaders(alice, http.MethodGet, uri, nil, now.Add(-5*time.Minute), randHex(16))); got != http.StatusUnauthorized {
		t.Fatalf("stale signed request: %d", got)
	}
	if got := status(http.MethodGet, uri+"?x=1", nil, signedHeaders(alice, http.MethodGet, uri, nil, now, randHex(16))); got != http.StatusUnauthorized {
		t.Fatalf("request for another URI: %d", got)
	}
	// The body is covered: a commit signed for one body is refused with another.
	body := []byte(`{"id":"` + randHex(16) + `"}`)
	hdr := signedHeaders(alice, http.MethodPost, uri+"/commit", body, now, randHex(16))
	if got := status(http.MethodPost, uri+"/commit", bytes.Replace(body, []byte(`"id"`), []byte(`"ID"`), 1), hdr); got != http.StatusUnauthorized {
		t.Fatalf("tampered body: %d", got)
	}
	// Another identity's key cannot sign for alice.
	mallory := registerTestIdentity(t, server, ts, "signedmallory.poweur.net")
	forged := signedHeaders(hostedID{name: alice.name, priv: mallory.priv}, http.MethodGet, uri, nil, now, randHex(16))
	if got := status(http.MethodGet, uri, nil, forged); got != http.StatusUnauthorized {
		t.Fatalf("forged signature: %d", got)
	}
}
