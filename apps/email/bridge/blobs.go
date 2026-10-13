package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
)

// Blobs are the bridge's only stored content, and none of it is readable by
// the bridge after it is written:
//
//   - blobs: attachments too large for the envelope and the original
//     message, sealed to the *recipient* (domain poweur/email/blob/v1) and
//     served at an unguessable URL until EMAIL_BLOB_TTL;
//   - uploads: outbound attachments a user sealed to the *bridge* (domain
//     poweur/email/upload/v1), kept a day or until the send that names them.
type Blobs struct {
	dir       string
	publicURL string
	ttl       time.Duration
	store     *Store
	now       func() time.Time

	mu     sync.Mutex
	nonces map[string]time.Time
}

const (
	uploadTTL        = 24 * time.Hour
	maxUploadQuota   = 100 << 20
	uploadSkew       = 5 * time.Minute
	maxUploadRequest = identity.MaxEmailBlobBytes*4/3 + 4096
)

var fileID = regexp.MustCompile(`^[A-Z0-9]{26}$`)

// NewBlobs prepares the blob directories.
func NewBlobs(dataDir, publicURL string, ttl time.Duration, store *Store) (*Blobs, error) {
	b := &Blobs{dir: dataDir, publicURL: strings.TrimRight(publicURL, "/"), ttl: ttl, store: store, now: time.Now, nonces: map[string]time.Time{}}
	for _, sub := range []string{"blobs", "uploads"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// Enabled reports whether blobs can be served (a public URL is configured).
func (b *Blobs) Enabled() bool { return b.publicURL != "" }

func newFileID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))[:26]
}

func (b *Blobs) path(kind, id string) string { return filepath.Join(b.dir, kind+"s", id+".json") }

// Put seals data to the recipient and stores it.
func (b *Blobs) Put(ctx context.Context, recipient string, encKey, data []byte) (identity.EmailBlobRef, error) {
	if !b.Enabled() {
		return identity.EmailBlobRef{}, errors.New("blobs need EMAIL_PUBLIC_URL")
	}
	id := newFileID()
	sealed, err := identity.Seal(encKey, data, identity.EmailBlobSealDomain, identity.EmailBlobContext(id))
	if err != nil {
		return identity.EmailBlobRef{}, err
	}
	raw, _ := json.Marshal(sealed)
	if err := os.WriteFile(b.path("blob", id), raw, 0o600); err != nil {
		return identity.EmailBlobRef{}, err
	}
	expires := b.now().Add(b.ttl)
	if err := b.store.AddFile(ctx, id, "blob", recipient, int64(len(raw)), expires); err != nil {
		_ = os.Remove(b.path("blob", id))
		return identity.EmailBlobRef{}, err
	}
	sum := sha256.Sum256(data)
	return identity.EmailBlobRef{
		ID: id, URL: b.publicURL + "/blobs/" + id, Size: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]), ExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

// ServeBlob answers GET /blobs/{id}. The id is the capability: 128 random
// bits, and the content is sealed to its recipient anyway.
func (b *Blobs) ServeBlob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !fileID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	f, ok, err := b.store.File(r.Context(), id)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if !ok || f.Kind != "blob" || b.now().After(f.Expires) {
		http.Error(w, "expired", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeFile(w, r, b.path("blob", id))
}

// ServeUpload answers PUT /v1/uploads/{id}: a user's outbound attachment,
// sealed to the bridge and signed by the user (identity.CanonicalEmailUpload).
func (b *Blobs) ServeUpload(dir *Directory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		if !fileID.MatchString(id) {
			http.Error(w, "invalid upload id", http.StatusBadRequest)
			return
		}
		owner := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Poweur-Identity")))
		issued, nonce, sig := r.Header.Get("X-Poweur-Timestamp"), r.Header.Get("X-Poweur-Nonce"), r.Header.Get("X-Poweur-Signature")
		at, err := time.Parse(time.RFC3339, issued)
		if err != nil || owner == "" || len(nonce) < 16 || len(nonce) > 64 || sig == "" || b.now().Sub(at).Abs() > uploadSkew {
			http.Error(w, "upload must be signed (X-Poweur-Identity, -Timestamp, -Nonce, -Signature)", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUploadRequest))
		if err != nil {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		sender, err := dir.Opted(r.Context(), owner)
		if err != nil {
			http.Error(w, "email is not enabled for "+owner+" through this bridge", http.StatusForbidden)
			return
		}
		sum := sha256.Sum256(body)
		canonical := identity.CanonicalEmailUpload(owner, id, hex.EncodeToString(sum[:]), int64(len(body)), issued, nonce)
		sigBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sig, "="))
		if err != nil || !ed25519.Verify(sender.PublicKey, []byte(canonical), sigBytes) {
			http.Error(w, "upload signature does not verify", http.StatusUnauthorized)
			return
		}
		if !b.useNonce(owner + "\n" + nonce) {
			http.Error(w, "replayed upload", http.StatusConflict)
			return
		}
		var sealed identity.SealedPayload
		if json.Unmarshal(body, &sealed) != nil || sealed.Ciphertext == "" {
			http.Error(w, "upload must be a sealed payload", http.StatusBadRequest)
			return
		}
		used, err := b.store.UploadBytes(r.Context(), owner)
		if err != nil {
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		if used+int64(len(body)) > maxUploadQuota {
			http.Error(w, fmt.Sprintf("upload quota exceeded (%d MB pending)", maxUploadQuota>>20), http.StatusTooManyRequests)
			return
		}
		if _, exists, _ := b.store.File(r.Context(), id); exists {
			http.Error(w, "upload id already used", http.StatusConflict)
			return
		}
		if err := os.WriteFile(b.path("upload", id), body, 0o600); err != nil {
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		if err := b.store.AddFile(r.Context(), id, "upload", owner, int64(len(body)), b.now().Add(uploadTTL)); err != nil {
			_ = os.Remove(b.path("upload", id))
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"upload":"` + id + `"}`))
	}
}

func (b *Blobs) useNonce(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for k, until := range b.nonces {
		if now.After(until) {
			delete(b.nonces, k)
		}
	}
	if _, seen := b.nonces[key]; seen {
		return false
	}
	b.nonces[key] = now.Add(2 * uploadSkew)
	return true
}

// TakeUpload opens an upload for owner's send and deletes it.
func (b *Blobs) TakeUpload(ctx context.Context, owner, id string, encPriv []byte) ([]byte, error) {
	if !fileID.MatchString(id) {
		return nil, errors.New("invalid upload id")
	}
	f, ok, err := b.store.File(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || f.Kind != "upload" || !strings.EqualFold(f.Owner, owner) || b.now().After(f.Expires) {
		return nil, fmt.Errorf("upload %s not found (uploads expire after %s)", id, uploadTTL)
	}
	raw, err := os.ReadFile(b.path("upload", id))
	if err != nil {
		return nil, err
	}
	var sealed identity.SealedPayload
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return nil, err
	}
	return identity.OpenSeal(encPriv, sealed, identity.EmailUploadSealDomain, identity.EmailBlobContext(id))
}

// DropUpload deletes an upload once its send went out.
func (b *Blobs) DropUpload(ctx context.Context, id string) {
	_ = os.Remove(b.path("upload", id))
	_ = b.store.DeleteFile(ctx, id)
}

// Sweep deletes expired blobs and uploads.
func (b *Blobs) Sweep(ctx context.Context) (int, error) {
	gone := 0
	for {
		expired, err := b.store.Expired(ctx, 500)
		if err != nil || len(expired) == 0 {
			return gone, err
		}
		for _, f := range expired {
			_ = os.Remove(b.path(f.Kind, f.ID))
			if err := b.store.DeleteFile(ctx, f.ID); err != nil {
				return gone, err
			}
			gone++
		}
	}
}
