package identity

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Device registry (EPIC-004 E04-T6): .poweur/state/devices.json.
//
// Sync made "the user's devices" first-class actors — they already existed
// implicitly as sessions carrying a DeviceFingerprint. This document gives
// them a name, a kind, a last-seen, the credentials they hold, and a
// server-side sync cursor so the owner can answer "when did my phone last
// sync?" without asking the phone.
//
// **The relay writes it; the owner reads it.** It lives in .poweur/state/,
// the relay-written zone, because the facts in it are the relay's own
// observations. The validator still runs on write so a relay bug cannot
// persist a document its own reader would choke on, and so the schema has
// one enforced definition rather than two.
//
// **The fingerprint is a label, not a credential.** Device fingerprints
// arrive unsigned today (they are absent from the canonical session
// registration string), and this document does not change that. A
// fingerprint only ever *narrows* what revoking a device kills; it never
// widens what a caller may do. Lying about yours means your own credentials
// survive a revocation you asked for — and you already hold the key.

// Device kinds. Unknown is the value used when a client says nothing.
const (
	DeviceKindLaptop  = "laptop"
	DeviceKindDesktop = "desktop"
	DeviceKindPhone   = "phone"
	DeviceKindTablet  = "tablet"
	DeviceKindBrowser = "browser"
	DeviceKindAgent   = "agent"
	DeviceKindUnknown = "unknown"
)

// DeviceKinds is every accepted kind, in display order.
var DeviceKinds = []string{
	DeviceKindLaptop, DeviceKindDesktop, DeviceKindPhone,
	DeviceKindTablet, DeviceKindBrowser, DeviceKindAgent, DeviceKindUnknown,
}

// Device clients say what kind of software is talking: the Poweur app (the
// native shell), the web app in a browser, or a command-line client.
const (
	DeviceClientApp = "app"
	DeviceClientWeb = "web"
	DeviceClientCLI = "cli"
)

// DeviceClients is every accepted client type. Unlike kind there is no
// "unknown" member: an unrecognised or absent client is simply left empty.
var DeviceClients = []string{DeviceClientApp, DeviceClientWeb, DeviceClientCLI}

const (
	// MaxDeviceFieldLen caps the free-text platform/browser labels.
	MaxDeviceFieldLen = 32
	// MaxEnrollmentIDLen caps the keystore enrollment id a device links to.
	MaxEnrollmentIDLen = 64
	// deviceIDChars is the length of the base32 body of a device id.
	deviceIDChars = 16
	// DeviceIDPrefix marks a device id apart from a session id.
	DeviceIDPrefix = "dev_"
	// MaxDeviceNameLen caps the owner-facing label.
	MaxDeviceNameLen = 64
	// MaxDevices caps the registry so a client looping through fingerprints
	// cannot grow the document without bound. The document is still subject
	// to the 64 KB poweur-sys cap on top of this.
	MaxDevices = 200
	// MaxDeviceSyncScopes caps the per-device selective-sync prefix list.
	MaxDeviceSyncScopes = 32
)

var deviceIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// DeviceIDFromFingerprint derives the stable device id for a client-supplied
// fingerprint. Returns "" for an empty fingerprint (no device claimed).
//
// Deriving rather than allocating is what makes the registry work without a
// registration handshake: the same laptop reconnecting after a restart, on a
// new session, through a different endpoint, lands on the same row. The hash
// also means the relay keeps no reversible copy of whatever the client chose
// to put in the fingerprint (a hostname, a hardware id).
func DeviceIDFromFingerprint(fingerprint string) string {
	normalized := strings.ToLower(strings.TrimSpace(fingerprint))
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("poweur-device-id\n" + normalized))
	return DeviceIDPrefix + strings.ToLower(deviceIDEncoding.EncodeToString(sum[:])[:deviceIDChars])
}

// ValidDeviceID reports whether id has the derived shape.
func ValidDeviceID(id string) bool {
	if !strings.HasPrefix(id, DeviceIDPrefix) {
		return false
	}
	body := strings.TrimPrefix(id, DeviceIDPrefix)
	if len(body) != deviceIDChars {
		return false
	}
	for _, c := range body {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// NormalizeDeviceKind maps a client-supplied kind onto the accepted set,
// falling back to unknown rather than rejecting: a device that calls itself
// something new is still a device, and refusing the write would cost the
// owner the whole row.
func NormalizeDeviceKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	for _, known := range DeviceKinds {
		if k == known {
			return known
		}
	}
	return DeviceKindUnknown
}

// SanitizeDeviceName trims a client-supplied label to something safe to
// render: no control characters, no runaway length.
func SanitizeDeviceName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > MaxDeviceNameLen {
		cleaned = strings.TrimSpace(cleaned[:MaxDeviceNameLen])
	}
	return cleaned
}

// NormalizeDeviceClient maps a client-supplied client type onto the accepted
// set, returning "" for anything else (the row keeps no client rather than a
// made-up one).
func NormalizeDeviceClient(client string) string {
	c := strings.ToLower(strings.TrimSpace(client))
	for _, known := range DeviceClients {
		if c == known {
			return known
		}
	}
	return ""
}

// SanitizeDeviceField trims a short free-text label (platform, browser) the
// same way SanitizeDeviceName does, with the smaller cap.
func SanitizeDeviceField(value string) string {
	cleaned := SanitizeDeviceName(value)
	if len(cleaned) > MaxDeviceFieldLen {
		cleaned = strings.TrimSpace(cleaned[:MaxDeviceFieldLen])
	}
	return cleaned
}

// SanitizeEnrollmentID keeps a keystore enrollment id to the characters the
// keystore itself produces (base64url), and drops anything else.
func SanitizeEnrollmentID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > MaxEnrollmentIDLen {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return id
}

// Device is one row of devices.json.
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`

	// Client is what is talking — app, web or cli — and Platform the OS it
	// runs on ("macOS", "iOS"). Browser names the browser for web clients.
	// All three are self-reported labels, like Name.
	Client   string `json:"client,omitempty"`
	Platform string `json:"platform,omitempty"`
	Browser  string `json:"browser,omitempty"`
	// EnrollmentID links the row to this device's keystore enrollment (a
	// passkey- or OS-keystore-wrapped copy of the seed), when it has one, so
	// one list can show both. Devices that hold their key only locally, like
	// the CLI, have none.
	EnrollmentID string `json:"enrollment_id,omitempty"`

	AddedAt  string `json:"added_at,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`

	// SyncScopes are the tree prefixes this device syncs (empty = whole
	// tree), reported by the client on its changes-feed reads.
	SyncScopes []string `json:"sync_scopes,omitempty"`
	// SyncCursor is the last changes cursor the device acknowledged, kept
	// server-side so the owner can see staleness without asking the device.
	SyncCursor string `json:"sync_cursor,omitempty"`
	// SyncedAt is when SyncCursor was last advanced.
	SyncedAt string `json:"synced_at,omitempty"`

	// Revoked keeps the row as an audit trail instead of deleting it: the
	// owner should be able to see that a lost phone was cut off, and when.
	Revoked   bool   `json:"revoked,omitempty"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

// DevicesFile is the schema of .poweur/state/devices.json.
type DevicesFile struct {
	Version int      `json:"version"`
	Devices []Device `json:"devices"`
}

func validDeviceScope(raw string) error {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" {
		return fmt.Errorf("sync_scopes entry is empty")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid sync_scopes segment %q", seg)
		}
	}
	return nil
}

// Validate checks one device row.
func (d Device) Validate() error {
	if !ValidDeviceID(d.ID) {
		return fmt.Errorf("invalid device id %q (want %s + 16 base32 chars)", d.ID, DeviceIDPrefix)
	}
	if d.Name != SanitizeDeviceName(d.Name) {
		return fmt.Errorf("device %s: name must be printable and at most %d bytes", d.ID, MaxDeviceNameLen)
	}
	if d.Kind != "" && NormalizeDeviceKind(d.Kind) != d.Kind {
		return fmt.Errorf("device %s: invalid kind %q (want one of %s)", d.ID, d.Kind, strings.Join(DeviceKinds, ", "))
	}
	if d.Client != "" && NormalizeDeviceClient(d.Client) != d.Client {
		return fmt.Errorf("device %s: invalid client %q (want one of %s)", d.ID, d.Client, strings.Join(DeviceClients, ", "))
	}
	if d.Platform != SanitizeDeviceField(d.Platform) || d.Browser != SanitizeDeviceField(d.Browser) {
		return fmt.Errorf("device %s: platform and browser must be printable and at most %d bytes", d.ID, MaxDeviceFieldLen)
	}
	if d.EnrollmentID != SanitizeEnrollmentID(d.EnrollmentID) {
		return fmt.Errorf("device %s: invalid enrollment_id", d.ID)
	}
	for _, ts := range []struct{ field, value string }{
		{"added_at", d.AddedAt},
		{"last_seen", d.LastSeen},
		{"synced_at", d.SyncedAt},
		{"revoked_at", d.RevokedAt},
	} {
		if ts.value == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, ts.value); err != nil {
			return fmt.Errorf("device %s: %s must be RFC3339", d.ID, ts.field)
		}
	}
	if len(d.SyncScopes) > MaxDeviceSyncScopes {
		return fmt.Errorf("device %s: at most %d sync_scopes", d.ID, MaxDeviceSyncScopes)
	}
	for _, scope := range d.SyncScopes {
		if err := validDeviceScope(scope); err != nil {
			return fmt.Errorf("device %s: %w", d.ID, err)
		}
	}
	if d.Revoked && d.RevokedAt == "" {
		return fmt.Errorf("device %s: revoked device must carry revoked_at", d.ID)
	}
	return nil
}

// Validate checks the whole document.
func (f DevicesFile) Validate() error {
	if f.Version != 0 && f.Version != 1 {
		return fmt.Errorf("unsupported devices.json version %d", f.Version)
	}
	if len(f.Devices) > MaxDevices {
		return fmt.Errorf("device registry holds at most %d devices", MaxDevices)
	}
	seen := make(map[string]bool, len(f.Devices))
	for _, d := range f.Devices {
		if err := d.Validate(); err != nil {
			return err
		}
		if seen[strings.ToLower(d.ID)] {
			return fmt.Errorf("duplicate device %s", d.ID)
		}
		seen[strings.ToLower(d.ID)] = true
	}
	return nil
}

// ParseDevicesFile decodes and validates devices.json.
func ParseDevicesFile(raw []byte) (DevicesFile, error) {
	var f DevicesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return DevicesFile{}, fmt.Errorf("invalid devices.json: %w", err)
	}
	if err := f.Validate(); err != nil {
		return DevicesFile{}, err
	}
	return f, nil
}

// Find returns the device with id.
func (f DevicesFile) Find(id string) (Device, bool) {
	for _, d := range f.Devices {
		if strings.EqualFold(d.ID, id) {
			return d, true
		}
	}
	return Device{}, false
}

// Upsert replaces the row with d.ID, or appends it. Rows stay in first-seen
// order so a listing does not shuffle every time a device checks in.
func (f DevicesFile) Upsert(d Device) DevicesFile {
	for i, existing := range f.Devices {
		if strings.EqualFold(existing.ID, d.ID) {
			f.Devices[i] = d
			return f
		}
	}
	f.Devices = append(f.Devices, d)
	return f
}

// Remove drops a device row entirely. Revoke is the usual path; this exists
// for callers who want no audit trail (and for eviction when the registry is
// full).
func (f *DevicesFile) Remove(id string) bool {
	for i, d := range f.Devices {
		if strings.EqualFold(d.ID, id) {
			f.Devices = append(f.Devices[:i], f.Devices[i+1:]...)
			return true
		}
	}
	return false
}

// Revoke marks a device revoked at now. Reports whether the device existed
// and was not already revoked.
func (f *DevicesFile) Revoke(id string, now time.Time) bool {
	for i, d := range f.Devices {
		if !strings.EqualFold(d.ID, id) {
			continue
		}
		if d.Revoked {
			return false
		}
		f.Devices[i].Revoked = true
		f.Devices[i].RevokedAt = now.UTC().Format(time.RFC3339)
		return true
	}
	return false
}

// Stale reports how long ago the device last advanced its sync cursor, and
// whether it ever did. This is the "phone last synced 3 days ago" primitive:
// the cursor moving is the only honest evidence of a sync, because a device
// can hold a stream open for a week and copy nothing.
func (d Device) Stale(now time.Time) (time.Duration, bool) {
	if d.SyncedAt == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, d.SyncedAt)
	if err != nil {
		return 0, false
	}
	return now.UTC().Sub(t.UTC()), true
}
