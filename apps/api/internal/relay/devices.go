package relay

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/files"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

func logDeviceError(owner, deviceID string, err error) {
	log.Printf("devices: %s/%s registry write failed: %v", owner, deviceID, err)
}

// Device registry (EPIC-004 E04-T6).
//
// `poweur-sys/relay/devices.json` is the relay's own record of the devices an
// identity uses: what they are called, when each was last seen, how far each
// has synced, and which app passwords each holds. The owner reads it (over
// DAV, or through the endpoints below); nobody else does, and no peer-facing
// presence API exists — EPIC-009 E09-T2's privacy decision, that presence is
// not user-visible in v1, is honoured by keeping every last-seen fact inside
// the owner's own `poweur-sys/relay` zone.
//
// **Writes here are deliberately not journaled.** Every session creation,
// stream open and changes-feed read touches a device row; putting those in
// the changes journal would wake every sync client on the identity each time
// any of them said hello — a feedback loop where syncing causes syncing. The
// access log in the same zone is written the same way, and for the same
// reason. Sync clients pull devices.json on a full manifest pass; they never
// push it.
//
// **What revocation actually kills.** Sessions, DAV tokens and app passwords
// bound to the device — the derived credentials. It cannot kill the identity
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

// devicesDocPath is the tree path of the registry.
var devicesDocPath = files.SysRelay + "/devices.json"

// deviceObservation is one thing the relay noticed about a device. Zero
// fields mean "unchanged" — a stream opening reports presence and nothing
// else, a changes read reports a cursor.
type deviceObservation struct {
	Fingerprint string
	Name        string
	Kind        string
	SyncScopes  []string
	SyncCursor  string
	// AppPassword links a freshly minted app password to the device.
	AppPassword string
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
	}
}

// readDevices loads and validates the registry. A missing or unreadable file
// is an empty registry: the document appears on first observation.
func (s *Server) readDevices(ctx context.Context, owner string) idpkg.DevicesFile {
	if !s.davEnabled() {
		return idpkg.DevicesFile{Version: 1}
	}
	f, err := s.filesProvider.OpenFile(ctx, owner, devicesDocPath, os.O_RDONLY, 0)
	if err != nil {
		return idpkg.DevicesFile{Version: 1}
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxDevicesDocBytes+1))
	_ = f.Close()
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
	_ = s.filesProvider.Mkdir(ctx, owner, files.SysRelay)
	f, err := s.filesProvider.OpenFile(ctx, owner, devicesDocPath,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// touchDevice records an observation, returning the device id it applied to
// ("" when the client claimed no device or storage is off).
//
// Best-effort by construction: a registry write must never fail the request
// that produced the observation. Sync working matters more than knowing
// which laptop did it.
func (s *Server) touchDevice(ctx context.Context, owner string, obs deviceObservation) string {
	if !s.davEnabled() || obs.Fingerprint == "" {
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
	if obs.AppPassword != "" && !containsFold(device.AppPasswords, obs.AppPassword) {
		device.AppPasswords = append(device.AppPasswords, obs.AppPassword)
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

	if err := s.writeDevices(ctx, owner, doc.Upsert(device)); err != nil {
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
	if !s.davEnabled() || fingerprint == "" {
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
	DeviceID     string `json:"device_id"`
	Sessions     int    `json:"sessions_revoked"`
	DAVTokens    int    `json:"dav_tokens_revoked"`
	AppPasswords int    `json:"app_passwords_revoked"`
}

// revokeDevice cuts a device off: registry row marked revoked, its sessions
// dropped, its DAV tokens invalidated, its app-password entries deleted.
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
	device, ok := doc.Find(deviceID)
	if !ok {
		return out, false
	}

	out.Sessions = s.sessions.DeleteMatching(owner, func(sess storage.Session) bool {
		return idpkg.DeviceIDFromFingerprint(sess.DeviceFingerprint) == deviceID
	})
	out.DAVTokens = s.davTokens.RevokeMatching(func(t davToken) bool {
		return strings.EqualFold(t.Audience, owner) && t.DeviceID == deviceID
	})
	out.AppPasswords = s.deleteAppPasswords(ctx, owner, device.AppPasswords)

	if doc.Revoke(deviceID, time.Now()) {
		if err := s.writeDevices(ctx, owner, doc); err != nil {
			logDeviceError(owner, deviceID, err)
			return out, false
		}
	}
	return out, true
}

// deleteAppPasswords rewrites app-passwords.json without the named entries,
// and without any entry that names the device directly. Re-reading the file
// per DAV request (the existing design) is what makes this take effect on
// the next request rather than the next restart.
func (s *Server) deleteAppPasswords(ctx context.Context, owner string, names []string) int {
	if !s.davEnabled() {
		return 0
	}
	path := files.SysRelay + "/app-passwords.json"
	f, err := s.filesProvider.OpenFile(ctx, owner, path, os.O_RDONLY, 0)
	if err != nil {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxDevicesDocBytes+1))
	_ = f.Close()
	if err != nil {
		return 0
	}
	parsed, err := idpkg.ParseAppPasswordsFile(raw)
	if err != nil {
		return 0
	}
	kept := make([]idpkg.AppPassword, 0, len(parsed.Passwords))
	removed := 0
	for _, entry := range parsed.Passwords {
		if containsFold(names, entry.Name) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	if removed == 0 {
		return 0
	}
	parsed.Passwords = kept
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return 0
	}
	w, err := s.filesProvider.OpenFile(ctx, owner, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0
	}
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		_ = w.Close()
		return 0
	}
	_ = w.Close()
	// Unlike devices.json this file is the owner's, and sync clients hold
	// copies of it — so this write does belong in the journal.
	if s.filesIndex != nil {
		etag, size, err := files.ETagFromReader(strings.NewReader(string(out)))
		if err == nil {
			s.filesIndex.RecordWrite(owner, path, etag, size, time.Now().UTC(), "relay")
		}
	}
	return removed
}

// handleDevicesGet serves GET /devices/{identity} — the owner's device list.
func (s *Server) handleDevicesGet(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.deviceOwnerAuth(w, r, files.AccessRead)
	if !ok {
		return
	}
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
	owner, ok := s.deviceOwnerAuth(w, r, files.AccessWrite)
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

// deviceOwnerAuth authenticates the tree owner for the registry endpoints,
// mirroring the quota endpoint: any owner credential the files layer
// accepts (DAV token, app password) works, and the credential's own scope
// still has to cover the document.
func (s *Server) deviceOwnerAuth(w http.ResponseWriter, r *http.Request, access files.Access) (string, bool) {
	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "the device registry requires POWEUR_DATA")
		return "", false
	}
	owner := strings.ToLower(r.PathValue("identity"))
	if !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return "", false
	}
	principal, ok, reason := s.authenticateDAV(r, owner)
	if !ok || !principal.Owner {
		if reason == "" {
			reason = "owner credentials required"
		}
		s.noteDAVAuthFailure(r, owner, principal.Identity, reason)
		w.Header().Set("WWW-Authenticate", `Basic realm="poweur-dav", Bearer`)
		writeError(w, http.StatusUnauthorized, "unauthorized", reason)
		return "", false
	}
	if !principal.Scope.Allows(devicesDocPath, access) {
		writeError(w, http.StatusForbidden, "forbidden", "credential scope does not cover the device registry")
		return "", false
	}
	return owner, true
}
