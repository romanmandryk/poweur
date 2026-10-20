package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// EmailAddressOf reports the email address an identity receives mail at,
// from public data only, the way an email bridge decides (PCP-0010):
//
//   - its signed opt-in (.poweur/public/email.json) names the address;
//   - without one, an identity whose home relay offers an email bridge and
//     says it has not opted out (capabilities.json features.email_receive =
//     "on") receives at the address its name gives it (EmailAddressFor).
//
// ok is false when the identity receives no email. An error means the
// answer could not be read, not that there is no address.
func EmailAddressOf(ctx context.Context, id string, opts ResolveOptions) (address string, ok bool, err error) {
	id = strings.ToLower(strings.TrimSpace(id))
	res, err := Resolve(ctx, id, opts)
	if err != nil {
		return "", false, err
	}
	pub, err := ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		return "", false, nil
	}
	raw, err := FetchPublicFile(ctx, id, WellKnownEmailPath, opts)
	switch {
	case err == nil:
		optIn, perr := ParseEmailOptIn(raw)
		if perr != nil || !strings.EqualFold(optIn.Identity, id) || optIn.VerifySignature(pub) != nil {
			return "", false, nil
		}
		return strings.ToLower(optIn.Address), optIn.Address != "", nil
	case !errors.Is(err, ErrPublicFileAbsent):
		return "", false, err
	}
	derived, err := EmailAddressFor(id)
	if err != nil {
		return "", false, nil
	}
	raw, err = FetchPublicFile(ctx, id, WellKnownCapabilitiesPath, opts)
	if errors.Is(err, ErrPublicFileAbsent) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var caps struct {
		Endpoints map[string]any `json:"endpoints"`
		Features  map[string]any `json:"features"`
	}
	if json.Unmarshal(raw, &caps) != nil {
		return "", false, nil
	}
	bridge, _ := caps.Endpoints["email_bridge"].(string)
	receive, _ := caps.Features["email_receive"].(string)
	if bridge == "" || receive != "on" {
		return "", false, nil
	}
	return derived, true, nil
}
