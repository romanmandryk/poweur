package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	idpkg "github.com/poweur/identity"
)

// Suspension is an operator's hold on one identity hosted on this relay. A
// suspended ID cannot send, receive, sign in, use its drive or be resolved, and
// its name cannot be claimed again; lifting the hold restores everything.
// Deleted means its data has also been erased (DeleteIdentityData); the name
// stays held until the operator releases it, so an erased abuser cannot simply
// register it again.
type Suspension struct {
	Reason  string `json:"reason"`
	At      string `json:"at"`
	Deleted bool   `json:"deleted,omitempty"`
}

// SuspensionObjectKey is where the suspension list lives in the relay's store
// (edit it with `poweur-relay identities`). Like the quota overrides it is one
// small document that every relay process re-reads, so an S3 relay needs no
// disk and a change takes effect within suspensionInterval, with no restart.
const SuspensionObjectKey = "relay/suspended-identities.json"

var suspensionInterval = 15 * time.Second

type suspensions struct {
	mu      sync.Mutex
	store   provider.Store
	checked time.Time
	hash    string
	values  map[string]Suspension
	onBad   func(error)
}

func newSuspensions(onBad func(error)) *suspensions {
	return &suspensions{values: map[string]Suspension{}, onBad: onBad}
}

func normalizeSuspended(identity string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(identity), "."))
}

// lookup reports the hold on identity, if any.
func (s *suspensions) lookup(identity string) (Suspension, bool) {
	if s == nil || identity == "" {
		return Suspension{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	sp, ok := s.values[normalizeSuspended(identity)]
	return sp, ok
}

func (s *suspensions) refreshLocked() {
	if s.store == nil || (!s.checked.IsZero() && time.Since(s.checked) < suspensionInterval) {
		return
	}
	s.checked = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	obj, err := s.store.Get(ctx, SuspensionObjectKey, nil)
	cancel()
	switch {
	case err == nil:
		sum := sha256.Sum256(obj.Data)
		hash := hex.EncodeToString(sum[:])
		if hash == s.hash {
			return
		}
		values, err := parseSuspensions(obj.Data)
		if err != nil {
			// Keep the last good list: a broken edit must not lift every hold.
			if s.onBad != nil {
				s.onBad(err)
			}
			s.hash = hash
			return
		}
		s.values, s.hash = values, hash
	case errors.Is(err, provider.ErrNotFound):
		s.values, s.hash = map[string]Suspension{}, ""
	}
	// Any other error: the store is briefly unreachable, keep what we have.
}

func parseSuspensions(raw []byte) (map[string]Suspension, error) {
	doc := map[string]Suspension{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("suspended identities: %w", err)
	}
	values := make(map[string]Suspension, len(doc))
	for identity, sp := range doc {
		if err := idpkg.ValidateIdentityName(identity); err != nil {
			return nil, fmt.Errorf("suspended identities: %q: %w", identity, err)
		}
		values[normalizeSuspended(identity)] = sp
	}
	return values, nil
}

// EditSuspensions changes the suspension list in a store: edit receives the
// current entries and changes them in place. The result is validated and the
// write is conditional, so two operators editing at once cannot lose an update.
func EditSuspensions(ctx context.Context, store provider.Store, edit func(map[string]Suspension) error) (map[string]Suspension, error) {
	obj, err := store.Get(ctx, SuspensionObjectKey, nil)
	match := ""
	var doc map[string]Suspension
	switch {
	case err == nil:
		if doc, err = parseSuspensions(obj.Data); err != nil {
			return nil, err
		}
		match = obj.ETag
	case errors.Is(err, provider.ErrNotFound):
		doc = map[string]Suspension{}
	default:
		return nil, err
	}
	if err := edit(doc); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := parseSuspensions(raw); err != nil {
		return nil, err
	}
	if _, err := store.PutIf(ctx, SuspensionObjectKey, append(raw, '\n'), match); err != nil {
		if errors.Is(err, provider.ErrPrecondition) {
			return nil, errors.New("the suspension list changed while editing; run the command again")
		}
		return nil, err
	}
	return doc, nil
}

// Held reports whether identity is suspended or deleted here.
func (s *Server) held(identity string) (Suspension, bool) { return s.suspended.lookup(identity) }

// rejectHeld answers 410 when any named identity is suspended. It is for
// identities the request is about, which is the resource or the counterpart
// rather than the caller (see rejectSuspendedCaller).
func (s *Server) rejectHeld(w http.ResponseWriter, identities ...string) bool {
	for _, identity := range identities {
		if _, ok := s.held(identity); ok {
			writeError(w, http.StatusGone, "identity_suspended", "this ID is suspended by the relay operator")
			return true
		}
	}
	return false
}

// guardSuspended refuses any request that names a suspended identity as its
// resource (path), its host (an identity's own page or app) or its caller
// (X-Poweur-Identity), before the handler runs. The message and group-message
// endpoints name sender and recipient in their bodies, so they check those too.
func (s *Server) guardSuspended(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("identity") != "" && s.rejectHeld(w, r.PathValue("identity")) {
			return
		}
		if host := requestHost(r); host != "" && s.rejectHeld(w, host) {
			return
		}
		if caller := r.Header.Get("X-Poweur-Identity"); caller != "" {
			if _, ok := s.held(caller); ok {
				writeError(w, http.StatusForbidden, "identity_suspended", "this ID is suspended by the relay operator")
				return
			}
		}
		next(w, r)
	}
}
