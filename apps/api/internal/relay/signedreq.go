package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/poweur/api/internal/storage"
)

// Signed requests: one round trip instead of two. Rather than fetching a
// one-shot challenge (GET /auth/challenge) and signing it, a client signs the
// request itself and sends X-Poweur-Timestamp and X-Poweur-Nonce with the
// usual X-Poweur-Identity / X-Poweur-Signature (and X-Poweur-Session-Id):
//
//	poweur-request/v1\n<identity>\n<METHOD>\n<request URI>\n<unix seconds>\n<nonce>\n<hex SHA-256 of the body>
//
// The relay accepts it within requestSkew of its own clock and remembers the
// nonce until the window closes, so a captured request cannot be replayed on
// this relay. The signature covers the method, path, query and body, so a
// request cannot be altered either. Challenge-based callers keep working.
const (
	signedRequestPrefix = "poweur-request/v1"
	requestSkew         = 60 * time.Second
	// maxSignedBody bounds what the relay buffers to hash a signed request;
	// chunk uploads (4 MiB) and commits fit well within it.
	maxSignedBody = 32 << 20
)

type bodyHashKey struct{}

// hashSignedBodies buffers and hashes the body of every signed request, so
// handlers can read it as usual and authentication can check the signature.
func hashSignedBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Poweur-Timestamp") == "" {
			next.ServeHTTP(w, r)
			return
		}
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignedBody))
			if err != nil {
				writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "signed request body too large")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		sum := sha256.Sum256(body)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bodyHashKey{}, hex.EncodeToString(sum[:]))))
	})
}

// signedRequestString is what the client signed.
func signedRequestString(identity string, r *http.Request, timestamp, nonce, bodyHash string) string {
	return signedRequestPrefix + "\n" + identity + "\n" + r.Method + "\n" + r.URL.RequestURI() + "\n" + timestamp + "\n" + nonce + "\n" + bodyHash
}

// requestNonces remembers nonces of accepted signed requests until they can
// no longer be replayed.
type requestNonces struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (n *requestNonces) use(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]time.Time{}
	}
	if until, ok := n.seen[key]; ok && now.Before(until) {
		return false
	}
	if len(n.seen) > 4096 {
		for k, until := range n.seen {
			if !now.Before(until) {
				delete(n.seen, k)
			}
		}
	}
	n.seen[key] = now.Add(2 * requestSkew)
	return true
}

// signedRequest returns the string a signed request's signature must cover,
// or false when the request is stale, replayed or malformed.
func (s *Server) signedRequest(identity string, r *http.Request) (storage.Challenge, bool) {
	timestamp, nonce := r.Header.Get("X-Poweur-Timestamp"), r.Header.Get("X-Poweur-Nonce")
	bodyHash, _ := r.Context().Value(bodyHashKey{}).(string)
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || bodyHash == "" || len(nonce) < 16 || len(nonce) > 64 {
		return storage.Challenge{}, false
	}
	now := time.Now()
	at := time.Unix(seconds, 0)
	if at.Before(now.Add(-requestSkew)) || at.After(now.Add(requestSkew)) {
		return storage.Challenge{}, false
	}
	if !s.requestNonces.use(identity+"\n"+nonce, now) {
		return storage.Challenge{}, false
	}
	return storage.Challenge{Value: signedRequestString(identity, r, timestamp, nonce, bodyHash), ExpiresAt: at.Add(requestSkew)}, true
}
