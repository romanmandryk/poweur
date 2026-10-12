package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A.
const (
	rfcPlaintext = "When I grow up, I want to be a watermelon"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	out, err := decodeB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rfcSubscription() Subscription {
	var sub Subscription
	sub.Endpoint = "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV"
	sub.Keys.P256dh = rfcUAPublic
	sub.Keys.Auth = rfcAuth
	return sub
}

func TestEncryptMatchesRFC8291Vector(t *testing.T) {
	asPrivate, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncryptWith(rfcSubscription(), []byte(rfcPlaintext), mustB64(t, rfcSalt), asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(body); got != rfcBody {
		t.Fatalf("body mismatch\n got %s\nwant %s", got, rfcBody)
	}
}

func TestDecryptRoundTrip(t *testing.T) {
	uaPrivate, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(mustB64(t, rfcBody), uaPrivate, mustB64(t, rfcAuth))
	if err != nil || string(plain) != rfcPlaintext {
		t.Fatalf("rfc body: %q %v", plain, err)
	}
	body, err := Encrypt(rfcSubscription(), []byte(`{"count":3}`))
	if err != nil {
		t.Fatal(err)
	}
	plain, err = Decrypt(body, uaPrivate, mustB64(t, rfcAuth))
	if err != nil || string(plain) != `{"count":3}` {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	// Wrong auth secret must not open it.
	if _, err := Decrypt(body, uaPrivate, make([]byte, 16)); err == nil {
		t.Fatal("decrypted with the wrong auth secret")
	}
}

func TestEncryptRejectsOversizedPayload(t *testing.T) {
	if _, err := Encrypt(rfcSubscription(), make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("expected size error")
	}
	body, err := Encrypt(rfcSubscription(), make([]byte, MaxPayload))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != recordSize {
		t.Fatalf("max payload body is %d bytes, want %d", len(body), recordSize)
	}
}

func TestSubscriptionValidate(t *testing.T) {
	good := rfcSubscription()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Subscription){
		"relative endpoint": func(s *Subscription) { s.Endpoint = "/push" },
		"bad p256dh":        func(s *Subscription) { s.Keys.P256dh = "AAAA" },
		"short auth":        func(s *Subscription) { s.Keys.Auth = "AAAA" },
		"not base64":        func(s *Subscription) { s.Keys.Auth = "!!!" },
		"long endpoint":     func(s *Subscription) { s.Endpoint = "https://x/" + strings.Repeat("a", 1100) },
	}
	for name, mutate := range cases {
		sub := rfcSubscription()
		mutate(&sub)
		if err := sub.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVAPIDFromKey(t *testing.T) {
	key, err := GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	v, err := VAPIDFromKey(key, "mailto:ops@example.net")
	if err != nil {
		t.Fatal(err)
	}
	if pub := mustB64(t, v.PublicKey()); len(pub) != 65 || pub[0] != 4 {
		t.Fatalf("public key shape: %d bytes", len(pub))
	}
	if _, err := VAPIDFromKey(key, "ops@example.net"); err == nil {
		t.Fatal("accepted a bare email subject")
	}
	if _, err := VAPIDFromKey("not-a-key", "mailto:x@y"); err == nil {
		t.Fatal("accepted garbage key")
	}
}

// verifyVAPID checks an Authorization header the way a push service does.
func verifyVAPID(t *testing.T, header, wantAud string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("authorization %q", header)
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)
	if len(parts) != 2 {
		t.Fatalf("authorization %q", header)
	}
	jwt, key := parts[0], parts[1]
	pub := mustB64(t, key)
	pk := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:])}
	segs := strings.Split(jwt, ".")
	if len(segs) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	digest := sha256.Sum256([]byte(segs[0] + "." + segs[1]))
	sig := mustB64(t, segs[2])
	if !ecdsa.Verify(pk, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature does not verify")
	}
	var claims map[string]any
	if err := json.Unmarshal(mustB64(t, segs[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != wantAud {
		t.Fatalf("aud %v, want %s", claims["aud"], wantAud)
	}
	return claims
}

func TestSendPostsEncryptedBodyWithVAPID(t *testing.T) {
	uaPrivate, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)

	var gotBody []byte
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	var sub Subscription
	sub.Endpoint = srv.URL + "/push/abc"
	sub.Keys.P256dh = b64.EncodeToString(uaPrivate.PublicKey().Bytes())
	sub.Keys.Auth = b64.EncodeToString(auth)

	key, _ := GenerateVAPIDKey()
	vapid, _ := VAPIDFromKey(key, "https://relay.example.net")
	now := time.Unix(1_800_000_000, 0)
	sender := &Sender{VAPID: vapid, Client: srv.Client(), Now: func() time.Time { return now }}
	err := sender.Send(context.Background(), sub, []byte(`{"kind":"message"}`), Options{
		TTL: time.Hour, Urgency: UrgencyHigh, Topic: Topic("alice.poweur.net"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeaders.Get("Content-Encoding") != "aes128gcm" || gotHeaders.Get("TTL") != "3600" ||
		gotHeaders.Get("Urgency") != "high" || len(gotHeaders.Get("Topic")) != 32 {
		t.Fatalf("headers %v", gotHeaders)
	}
	claims := verifyVAPID(t, gotHeaders.Get("Authorization"), srv.URL)
	if claims["sub"] != "https://relay.example.net" || claims["exp"].(float64) != float64(now.Add(12*time.Hour).Unix()) {
		t.Fatalf("claims %v", claims)
	}
	plain, err := Decrypt(gotBody, uaPrivate, auth)
	if err != nil || string(plain) != `{"kind":"message"}` {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
}

func TestSendMapsStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		gone   bool
	}{{404, true}, {410, true}, {413, false}, {429, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		sub := rfcSubscription()
		sub.Endpoint = srv.URL
		key, _ := GenerateVAPIDKey()
		vapid, _ := VAPIDFromKey(key, "mailto:a@b.c")
		err := (&Sender{VAPID: vapid, Client: srv.Client()}).Send(context.Background(), sub, []byte("x"), Options{})
		srv.Close()
		if tc.gone != errors.Is(err, ErrGone) {
			t.Errorf("%d: err %v", tc.status, err)
		}
		var se *StatusError
		if !tc.gone && (!errors.As(err, &se) || se.Status != tc.status) {
			t.Errorf("%d: want StatusError, got %v", tc.status, err)
		}
	}
}
