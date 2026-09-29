package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Public self-description files (EPIC-006 E06-T2), served world-readable
// from .poweur/public/ (they back /.well-known/poweur/):
//
//	profile.json       human-facing "who am I"
//	capabilities.json  machine-facing "what do I speak"
//
// Both are owner-written; the relay validates structure on write.

// MaxProfileLink bounds the links list.
const MaxProfileLinks = 32

// ProfileLink is one labeled URL on a profile.
type ProfileLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// IdentityPageSettings controls the relay-rendered public identity page.
// Pointer booleans preserve the distinction between an omitted field (use the
// forward-compatible default) and an explicit false. The document is public;
// enforcement of anonymous ingress remains in the private inbox policy.
type IdentityPageSettings struct {
	Enabled                    *bool `json:"enabled,omitempty"`
	Indexable                  *bool `json:"indexable,omitempty"`
	AdvertiseAnonymousMessages *bool `json:"advertise_anonymous_messages,omitempty"`
}

// EnabledOrDefault reports whether the generated page is enabled. Absence is
// deliberately on so every existing identity gains a page without migration.
func (s *IdentityPageSettings) EnabledOrDefault() bool {
	return s == nil || s.Enabled == nil || *s.Enabled
}

// IndexableOrDefault reports whether crawlers may index the generated page.
// Off unless the owner turned it on: a profile written for contacts should
// not become searchable without being asked.
func (s *IdentityPageSettings) IndexableOrDefault() bool {
	return s != nil && s.Indexable != nil && *s.Indexable
}

// AdvertisesAnonymousMessages reports the public presentation preference. It
// never substitutes for inbox-policy enforcement.
func (s *IdentityPageSettings) AdvertisesAnonymousMessages() bool {
	return s != nil && s.AdvertiseAnonymousMessages != nil && *s.AdvertiseAnonymousMessages
}

// Profile is the schema of .poweur/public/profile.json.
type Profile struct {
	Version     int    `json:"version"`
	DisplayName string `json:"display_name,omitempty"`
	// Avatar names an image file in the identity's .poweur/public/ (e.g.
	// "avatar.png"), served at /.well-known/poweur/<name> — never a URL, so
	// rendering somebody's profile cannot become a request to a host they
	// chose.
	Avatar       string                `json:"avatar,omitempty"`
	Bio          string                `json:"bio,omitempty"`
	Links        []ProfileLink         `json:"links,omitempty"`
	Locale       string                `json:"locale,omitempty"`
	IdentityPage *IdentityPageSettings `json:"identity_page,omitempty"`
}

// Validate checks the profile document.
func (p Profile) Validate() error {
	if p.Version != 0 && p.Version != 1 {
		return fmt.Errorf("unsupported profile version %d", p.Version)
	}
	if len(p.DisplayName) > 256 {
		return fmt.Errorf("display_name too long (max 256)")
	}
	if len(p.Bio) > 4096 {
		return fmt.Errorf("bio too long (max 4096)")
	}
	if p.Avatar != "" && !ValidAvatarName(p.Avatar) {
		return fmt.Errorf("avatar must be an image file name like avatar.png (got %q)", p.Avatar)
	}
	if len(p.Links) > MaxProfileLinks {
		return fmt.Errorf("too many links (max %d)", MaxProfileLinks)
	}
	for i, l := range p.Links {
		if strings.TrimSpace(l.URL) == "" {
			return fmt.Errorf("link %d: url is required", i)
		}
	}
	return nil
}

// ParseProfile decodes and validates profile.json.
func ParseProfile(raw []byte) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return Profile{}, fmt.Errorf("invalid profile.json: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		if page, ok := fields["identity_page"]; ok && bytes.Equal(bytes.TrimSpace(page), []byte("null")) {
			return Profile{}, fmt.Errorf("identity_page must be an object")
		}
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Capabilities is the schema of .poweur/public/capabilities.json:
// supported protocol features and endpoint hints, superseding the
// `_poweur-caps` TXT sketch for web-resolved identities.
type Capabilities struct {
	Version int `json:"version"`
	// Features maps a feature name to its version/variant, e.g.
	// {"messaging": "v1", "files": "webdav", "sync": "v1", "sign_in": ""}.
	// Unknown feature names are allowed (forward compatibility).
	Features map[string]string `json:"features,omitempty"`
	// Endpoints optionally overrides discovery, e.g. {"dav": "https://…"}.
	Endpoints map[string]string `json:"endpoints,omitempty"`
}

// Validate checks the capabilities document.
func (c Capabilities) Validate() error {
	if c.Version != 0 && c.Version != 1 {
		return fmt.Errorf("unsupported capabilities version %d", c.Version)
	}
	if len(c.Features) > 64 || len(c.Endpoints) > 64 {
		return fmt.Errorf("too many entries (max 64 each)")
	}
	for k := range c.Features {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("empty feature name")
		}
	}
	return nil
}

// ParseCapabilities decodes and validates capabilities.json.
func ParseCapabilities(raw []byte) (Capabilities, error) {
	var c Capabilities
	if err := json.Unmarshal(raw, &c); err != nil {
		return Capabilities{}, fmt.Errorf("invalid capabilities.json: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Capabilities{}, err
	}
	return c, nil
}

// AppManifest is the schema of /apps/<app-id>/manifest.json (EPIC-006
// E06-T3): every app namespace must self-describe at its root. app_id is
// reverse-DNS of the vendor (e.g. net.poweur.tasks).
type AppManifest struct {
	AppID         string `json:"app_id"`
	Name          string `json:"name"`
	Vendor        string `json:"vendor,omitempty"`
	SchemaVersion string `json:"schema_version,omitempty"`
	DocsURL       string `json:"docs_url,omitempty"`
}

// Validate checks the manifest document.
func (m AppManifest) Validate() error {
	id := strings.TrimSpace(m.AppID)
	if id == "" {
		return fmt.Errorf("app_id is required")
	}
	if !strings.Contains(id, ".") || strings.ContainsAny(id, "/\\ ") {
		return fmt.Errorf("app_id must be reverse-DNS (e.g. net.poweur.tasks)")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

// ParseAppManifest decodes and validates an app manifest.json.
func ParseAppManifest(raw []byte) (AppManifest, error) {
	var m AppManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return AppManifest{}, fmt.Errorf("invalid manifest.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return AppManifest{}, err
	}
	return m, nil
}

// avatarExtensions are the image types an avatar may have; the relay serves
// exactly these from .poweur/public/.
var avatarExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

// ValidAvatarName reports whether name is a flat file name in
// .poweur/public/: lowercase letters, digits, '-' and '_', then one image
// extension.
func ValidAvatarName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	dot := strings.IndexByte(name, '.')
	if dot <= 0 || strings.Count(name, ".") != 1 {
		return false
	}
	for _, c := range name[:dot] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	for _, ext := range avatarExtensions {
		if name[dot:] == ext {
			return true
		}
	}
	return false
}
