package bridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
)

// Relay is the bridge identity's view of the Poweur network: send as itself,
// pick up its own inbox, and listen for new messages. Everything goes
// through the bridge's home relay over the public protocol, authenticated
// with the identity key — the bridge holds no relay credential.
type Relay struct {
	base     string
	identity string
	priv     ed25519.PrivateKey
	encPub   []byte
	encPriv  []byte
	http     *http.Client
	resolve  identity.ResolveOptions

	mu    sync.Mutex
	cache map[string]cachedKey
}

type cachedKey struct {
	key []byte
	at  time.Time
}

// Envelope is a message on the wire.
type Envelope struct {
	ID         string                       `json:"id"`
	Sender     string                       `json:"sender"`
	Recipient  string                       `json:"recipient"`
	Timestamp  string                       `json:"timestamp"`
	Payload    string                       `json:"payload"`
	Signature  string                       `json:"signature"`
	Type       string                       `json:"type,omitempty"`
	ThreadID   string                       `json:"thread_id,omitempty"`
	ExpiresAt  string                       `json:"expires_at,omitempty"`
	Metadata   map[string]string            `json:"metadata,omitempty"`
	SessionID  string                       `json:"session_id,omitempty"`
	Encryption *identity.EnvelopeEncryption `json:"encryption,omitempty"`
}

// NewRelay builds the client.
func NewRelay(base, id string, priv ed25519.PrivateKey, encPub, encPriv []byte, resolve identity.ResolveOptions) *Relay {
	return &Relay{
		base: strings.TrimRight(base, "/"), identity: id, priv: priv, encPub: encPub, encPriv: encPriv,
		http: &http.Client{Timeout: 30 * time.Second}, resolve: resolve, cache: map[string]cachedKey{},
	}
}

// RelayError is a relay's refusal; Status tells permanent (4xx) from
// retryable (5xx, transport) failures.
type RelayError struct {
	Status int
	Code   string
	Detail string
}

func (e *RelayError) Error() string {
	return fmt.Sprintf("relay %d %s: %s", e.Status, e.Code, e.Detail)
}

// Permanent reports a refusal that will not change on retry.
func (e *RelayError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusRequestTimeout
}

// IsPermanent reports whether err is a permanent relay refusal.
func IsPermanent(err error) bool {
	var re *RelayError
	return errors.As(err, &re) && re.Permanent()
}

func relayError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var parsed struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Detail == "" {
		parsed.Detail = strings.TrimSpace(string(body))
	}
	return &RelayError{Status: resp.StatusCode, Code: parsed.Error, Detail: parsed.Detail}
}

// sign adds the one-round-trip signed-request headers (relay signedreq.go).
func (r *Relay) sign(req *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	nonce := make([]byte, 18)
	_, _ = rand.Read(nonce)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	n := base64.RawURLEncoding.EncodeToString(nonce)
	canonical := "poweur-request/v1\n" + r.identity + "\n" + req.Method + "\n" + req.URL.RequestURI() + "\n" + ts + "\n" + n + "\n" + hex.EncodeToString(sum[:])
	req.Header.Set("X-Poweur-Identity", r.identity)
	req.Header.Set("X-Poweur-Timestamp", ts)
	req.Header.Set("X-Poweur-Nonce", n)
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, []byte(canonical))))
	req.Header.Set("X-Poweur-Device-Name", "poweur-email "+Version)
	req.Header.Set("X-Poweur-Device-Kind", "service")
}

func (r *Relay) do(ctx context.Context, method, path string, body []byte, signed bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed {
		r.sign(req, body)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return relayError(resp)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
	}
	return nil
}

// EncryptionKey resolves an identity's X25519 key (cached briefly).
func (r *Relay) EncryptionKey(ctx context.Context, id string) ([]byte, error) {
	id = strings.ToLower(id)
	r.mu.Lock()
	if c, ok := r.cache[id]; ok && time.Since(c.at) < 5*time.Minute {
		r.mu.Unlock()
		return c.key, nil
	}
	r.mu.Unlock()
	res, err := identity.Resolve(ctx, id, r.resolve)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimPrefix(res.Document.EncryptionPublicKey, "x25519:")
	key, err := decodeB64(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s has no usable encryption key", id)
	}
	r.mu.Lock()
	r.cache[id] = cachedKey{key: key, at: time.Now()}
	r.mu.Unlock()
	return key, nil
}

func decodeB64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// Send encrypts plaintext to `to` and posts it as the bridge, through the
// bridge's home relay (which delivers or forwards). It returns the message id.
func (r *Relay) Send(ctx context.Context, to, msgType string, plaintext []byte, id string) (string, error) {
	key, err := r.EncryptionKey(ctx, to)
	if err != nil {
		return "", err
	}
	sealed, err := identity.Seal(key, plaintext, identity.MessageSealDomain, nil)
	if err != nil {
		return "", err
	}
	if id == "" {
		id = newMessageID()
	}
	env := Envelope{
		ID: id, Sender: r.identity, Recipient: strings.ToLower(to),
		Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: sealed.Ciphertext, Type: msgType,
		Encryption: &identity.EnvelopeEncryption{Alg: identity.SealAlgorithm, EphemeralPublicKey: sealed.EphemeralPublicKey, Nonce: sealed.Nonce},
	}
	canonical := identity.CanonicalMessageEnvelope(env.Sender, env.Recipient, env.Timestamp, env.Payload, env.ID, "",
		env.Type, env.ThreadID, env.ExpiresAt, env.Metadata, env.Encryption)
	env.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, []byte(canonical)))
	body, _ := json.Marshal(env)
	if err := r.do(ctx, http.MethodPost, "/messages", body, false, nil); err != nil {
		return "", err
	}
	return id, nil
}

// newMessageID mints an id in the shape the CLI uses.
func newMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "msg_" + hex.EncodeToString(b)
}

// Pickup reads the bridge's inbox from cursor without forgetting it.
func (r *Relay) Pickup(ctx context.Context, cursor string) ([]Envelope, string, error) {
	var out struct {
		Messages []Envelope `json:"messages"`
		Cursor   string     `json:"cursor"`
	}
	path := "/messages/" + url.PathEscape(r.identity) + "?since=" + url.QueryEscape(cursor)
	if err := r.do(ctx, http.MethodGet, path, nil, true, &out); err != nil {
		return nil, cursor, err
	}
	return out.Messages, out.Cursor, nil
}

// Consume forgets the inbox through cursor.
func (r *Relay) Consume(ctx context.Context, through string) error {
	if through == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"through": through})
	return r.do(ctx, http.MethodPost, "/messages/"+url.PathEscape(r.identity)+"/consume", body, true, nil)
}

// Open decrypts an envelope addressed to the bridge.
func (r *Relay) Open(env Envelope) ([]byte, error) {
	if env.Encryption == nil || env.Encryption.Alg != identity.SealAlgorithm {
		return nil, errors.New("not an encrypted message")
	}
	return identity.OpenSeal(r.encPriv, identity.SealedPayload{
		Ciphertext: env.Payload, EphemeralPublicKey: env.Encryption.EphemeralPublicKey, Nonce: env.Encryption.Nonce,
	}, identity.MessageSealDomain, nil)
}

// PutSystemFile writes one of the bridge identity's own system documents.
func (r *Relay) PutSystemFile(ctx context.Context, path string, body []byte) error {
	return r.do(ctx, http.MethodPut, "/identities/"+url.PathEscape(r.identity)+"/system/"+path, body, true, nil)
}

// Listen holds the event stream open and calls wake for every event (and
// once on connect). It returns when ctx ends or the stream drops.
func (r *Relay) Listen(ctx context.Context, wake func()) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/events/"+url.PathEscape(r.identity), nil)
	if err != nil {
		return err
	}
	r.sign(req, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return relayError(resp)
	}
	wake()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event:") {
			wake()
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return errors.New("event stream closed")
}
