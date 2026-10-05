package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

func logDeviceError(owner, deviceID string, err error) {
	log.Printf("devices: %s/%s registry write failed: %v", owner, deviceID, err)
}

// Device registry (EPIC-004 E04-T6).
//
// `.poweur/state/devices.json` is the relay's own record of the devices an
// identity uses: what they are called and when each was last seen. The relay
// writes it; the owner reads it through the endpoints below; nobody else
// does, and no peer-facing presence API exists — EPIC-009 E09-T2's privacy
// decision, that presence is not user-visible, is honoured by keeping every
// last-seen fact inside the owner's own `.poweur/state` zone.
//
// **What revocation actually kills.** Sessions bound to the device — the
// derived credentials. It cannot kill the identity
// private key: a device holding that key *is* the owner, and can mint fresh
// credentials under any fingerprint it likes. Recovering from a genuinely
// compromised key is key rotation (EPIC-011 / E01-T5), not device
// revocation. Saying so plainly beats implying a guarantee the design cannot
// make.

const (
	// deviceTouchInterval is how stale last_seen may get before a presence
	// observation is worth a disk write. Devices check in constantly; the
	// owner cares about days, not seconds.
	deviceTouchInterval = time.Minute
	// maxDevicesDocBytes matches the poweur-sys document cap.
	maxDevicesDocBytes = 64 * 1024
)

// deviceObservation is one thing the relay noticed about a device. Zero
// fields mean "unchanged" — a stream opening reports presence and nothing
// else, a changes read reports a cursor.
type deviceObservation struct {
	Fingerprint string
	Name        string
	Kind        string
	Client      string
	Platform    string
	Browser     string
	Enrollment  string
	SyncScopes  []string
	SyncCursor  string
}

// deviceLocks serializes the read-modify-write of one identity's registry.
// The document is small and contended only by that identity's own devices,
// so one mutex per identity is enough; a global lock would serialize every
// tenant behind the noisiest one.
type deviceLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newDeviceLocks() *deviceLocks { return &deviceLocks{locks: map[string]*sync.Mutex{}} }

func (d *deviceLocks) get(identity string) *sync.Mutex {
	key := strings.ToLower(identity)
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.locks[key]
	if !ok {
		m = &sync.Mutex{}
		d.locks[key] = m
	}
	return m
}

// deviceFromRequest reads the device headers a client may send. They are
// optional everywhere: a client that says nothing simply does not appear in
// the registry, which is the pre-E04-T6 behaviour.
func deviceFromRequest(r *http.Request) deviceObservation {
	return deviceObservation{
		Fingerprint: strings.TrimSpace(r.Header.Get("X-Poweur-Device")),
		Name:        r.Header.Get("X-Poweur-Device-Name"),
		Kind:        r.Header.Get("X-Poweur-Device-Kind"),
		Client:      r.Header.Get("X-Poweur-Device-Client"),
		Platform:    r.Header.Get("X-Poweur-Device-Platform"),
		Browser:     r.Header.Get("X-Poweur-Device-Browser"),
		Enrollment:  r.Header.Get("X-Poweur-Device-Enrollment"),
	}
}

// readDevices loads and validates the registry. A missing or unreadable file
// is an empty registry: the document appears on first observation.
func (s *Server) readDevices(ctx context.Context, owner string) idpkg.DevicesFile {
	raw, err := s.sysFiles.Read(ctx, owner, devicesDocPath)
	if err != nil || len(raw) > maxDevicesDocBytes {
		return idpkg.DevicesFile{Version: 1}
	}
	parsed, err := idpkg.ParseDevicesFile(raw)
	if err != nil {
		// A registry the relay itself cannot read is worse than none: it
		// would freeze revocation. Start over loudly rather than silently.
		return idpkg.DevicesFile{Version: 1}
	}
	if parsed.Version == 0 {
		parsed.Version = 1
	}
	return parsed
}

// writeDevices validates then persists the registry.
func (s *Server) writeDevices(ctx context.Context, owner string, doc idpkg.DevicesFile) error {
	doc.Version = 1
	if err := doc.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return s.sysFiles.Write(ctx, owner, devicesDocPath, append(raw, '\n'))
}

// touchDevice records an observation, returning the device id it applied to
// ("" when the client claimed no device).
//
// Best-effort by construction: a registry write must never fail the request
// that produced the observation. Sync working matters more than knowing
// which laptop did it.
func (s *Server) touchDevice(ctx context.Context, owner string, obs deviceObservation) string {
	if obs.Fingerprint == "" {
		return ""
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	if !s.identities.Exists(owner) {
		return ""
	}
	id := idpkg.DeviceIDFromFingerprint(obs.Fingerprint)
	if id == "" {
		return ""
	}
	lock := s.deviceLocks.get(owner)
	lock.Lock()
	defer lock.Unlock()

	doc := s.readDevices(ctx, owner)
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)

	device, existed := doc.Find(id)
	if !existed {
		if len(doc.Devices) >= idpkg.MaxDevices {
			return id // full registry: keep serving, stop recording
		}
		device = idpkg.Device{ID: id, AddedAt: stamp, Kind: idpkg.DeviceKindUnknown}
	}
	// A revoked row stays revoked until the owner's client re-enrols under a
	// new fingerprint. Silently resurrecting it on the next hello would undo
	// the revocation the owner just performed.
	if device.Revoked {
		return id
	}

	changed := !existed
	if name := idpkg.SanitizeDeviceName(obs.Name); name != "" && name != device.Name {
		device.Name = name
		changed = true
	}
	if obs.Kind != "" {
		if kind := idpkg.NormalizeDeviceKind(obs.Kind); kind != device.Kind {
			device.Kind = kind
			changed = true
		}
	}
	if client := idpkg.NormalizeDeviceClient(obs.Client); client != "" && client != device.Client {
		device.Client = client
		changed = true
	}
	if platform := idpkg.SanitizeDeviceField(obs.Platform); platform != "" && platform != device.Platform {
		device.Platform = platform
		changed = true
	}
	if browser := idpkg.SanitizeDeviceField(obs.Browser); browser != "" && browser != device.Browser {
		device.Browser = browser
		changed = true
	}
	if enrollment := idpkg.SanitizeEnrollmentID(obs.Enrollment); enrollment != "" && enrollment != device.EnrollmentID {
		device.EnrollmentID = enrollment
		changed = true
	}
	if len(obs.SyncScopes) > 0 && !sameStrings(obs.SyncScopes, device.SyncScopes) {
		scopes := obs.SyncScopes
		if len(scopes) > idpkg.MaxDeviceSyncScopes {
			scopes = scopes[:idpkg.MaxDeviceSyncScopes]
		}
		device.SyncScopes = scopes
		changed = true
	}
	if obs.SyncCursor != "" && obs.SyncCursor != device.SyncCursor {
		device.SyncCursor = obs.SyncCursor
		device.SyncedAt = stamp
		changed = true
	}
	// last_seen alone is only worth a write once the old value has gone
	// stale; otherwise a chatty client rewrites the document per request.
	if !changed {
		if last, err := time.Parse(time.RFC3339, device.LastSeen); err == nil &&
			now.Sub(last) < deviceTouchInterval {
			return id
		}
	}
	device.LastSeen = stamp

	if err := s.writeDevices(ctx, owner, doc.Upsert(device)); err != nil && !errors.Is(err, errSystemFilesUnavailable) {
		// Loud, not fatal: the caller's request still succeeds.
		logDeviceError(owner, id, err)
	}
	return id
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// deviceRevoked reports whether the caller's claimed device has been revoked.
// Consulted where a *derived* credential is minted, so a revoked laptop that
// still holds an identity key cannot quietly re-arm itself with the same
// name.
func (s *Server) deviceRevoked(ctx context.Context, owner, fingerprint string) bool {
	if fingerprint == "" {
		return false
	}
	id := idpkg.DeviceIDFromFingerprint(fingerprint)
	if id == "" {
		return false
	}
	device, ok := s.readDevices(ctx, strings.ToLower(strings.TrimSpace(owner))).Find(id)
	return ok && device.Revoked
}

// deviceRevocation is the outcome of revoking one device, reported back so
// the owner can see what it actually cost.
type deviceRevocation struct {
	DeviceID string `json:"device_id"`
	Sessions int    `json:"sessions_revoked"`
}

// revokeDevice cuts a device off: registry row marked revoked and its
// sessions dropped.
//
// Order matters. Credentials go first and the registry row last: if the
// process dies halfway, a device with no credentials and an un-revoked row
// is a device that has to come back and be revoked again — which is
// recoverable. The reverse (row revoked, credentials alive) looks done and
// is not.
func (s *Server) revokeDevice(ctx context.Context, owner, deviceID string) (deviceRevocation, bool) {
	out := deviceRevocation{DeviceID: deviceID}
	lock := s.deviceLocks.get(owner)
	lock.Lock()
	defer lock.Unlock()

	doc := s.readDevices(ctx, owner)
	if _, ok := doc.Find(deviceID); !ok {
		return out, false
	}

	out.Sessions = s.sessions.DeleteMatching(owner, func(sess storage.Session) bool {
		return idpkg.DeviceIDFromFingerprint(sess.DeviceFingerprint) == deviceID
	})
	if doc.Revoke(deviceID, time.Now()) {
		if err := s.writeDevices(ctx, owner, doc); err != nil {
			logDeviceError(owner, deviceID, err)
			return out, false
		}
	}
	return out, true
}

// handleDevicesGet serves GET /devices/{identity} — the owner's device list.
func (s *Server) handleDevicesGet(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.deviceOwnerAuth(w, r)
	if !ok {
		return
	}
	// The caller is the device being asked about: record what it says about itself
	// (name, kind, and the keystore enrollment it holds) before answering, so the list
	// it is shown already joins this device to its backup instead of showing both.
	s.touchDevice(r.Context(), owner, deviceFromRequest(r))
	doc := s.readDevices(r.Context(), owner)
	if doc.Devices == nil {
		doc.Devices = []idpkg.Device{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity": owner,
		"devices":  doc.Devices,
	})
}

// DeviceRevokeRequest is the body of POST /devices/{identity}/revoke.
type DeviceRevokeRequest struct {
	DeviceID string `json:"device_id"`
}

// handleDevicesRevoke serves POST /devices/{identity}/revoke.
func (s *Server) handleDevicesRevoke(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.deviceOwnerAuth(w, r)
	if !ok {
		return
	}
	var req DeviceRevokeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	deviceID := strings.ToLower(strings.TrimSpace(req.DeviceID))
	if !idpkg.ValidDeviceID(deviceID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "device_id must be a dev_… device id")
		return
	}
	result, found := s.revokeDevice(r.Context(), owner, deviceID)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// deviceOwnerAuth authenticates the owner for the registry endpoints with
// the same proof an inbox pickup needs: a one-shot challenge signed by the
// identity key or a live session key.
func (s *Server) deviceOwnerAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner := strings.ToLower(r.PathValue("identity"))
	if !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return "", false
	}
	if !s.authorizeInboxRead(w, r, owner) {
		return "", false
	}
	return owner, true
}
