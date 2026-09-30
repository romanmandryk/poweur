package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	idpkg "github.com/poweur/identity"
)

// `poweur devices` (EPIC-004 E04-T6): the owner's view of which machines are
// using their identity, and the switch that cuts one off.
//
//	poweur devices show               what this machine calls itself
//	poweur devices name <name>        rename this machine
//	poweur devices list               every device the relay has seen
//	poweur devices revoke <dev_…>     kill its sessions, tokens, app passwords
//
// The local half lives in `~/.poweur/device.json`, beside config.toml and
// keys/. It is deliberately additive to the shared CLI state: an older CLI
// (or the TS one) that has never heard of the file behaves exactly as
// before, because every device header is optional on the wire.

// localDevice is ~/.poweur/device.json.
type localDevice struct {
	// Fingerprint is a random, stable, machine-local string. Random rather
	// than derived from a hostname or a MAC: the relay only ever stores its
	// hash, and there is no reason to hand it a real-world identifier it
	// could correlate across identities.
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// DeviceID is the id the relay derives from this device's fingerprint.
func (d localDevice) DeviceID() string { return idpkg.DeviceIDFromFingerprint(d.Fingerprint) }

func devicePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".poweur", "device.json"), nil
}

// loadDevice reads the local device record, creating it on first use.
func loadDevice() (localDevice, error) {
	path, err := devicePath()
	if err != nil {
		return localDevice{}, err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var d localDevice
		if err := json.Unmarshal(raw, &d); err == nil && strings.TrimSpace(d.Fingerprint) != "" {
			return d, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return localDevice{}, err
	}
	d := localDevice{
		Fingerprint: base64.RawURLEncoding.EncodeToString(buf),
		Name:        defaultDeviceName(),
		Kind:        defaultDeviceKind(),
	}
	return d, saveDevice(d)
}

func saveDevice(d localDevice) error {
	path, err := devicePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func defaultDeviceName() string {
	if name := strings.TrimSpace(os.Getenv("POWEUR_DEVICE_NAME")); name != "" {
		return idpkg.SanitizeDeviceName(name)
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return idpkg.SanitizeDeviceName(host)
}

func defaultDeviceKind() string {
	if kind := strings.TrimSpace(os.Getenv("POWEUR_DEVICE_KIND")); kind != "" {
		return idpkg.NormalizeDeviceKind(kind)
	}
	// A CLI on a machine with no controlling terminal is almost always a
	// cron/systemd sync job rather than someone's laptop.
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		return idpkg.DeviceKindAgent
	}
	return idpkg.DeviceKindLaptop
}

// deviceHeaders returns the optional device identification headers. Failing
// to build them is never fatal: the request still works, the device just
// stays anonymous.
func deviceHeaders() map[string]string {
	d, err := loadDevice()
	if err != nil || d.Fingerprint == "" {
		return nil
	}
	h := map[string]string{
		"X-Poweur-Device":          d.Fingerprint,
		"X-Poweur-Device-Client":   idpkg.DeviceClientCLI,
		"X-Poweur-Device-Platform": platformName(),
	}
	if d.Name != "" {
		h["X-Poweur-Device-Name"] = d.Name
	}
	if d.Kind != "" {
		h["X-Poweur-Device-Kind"] = d.Kind
	}
	return h
}

func applyDeviceHeaders(h http.Header) {
	for k, v := range deviceHeaders() {
		h.Set(k, v)
	}
}

func runDevices(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur devices <show|name|list|revoke>")
		return 1
	}
	switch args[0] {
	case "show":
		return runDeviceShow(args[1:], stdout, stderr)
	case "name":
		return runDeviceName(args[1:], stdout, stderr)
	case "list":
		return runDeviceList(args[1:], stdout, stderr)
	case "revoke":
		return runDeviceRevoke(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown devices subcommand (want show, name, list, revoke)")
		return 1
	}
}

func runDeviceShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devices show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	d, err := loadDevice()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, stderr, map[string]string{
			"device_id": d.DeviceID(), "name": d.Name, "kind": d.Kind,
		})
	}
	fmt.Fprintf(stdout, "device_id: %s\nname:      %s\nkind:      %s\n", d.DeviceID(), d.Name, d.Kind)
	return 0
}

func runDeviceName(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: poweur devices name <name>")
		return 1
	}
	d, err := loadDevice()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	name := idpkg.SanitizeDeviceName(args[0])
	if name == "" {
		fmt.Fprintln(stderr, "device name is empty after sanitizing")
		return 1
	}
	d.Name = name
	if err := saveDevice(d); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "device %s is now %q\n", d.DeviceID(), name)
	return 0
}

// deviceListResponse is GET /devices/{identity}.
type deviceListResponse struct {
	Identity string         `json:"identity"`
	Devices  []idpkg.Device `json:"devices"`
}

func runDeviceList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devices list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to authenticate as")
	relayFlag := fs.String("relay", "", "relay to query (default: configured relay)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	relayURL, identityValue, priv, ok := deviceAuth(*useIdentity, *relayFlag, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		relayURL+"/devices/"+url.PathEscape(identityValue), nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := setOwnerAuth(req, relayURL, identityValue, priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, parseErrorResponse("device list failed", resp))
		return 1
	}
	var out deviceListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, stderr, out)
	}
	if len(out.Devices) == 0 {
		fmt.Fprintln(stdout, "no devices recorded yet")
		return 0
	}
	self := ""
	if d, err := loadDevice(); err == nil {
		self = d.DeviceID()
	}
	now := time.Now().UTC()
	for _, d := range out.Devices {
		marker := " "
		if d.ID == self {
			marker = "*"
		}
		name := d.Name
		if name == "" {
			name = "(unnamed)"
		}
		state := describeSync(d, now)
		if d.Revoked {
			state = "revoked " + d.RevokedAt
		}
		// Name first, then what and where it is, then when.
		fmt.Fprintf(stdout, "%s %-28s %-22s added %s  last seen %s  %s  %s\n",
			marker, name, describeDeviceClient(d), dateOnly(d.AddedAt), orDash(d.LastSeen), state, d.ID)
	}
	return 0
}

// describeDeviceClient reads "cli · macOS", "web · macOS · Safari" or, for a
// row from before clients reported themselves, just the kind.
func describeDeviceClient(d idpkg.Device) string {
	var parts []string
	for _, p := range []string{d.Client, d.Platform, d.Browser} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return d.Kind
	}
	return strings.Join(parts, " · ")
}

func dateOnly(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return "-"
}

// printJSON emits payload as indented JSON, reporting a marshal failure on
// stderr rather than printing a half-written document.
func printJSON(stdout, stderr io.Writer, payload any) int {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func orDash(v string) string {
	if v == "" {
		return "never"
	}
	return v
}

// describeSync renders the staleness line the epic asked for — "phone last
// synced 3 days ago" — from the server-side cursor timestamp.
func describeSync(d idpkg.Device, now time.Time) string {
	age, ok := d.Stale(now)
	if !ok {
		return "never synced"
	}
	switch {
	case age < time.Minute:
		return "synced just now"
	case age < time.Hour:
		return fmt.Sprintf("synced %d min ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("synced %d h ago", int(age.Hours()))
	default:
		return fmt.Sprintf("synced %d day(s) ago", int(age.Hours()/24))
	}
}

func runDeviceRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devices revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to authenticate as")
	relayFlag := fs.String("relay", "", "relay to query (default: configured relay)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur devices revoke <device-id>")
		return 1
	}
	deviceID := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	if !idpkg.ValidDeviceID(deviceID) {
		fmt.Fprintln(stderr, "device id must look like dev_… (see `poweur devices list`)")
		return 1
	}
	relayURL, identityValue, priv, ok := deviceAuth(*useIdentity, *relayFlag, stderr)
	if !ok {
		return 1
	}
	body, err := json.Marshal(map[string]string{"device_id": deviceID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		relayURL+"/devices/"+url.PathEscape(identityValue)+"/revoke", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if err := setOwnerAuth(req, relayURL, identityValue, priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, parseErrorResponse("device revoke failed", resp))
		return 1
	}
	var out struct {
		DeviceID string `json:"device_id"`
		Sessions int    `json:"sessions_revoked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, stderr, out)
	}
	fmt.Fprintf(stdout, "revoked %s: %d session(s)\n", out.DeviceID, out.Sessions)
	return 0
}

// deviceAuth loads the identity whose registry the device commands read.
func deviceAuth(useIdentity, relayFlag string, stderr io.Writer) (relayURL, identityValue string, priv ed25519.PrivateKey, ok bool) {
	cfg, identityValue, priv, loaded := loadIdentityKey(useIdentity, stderr)
	if !loaded {
		return "", "", nil, false
	}
	relayURL = cfg.RelayURL
	if relayFlag != "" {
		relayURL = relayFlag
	}
	return strings.TrimSuffix(relayURL, "/"), identityValue, priv, true
}

// setOwnerAuth signs a fresh challenge with the identity key and sets the
// owner headers the relay's owner endpoints expect.
func setOwnerAuth(req *http.Request, relayURL, identityValue string, priv ed25519.PrivateKey) error {
	challenge, err := FetchChallenge(req.Context(), relayURL, identityValue)
	if err != nil {
		return err
	}
	req.Header.Set("X-Poweur-Identity", identityValue)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(challenge.Challenge))))
	return nil
}
