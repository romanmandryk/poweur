package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	gopath "path"
	"strings"

	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
)

// poweur-sys write validation (EPIC-006 E06-T1). Known system documents are
// schema-checked on PUT so a malformed contacts.json can never take down
// policy enforcement; unknown files inside poweur-sys are preserved
// untouched (forward compatibility — relays and sync clients MUST NOT
// reject or delete files they don't recognize).

// maxSysDocBytes caps any validated system document.
const maxSysDocBytes = 64 * 1024

// sysValidators maps exact tree paths to their validators.
var sysValidators = map[string]func([]byte) error{
	files.SysRelay + "/contacts.json": func(b []byte) error {
		_, err := idpkg.ParseContactsFile(b)
		return err
	},
	files.SysRelay + "/inbox-policy.json": func(b []byte) error {
		_, err := idpkg.ParseInboxPolicy(b)
		return err
	},
	files.SysRelay + "/app-passwords.json": func(b []byte) error {
		_, err := idpkg.ParseAppPasswordsFile(b)
		return err
	},
	// devices.json is relay-managed, so an owner PUT is refused by the
	// permission engine before it ever gets here. The validator is
	// registered anyway: it is the one enforced definition of the schema,
	// and it means the relay's own writes and a hand-repaired file are held
	// to the same standard.
	files.SysRelay + "/devices.json": func(b []byte) error {
		_, err := idpkg.ParseDevicesFile(b)
		return err
	},
	files.SysPublic + "/profile.json": func(b []byte) error {
		_, err := idpkg.ParseProfile(b)
		return err
	},
	files.SysPublic + "/capabilities.json": func(b []byte) error {
		_, err := idpkg.ParseCapabilities(b)
		return err
	},
}

// sysWriteValidator returns the validator for a clean tree path, or nil when
// the path is not a schema-governed document.
func sysWriteValidator(clean string) func([]byte) error {
	if v, ok := sysValidators[clean]; ok {
		return v
	}
	dir := gopath.Dir(clean)
	switch {
	case dir == files.SysRelay+"/shares" && strings.HasSuffix(clean, ".json"):
		return func(b []byte) error {
			_, err := idpkg.ParseShareGrant(b)
			return err
		}
	case dir == files.SysRelay+"/groups" && strings.HasSuffix(clean, ".json"):
		return func(b []byte) error {
			_, err := idpkg.ParseShareGroup(b)
			return err
		}
	// /apps/<app-id>/manifest.json must be a valid app manifest whose
	// app_id matches its directory (E06-T3).
	case files.TopRoot(clean) == files.RootApps && gopath.Base(clean) == "manifest.json" &&
		strings.Count(clean, "/") == 2:
		return func(b []byte) error {
			m, err := idpkg.ParseAppManifest(b)
			if err != nil {
				return err
			}
			appDir := strings.Split(clean, "/")[1]
			if !strings.EqualFold(m.AppID, appDir) {
				return fmt.Errorf("manifest app_id %q must match its directory %q", m.AppID, appDir)
			}
			return nil
		}
	}
	return nil
}

// checkSysWrite validates a PUT to a schema-governed document, replacing
// r.Body so the DAV handler still sees the full stream. Returns false when
// the request was rejected (response already written).
func (s *Server) checkSysWrite(w http.ResponseWriter, r *http.Request, clean string) bool {
	if r.Method != http.MethodPut {
		return true
	}
	validate := sysWriteValidator(clean)
	if validate == nil {
		return true
	}
	if r.ContentLength > maxSysDocBytes {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document",
			fmt.Sprintf("system document exceeds %d bytes", maxSysDocBytes))
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSysDocBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read body")
		return false
	}
	if len(body) > maxSysDocBytes {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document",
			fmt.Sprintf("system document exceeds %d bytes", maxSysDocBytes))
		return false
	}
	if err := validate(body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document", err.Error())
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}
