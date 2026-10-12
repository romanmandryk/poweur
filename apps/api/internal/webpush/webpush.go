// Package webpush sends Web Push messages (EPIC-009 E09-T11): RFC 8291
// message encryption, RFC 8188 aes128gcm content coding and RFC 8292 VAPID.
//
// Standard library crypto plus x/crypto's HKDF, which the relay already
// depends on — a push library would be the relay's first dependency for
// about two hundred lines of well-specified code.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// recordSize is the single record's rs. Push services cap the body at 4096
// bytes, so one record always holds the whole message.
const recordSize = 4096

// MaxPayload is the largest plaintext that fits in one 4096-byte body:
// 4096 − 86-byte header − 16-byte tag − 1 delimiter.
const MaxPayload = 3993

var b64 = base64.RawURLEncoding

// decodeB64 accepts base64url with or without padding, and standard
// base64 — browsers hand subscription keys over as base64url, but a
// hand-written config may not.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.TrimRight(s, "="))
	if out, err := b64.DecodeString(s); err == nil {
		return out, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Subscription is what PushManager.subscribe() returns, as the browser
// serializes it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Validate checks the keys decode to a P-256 point and a 16-byte secret.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("endpoint must be an absolute URL")
	}
	if len(s.Endpoint) > 1024 {
		return errors.New("endpoint is too long")
	}
	pub, err := decodeB64(s.Keys.P256dh)
	if err != nil {
		return errors.New("keys.p256dh is not base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errors.New("keys.p256dh is not a P-256 public key")
	}
	auth, err := decodeB64(s.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("keys.auth must be 16 bytes of base64url")
	}
	return nil
}

// Encrypt seals plaintext for one subscription (RFC 8291 §3, RFC 8188 §2).
// salt and the application server's ephemeral key are random; EncryptWith
// takes them explicitly for the RFC's test vector.
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return EncryptWith(sub, plaintext, salt, ephemeral)
}

// EncryptWith is Encrypt with a caller-chosen salt and ephemeral key.
func EncryptWith(sub Subscription, plaintext, salt []byte, asPrivate *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("payload is %d bytes; at most %d fit", len(plaintext), MaxPayload)
	}
	if len(salt) != 16 {
		return nil, errors.New("salt must be 16 bytes")
	}
	uaRaw, err := decodeB64(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	uaPublic, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := decodeB64(sub.Keys.Auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("auth must be 16 bytes")
	}
	shared, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()

	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0 || ua_public || as_public, 32)
	keyInfo := append([]byte("WebPush: info\x00"), uaRaw...)
	keyInfo = append(keyInfo, asPublic...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, authSecret, keyInfo), ikm); err != nil {
		return nil, err
	}
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record, so it is the last one: delimiter 0x02, no padding.
	record := append(append([]byte{}, plaintext...), 0x02)

	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return gcm.Seal(header, nonce, record, nil), nil
}

// Decrypt opens a body sealed by Encrypt, as the user agent would. The relay
// never needs it; tests and the fake push service in apps/integration do.
func Decrypt(body []byte, uaPrivate *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("body too short")
	}
	salt := body[:16]
	idLen := int(body[20])
	if len(body) < 21+idLen {
		return nil, errors.New("body too short")
	}
	asPublic, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		return nil, err
	}
	shared, err := uaPrivate.ECDH(asPublic)
	if err != nil {
		return nil, err
	}
	keyInfo := append([]byte("WebPush: info\x00"), uaPrivate.PublicKey().Bytes()...)
	keyInfo = append(keyInfo, asPublic.Bytes()...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, authSecret, keyInfo), ikm); err != nil {
		return nil, err
	}
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		return nil, err
	}
	// Strip padding back to the delimiter.
	end := bytes.LastIndexAny(plain, "\x01\x02")
	if end < 0 {
		return nil, errors.New("no record delimiter")
	}
	return plain[:end], nil
}

// VAPID is the application server's identity toward push services
// (RFC 8292). Apple and Mozilla require it; Chrome refuses a push whose key
// differs from the one the subscription was made with.
type VAPID struct {
	key     *ecdsa.PrivateKey
	Subject string
}

// GenerateVAPIDKey returns a new private key as base64url (the raw 32-byte
// scalar) — the format VAPIDFromKey reads and the relay's
// PUSH_VAPID_PRIVATE_KEY holds.
func GenerateVAPIDKey() (string, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(key.Bytes()), nil
}

// VAPIDFromKey parses a base64url raw P-256 private scalar.
func VAPIDFromKey(privateB64, subject string) (*VAPID, error) {
	raw, err := decodeB64(privateB64)
	if err != nil {
		return nil, errors.New("VAPID key is not base64url")
	}
	ecdhKey, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID key: %w", err)
	}
	pub := ecdhKey.PublicKey().Bytes() // 0x04 || X || Y
	key := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(pub[1:33]),
			Y:     new(big.Int).SetBytes(pub[33:]),
		},
		D: new(big.Int).SetBytes(raw),
	}
	if !strings.HasPrefix(subject, "mailto:") && !strings.HasPrefix(subject, "https://") {
		return nil, errors.New("VAPID subject must be a mailto: or https: URL")
	}
	return &VAPID{key: key, Subject: subject}, nil
}

// PublicKey is the uncompressed point, base64url — the browser's
// applicationServerKey.
func (v *VAPID) PublicKey() string {
	pub := make([]byte, 65)
	pub[0] = 4
	v.key.X.FillBytes(pub[1:33])
	v.key.Y.FillBytes(pub[33:])
	return b64.EncodeToString(pub)
}

// header builds `Authorization: vapid t=<jwt>, k=<key>` for an endpoint.
func (v *VAPID) header(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		// RFC 8292 allows up to 24h; Apple rejects anything over an hour
		// in the future, so stay well under it.
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": v.Subject,
	})
	if err != nil {
		return "", err
	}
	signing := head + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + v.PublicKey(), nil
}

// Urgency is RFC 8030 §5.3.
type Urgency string

const (
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Options shape one push.
type Options struct {
	// TTL is how long the push service may hold the message for an offline
	// device. Zero means deliver now or drop.
	TTL     time.Duration
	Urgency Urgency
	// Topic lets a newer push replace an undelivered older one (≤ 32
	// base64url characters).
	Topic string
}

// ErrGone means the push service no longer knows the subscription (404 or
// 410): the user unsubscribed, reinstalled, or the browser expired it.
// Callers delete the subscription.
var ErrGone = errors.New("push subscription is gone")

// StatusError is any other refusal from the push service.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("push service answered %d: %s", e.Status, e.Body)
}

// Sender delivers encrypted pushes.
type Sender struct {
	VAPID  *VAPID
	Client *http.Client
	Now    func() time.Time
}

// Send encrypts payload for sub and posts it to the push service.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	auth, err := s.VAPID.header(sub.Endpoint, now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Authorization", auth)
	req.Header.Set("TTL", strconv.Itoa(int(opts.TTL/time.Second)))
	if opts.Urgency != "" {
		req.Header.Set("Urgency", string(opts.Urgency))
	}
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}
}

// Topic derives a stable RFC 8030 topic from any key (an identity): 32
// base64url characters, so a burst for one identity collapses to one
// undelivered message at the push service.
func Topic(key string) string {
	sum := sha256.Sum256([]byte("poweur-push-topic\n" + strings.ToLower(key)))
	return b64.EncodeToString(sum[:24])
}
