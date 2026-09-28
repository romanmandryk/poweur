package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
)

// System files (EPIC-020 E20-T6) are the small documents in an identity's
// drive under `.poweur/`: contacts and inbox policy the relay enforces
// (`.poweur/relay/`), the public profile and avatar (`.poweur/public/`) and
// the relay's own records such as the device registry (`.poweur/state/`).
// The CLI reads and writes them through the relay's owner API,
// `/identities/{identity}/system/{path}`, authenticated with a challenge
// signed by the identity key.
//
// Owner-only encrypted records (`.poweur/private/`: message history, the
// sign-in log) wait for storage v2's drive; writes there fail with
// errStorageUnavailable.

// errStorageUnavailable is returned for documents that need storage v2.
var errStorageUnavailable = errors.New("this needs the new storage (EPIC-020), which is not available yet")

func sysFileURL(relayURL, identity, path string) string {
	return strings.TrimSuffix(relayURL, "/") + "/identities/" + url.PathEscape(identity) + "/system/" + path
}

// readSysFile returns a system document of identity, with an HTTP-style
// status: 200 with the bytes, or 404 when the document is absent.
func readSysFile(ctx context.Context, relayURL, identity string, priv ed25519.PrivateKey, path string) ([]byte, int, error) {
	if strings.HasPrefix(path, ".poweur/private/") {
		return nil, http.StatusNotFound, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sysFileURL(relayURL, identity, path), nil)
	if err != nil {
		return nil, 0, err
	}
	if err := setOwnerAuth(req, relayURL, identity, priv); err != nil {
		return nil, 0, err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, http.StatusNotFound, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, parseErrorResponse("read "+path+" failed", resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return raw, http.StatusOK, err
}

// writeSysFile replaces a system document of identity.
func writeSysFile(ctx context.Context, relayURL, identity string, priv ed25519.PrivateKey, path string, body []byte) error {
	if strings.HasPrefix(path, ".poweur/private/") {
		return errStorageUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sysFileURL(relayURL, identity, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := setOwnerAuth(req, relayURL, identity, priv); err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parseErrorResponse("write "+path+" failed", resp)
	}
	return nil
}

// loadIdentityKey loads the configured (or --use-identity) identity, its
// relay and its signing key.
func loadIdentityKey(useIdentity string, stderr io.Writer) (cfg config.Config, identityValue string, priv ed25519.PrivateKey, ok bool) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, "", nil, false
	}
	identityValue = resolveIdentity(useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured (run `poweur identity create` or pass --use-identity)")
		return cfg, "", nil, false
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return cfg, "", nil, false
	}
	priv, err = identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, "", nil, false
	}
	return cfg, identityValue, priv, true
}

// loadSysSession loads the identity and key for reading and writing its own
// system files.
func loadSysSession(useIdentity string, stderr io.Writer) (relayURL, identityValue string, priv ed25519.PrivateKey, ok bool) {
	cfg, identityValue, priv, ok := loadIdentityKey(useIdentity, stderr)
	if !ok {
		return "", "", nil, false
	}
	return cfg.RelayURL, identityValue, priv, true
}
