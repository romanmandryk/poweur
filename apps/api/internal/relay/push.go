package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/webpush"
	idpkg "github.com/poweur/identity"
)

// Background push (EPIC-009 E09-T11).
//
// E09-T2's stream only lives while a page is open; a phone in a pocket has
// none. When something arrives for an identity the relay also sends a Web
// Push to each of its devices that registered one and is not already
// listening. The payload names the identity and the kind of event and
// carries the **pending count** — what the relay knows without decrypting
// anything. Message content never leaves in a push: the app opens and
// picks up as it always does.
//
// Pushes are coalesced per identity (PUSH_COALESCE), so fifty messages in a
// burst light the phone up once, and they are sent off the request path —
// a slow or failing push service never delays the POST that caused it.

// pushKinds are the notify kinds worth waking a phone for, and how urgently.
// Acks are not: "bob read your message" is not worth a buzz.
var pushKinds = map[string]webpush.Urgency{
	"message": webpush.UrgencyHigh,
	"request": webpush.UrgencyHigh,
	"anon":    webpush.UrgencyNormal,
}

// pushRank orders kinds within one coalesced push: the notification names
// the most important thing that happened.
var pushRank = map[string]int{"anon": 1, "message": 2, "request": 3}

const (
	// pushTTL is how long a push service may hold a push for an offline
	// device. The spool holds the mail itself for SPOOL_TTL.
	pushTTL = 24 * time.Hour
	// pushSendTimeout bounds one push service round trip.
	pushSendTimeout = 15 * time.Second
	// pushConcurrency bounds in-flight pushes across the relay.
	pushConcurrency = 16
)

// PushPayload is the plaintext the service worker receives.
type PushPayload struct {
	V        int    `json:"v"`
	Identity string `json:"identity"`
	// Kind is message | request | anon | test.
	Kind string `json:"kind"`
	// Count is everything waiting for the identity on this relay: unpicked
	// messages, contact requests and anonymous mail. The icon badge.
	Count     int    `json:"count"`
	Timestamp string `json:"ts"`
}

type pusher struct {
	sender   *webpush.Sender
	coalesce time.Duration
	hosts    []string
	insecure bool
	slots    chan struct{}

	mu      sync.Mutex
	pending map[string]string // identity -> highest-ranked kind waiting
	// wg tracks in-flight work so tests (and shutdown) can wait for it.
	wg sync.WaitGroup
}

// newPusher builds the push sender, or nil when the relay has no VAPID key.
func newPusher(key, subject string, hosts []string, coalesce time.Duration, insecure bool) *pusher {
	if key == "" {
		return nil
	}
	vapid, err := webpush.VAPIDFromKey(key, subject)
	if err != nil {
		// Config validation rejects this before the server starts; a
		// constructor used directly by tests just gets no push.
		log.Printf("push: disabled: %v", err)
		return nil
	}
	return &pusher{
		sender:   &webpush.Sender{VAPID: vapid, Client: &http.Client{Timeout: pushSendTimeout}},
		coalesce: coalesce,
		hosts:    hosts,
		insecure: insecure,
		slots:    make(chan struct{}, pushConcurrency),
		pending:  map[string]string{},
	}
}

// endpointAllowed is the SSRF guard: the relay POSTs to whatever endpoint a
// subscription names, so it only ever names a known push service.
func (p *pusher) endpointAllowed(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	if u.Scheme != "https" && !(p.insecure && u.Scheme == "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range p.hosts {
		allowed = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(allowed), "."))
		if allowed != "" && (host == allowed || strings.HasSuffix(host, "."+allowed)) {
			return true
		}
	}
	return false
}

// WaitPush blocks until every scheduled and in-flight push has finished.
// Tests use it; nothing on the request path does.
func (s *Server) WaitPush() {
	if s.push != nil {
		s.push.wg.Wait()
	}
}

// schedulePush queues a coalesced push for identity, if kind warrants one.
func (s *Server) schedulePush(identity, kind string) {
	p := s.push
	if p == nil {
		return
	}
	if _, ok := pushKinds[kind]; !ok {
		return
	}
	key := strings.ToLower(identity)
	p.mu.Lock()
	if waiting, ok := p.pending[key]; ok {
		if pushRank[kind] > pushRank[waiting] {
			p.pending[key] = kind
		}
		p.mu.Unlock()
		return
	}
	p.pending[key] = kind
	p.wg.Add(1)
	p.mu.Unlock()
	time.AfterFunc(p.coalesce, func() {
		defer p.wg.Done()
		p.mu.Lock()
		kind := p.pending[key]
		delete(p.pending, key)
		p.mu.Unlock()
		s.flushPush(key, kind)
	})
}

// pendingCount is what the badge shows: everything waiting on this relay.
func (s *Server) pendingCount(identity string) int {
	return s.inbox.Pending(identity) + s.requests.Pending(identity) + s.anonInbox.Pending(identity)
}

// flushPush sends one push per registered, not-listening device.
func (s *Server) flushPush(identity, kind string) {
	ctx := context.Background()
	doc := s.readDevices(ctx, identity)
	payload, err := json.Marshal(PushPayload{
		V:         1,
		Identity:  identity,
		Kind:      kind,
		Count:     s.pendingCount(identity),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	opts := webpush.Options{TTL: pushTTL, Urgency: pushKinds[kind], Topic: webpush.Topic(identity)}
	for _, device := range doc.Devices {
		if device.Push == nil || device.Revoked {
			continue
		}
		if s.hub.deviceListening(identity, device.ID) {
			s.event(ctx, "push.send", "skipped_listening")
			continue
		}
		s.sendDevicePush(ctx, identity, device.ID, *device.Push, payload, opts)
	}
}

// sendDevicePush delivers one push in the background and cleans up a
// subscription the push service says is gone.
func (s *Server) sendDevicePush(ctx context.Context, identity, deviceID string, reg idpkg.DevicePush, payload []byte, opts webpush.Options) {
	p := s.push
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.slots <- struct{}{}
		defer func() { <-p.slots }()
		err := s.deliverPush(ctx, reg, payload, opts)
		switch {
		case err == nil:
			s.event(ctx, "push.send", "success")
		case errors.Is(err, webpush.ErrGone):
			s.event(ctx, "push.send", "gone")
			s.clearDevicePush(ctx, identity, deviceID, reg.Endpoint)
		default:
			s.event(ctx, "push.send", "failure")
			log.Printf("push: %s/%s: %v", identity, deviceID, err)
		}
	}()
}

func (s *Server) deliverPush(ctx context.Context, reg idpkg.DevicePush, payload []byte, opts webpush.Options) error {
	if !s.push.endpointAllowed(reg.Endpoint) {
		// The allow-list may have shrunk since the device registered.
		return errors.New("endpoint no longer allowed")
	}
	var sub webpush.Subscription
	sub.Endpoint = reg.Endpoint
	sub.Keys.P256dh = reg.P256dh
	sub.Keys.Auth = reg.Auth
	ctx, cancel := context.WithTimeout(ctx, pushSendTimeout)
	defer cancel()
	return s.push.sender.Send(ctx, sub, payload, opts)
}

// setDevicePush stores (or, with nil, removes) a device's registration.
// With endpoint set, a removal only applies while the device still holds
// that endpoint — a gone answer for an old subscription must not wipe the
// one the device registered since.
func (s *Server) setDevicePush(ctx context.Context, owner, deviceID string, reg *idpkg.DevicePush, endpoint string) error {
	lock := s.deviceLocks.get(owner)
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDevices(ctx, owner)
	device, ok := doc.Find(deviceID)
	if !ok {
		return errNoDevice
	}
	if reg == nil && (device.Push == nil || (endpoint != "" && device.Push.Endpoint != endpoint)) {
		return nil
	}
	if reg != nil {
		if device.Revoked {
			return errDeviceRevoked
		}
		if device.Push == nil && doc.PushCount() >= idpkg.MaxPushDevices {
			return errTooManyPushDevices
		}
	}
	device.Push = reg
	return s.writeDevices(ctx, owner, doc.Upsert(device))
}

func (s *Server) clearDevicePush(ctx context.Context, owner, deviceID, endpoint string) {
	if err := s.setDevicePush(ctx, owner, deviceID, nil, endpoint); err != nil && !errors.Is(err, errNoDevice) {
		logDeviceError(owner, deviceID, err)
	}
}

var (
	errNoDevice           = errors.New("device not in registry")
	errDeviceRevoked      = errors.New("device is revoked")
	errTooManyPushDevices = errors.New("too many devices registered for push")
)

// --- HTTP ---------------------------------------------------------------

// handlePushConfig serves GET /push/config: whether this relay sends
// background pushes and the key a browser subscribes with.
func (s *Server) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":          true,
		"vapid_public_key": s.push.sender.VAPID.PublicKey(),
		"transports":       []string{idpkg.PushTransportWebPush},
	})
}

// PushSubscribeRequest is the body of POST /push/{identity}/subscribe: the
// browser's PushSubscription.toJSON(), plus the transport.
type PushSubscribeRequest struct {
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint"`
	Keys      struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushDevice authenticates the owner and names the calling device. Push is
// per device, so a caller that sends no X-Poweur-Device has nowhere to
// keep a registration.
func (s *Server) pushDevice(w http.ResponseWriter, r *http.Request) (owner, deviceID string, ok bool) {
	if s.push == nil {
		writeError(w, http.StatusNotFound, "push_disabled", "this relay does not send background push")
		return "", "", false
	}
	owner, ok = s.deviceOwnerAuth(w, r)
	if !ok {
		return "", "", false
	}
	deviceID = s.touchDevice(r.Context(), owner, deviceFromRequest(r))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "device_required",
			"push is registered per device: send X-Poweur-Device")
		return "", "", false
	}
	return owner, deviceID, true
}

// handlePushSubscribe serves POST /push/{identity}/subscribe.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	owner, deviceID, ok := s.pushDevice(w, r)
	if !ok {
		return
	}
	var req PushSubscribeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.Transport != "" && req.Transport != idpkg.PushTransportWebPush {
		writeError(w, http.StatusBadRequest, "unsupported_transport", "only webpush is supported")
		return
	}
	var sub webpush.Subscription
	sub.Endpoint = strings.TrimSpace(req.Endpoint)
	sub.Keys.P256dh = strings.TrimSpace(req.Keys.P256dh)
	sub.Keys.Auth = strings.TrimSpace(req.Keys.Auth)
	if err := sub.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_subscription", err.Error())
		return
	}
	if !s.push.endpointAllowed(sub.Endpoint) {
		writeError(w, http.StatusForbidden, "endpoint_not_allowed",
			"the endpoint is not a push service this relay delivers to")
		return
	}
	reg := &idpkg.DevicePush{
		Transport: idpkg.PushTransportWebPush,
		Endpoint:  sub.Endpoint,
		P256dh:    sub.Keys.P256dh,
		Auth:      sub.Keys.Auth,
		AddedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	switch err := s.setDevicePush(r.Context(), owner, deviceID, reg, ""); {
	case err == nil:
	case errors.Is(err, errDeviceRevoked):
		writeError(w, http.StatusForbidden, "device_revoked", "this device was revoked")
		return
	case errors.Is(err, errTooManyPushDevices):
		writeError(w, http.StatusConflict, "too_many_push_devices",
			"turn notifications off on another device first")
		return
	case errors.Is(err, errNoDevice):
		writeError(w, http.StatusServiceUnavailable, "registry_full", "the device registry is full")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save the registration")
		return
	}
	s.event(r.Context(), "push.subscribe", "success")
	writeJSON(w, http.StatusOK, map[string]any{"device_id": deviceID, "enabled": true})
}

// handlePushUnsubscribe serves POST /push/{identity}/unsubscribe for the
// calling device.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	owner, deviceID, ok := s.pushDevice(w, r)
	if !ok {
		return
	}
	if err := s.setDevicePush(r.Context(), owner, deviceID, nil, ""); err != nil && !errors.Is(err, errNoDevice) {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not remove the registration")
		return
	}
	s.event(r.Context(), "push.unsubscribe", "success")
	writeJSON(w, http.StatusOK, map[string]any{"device_id": deviceID, "enabled": false})
}

// handlePushTest serves POST /push/{identity}/test: one push to the calling
// device, now, answered with what the push service said — the "is this
// actually working on my phone?" button.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	owner, deviceID, ok := s.pushDevice(w, r)
	if !ok {
		return
	}
	device, found := s.readDevices(r.Context(), owner).Find(deviceID)
	if !found || device.Push == nil {
		writeError(w, http.StatusNotFound, "not_subscribed", "this device has no push registration")
		return
	}
	payload, _ := json.Marshal(PushPayload{
		V: 1, Identity: owner, Kind: "test",
		Count:     s.pendingCount(owner),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	err := s.deliverPush(r.Context(), *device.Push, payload, webpush.Options{TTL: time.Minute, Urgency: webpush.UrgencyHigh})
	var status *webpush.StatusError
	switch {
	case err == nil:
		s.event(r.Context(), "push.test", "success")
		writeJSON(w, http.StatusOK, map[string]any{"sent": true})
	case errors.Is(err, webpush.ErrGone):
		s.clearDevicePush(r.Context(), owner, deviceID, device.Push.Endpoint)
		writeError(w, http.StatusGone, "subscription_gone", "the push service no longer knows this device; turn notifications on again")
	case errors.As(err, &status):
		writeError(w, http.StatusBadGateway, "push_rejected", status.Error())
	default:
		writeError(w, http.StatusBadGateway, "push_failed", err.Error())
	}
}
