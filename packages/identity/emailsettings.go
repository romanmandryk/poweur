package identity

import (
	"fmt"
	"strings"
	"time"
)

// EmailSettings is the payload of `email.settings` (user → bridge): who may
// email the user. It travels encrypted to the bridge and stays private —
// unlike email.json, nobody else can read whom a user blocked. Absent
// settings mean everyone may (the permissive default).
//
// Filtered mail is accepted and dropped silently at the bridge: the sender
// learns nothing, and no bounce goes out. The bridge keeps only HMACs of the
// lists; the user's own copy is the latest `email.settings` in their archive
// (it is archived like any message they send).
type EmailSettings struct {
	Version int `json:"version"`
	// Receive is "everyone" (default) or "contacts": then only Allow, people
	// the user has emailed through the bridge, replies to their own mail and
	// delivery reports get through.
	Receive string `json:"receive,omitempty"`
	// Allow and Block hold addresses ("ann@example.com") or whole domains
	// ("@example.com", which also covers its subdomains). Block wins.
	Allow     []string `json:"allow,omitempty"`
	Block     []string `json:"block,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

// Receive modes.
const (
	EmailReceiveEveryone = "everyone"
	EmailReceiveContacts = "contacts"
)

// MaxEmailFilterEntries bounds Allow and Block each.
const MaxEmailFilterEntries = 2000

// Validate checks the settings and normalises the lists in place.
func (s *EmailSettings) Validate() error {
	if s.Version != EmailPayloadVersion {
		return fmt.Errorf("unsupported email settings version %d", s.Version)
	}
	switch s.Receive {
	case "", EmailReceiveEveryone, EmailReceiveContacts:
	default:
		return fmt.Errorf("receive must be %q or %q", EmailReceiveEveryone, EmailReceiveContacts)
	}
	if _, err := time.Parse(time.RFC3339, s.UpdatedAt); err != nil {
		return fmt.Errorf("updated_at must be RFC3339: %w", err)
	}
	var err error
	if s.Allow, err = normalizeFilter("allow", s.Allow); err != nil {
		return err
	}
	s.Block, err = normalizeFilter("block", s.Block)
	return err
}

func normalizeFilter(name string, list []string) ([]string, error) {
	if len(list) > MaxEmailFilterEntries {
		return nil, fmt.Errorf("%s has more than %d entries", name, MaxEmailFilterEntries)
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, entry := range list {
		e := strings.ToLower(strings.TrimSpace(entry))
		if e == "" {
			continue
		}
		at := strings.LastIndex(e, "@")
		if at < 0 || len(e) > 320 || ValidateIdentityName(e[at+1:]) != nil || strings.ContainsAny(e[:at], " <>\"") {
			return nil, fmt.Errorf("%s: %q is neither an address nor @domain", name, entry)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// filterMatches reports whether address is in list, by address or domain.
func filterMatches(list []string, address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false
	}
	domain := address[at+1:]
	for _, e := range list {
		if e == address {
			return true
		}
		if strings.HasPrefix(e, "@") {
			d := e[1:]
			if domain == d || strings.HasSuffix(domain, "."+d) {
				return true
			}
		}
	}
	return false
}

// Blocks reports whether mail from address is blocked.
func (s EmailSettings) Blocks(address string) bool { return filterMatches(s.Block, address) }

// Allows reports whether address is on the allow list.
func (s EmailSettings) Allows(address string) bool { return filterMatches(s.Allow, address) }

// ContactsOnly reports the "contacts" receive mode.
func (s EmailSettings) ContactsOnly() bool { return s.Receive == EmailReceiveContacts }
