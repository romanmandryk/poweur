package relay

import (
	"net"
	"net/http"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Handle availability (EPIC-018 E18-T2).
//
// The point of this endpoint is ordering: without it, a user learns their
// handle is taken only after a WebAuthn ceremony and a failed POST /identities.
// It answers with a *verdict* rather than a boolean, because "no" has half a
// dozen meanings and the user can only act on the right one.
//
// It is an enumeration oracle over the registered set, which a public registry
// is by design — DNS answers the same question to anyone who asks. The limit
// below is therefore about cost, not secrecy: it charges several units of the
// per-IP bucket so probing is proportionally more expensive than messaging.
const availabilityRateCost = 4

type availabilityResponse struct {
	Handle    string         `json:"handle"`
	Identity  string         `json:"identity"`
	Available bool           `json:"available"`
	Reason    string         `json:"reason"`
	Message   string         `json:"message"`
	Policy    map[string]any `json:"policy"`
}

func (s *Server) handleHostedAvailability(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if decision := s.rateLimit.AllowCost("availability:"+ip, availabilityRateCost); !decision.Allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			"too many availability lookups from this address")
		return
	}

	handle := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("handle")))
	if handle == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "handle is required")
		return
	}

	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if domain == "" && len(s.cfg.HostedDomains) > 0 {
		domain = strings.ToLower(strings.TrimSuffix(s.cfg.HostedDomains[0], "."))
	}

	policy := s.cfg.NamePolicy
	respond := func(reason idpkg.NameReason, message string) {
		writeJSON(w, http.StatusOK, availabilityResponse{
			Handle:    handle,
			Identity:  handle + "." + domain,
			Available: reason == idpkg.ReasonAvailable,
			Reason:    string(reason),
			Message:   message,
			Policy:    policy.Describe(),
		})
	}

	if domain == "" || !s.cfg.IsHostedDomain(handle+"."+domain) {
		respond(idpkg.ReasonDomainNotHosted, "This relay does not host identities under that domain.")
		return
	}

	// Policy first, registration last: answering "taken" for a name the policy
	// would have refused anyway would leak whether a reserved handle is also
	// registered.
	if err := policy.ValidateHandleLabel(handle); err != nil {
		respond(idpkg.ReasonOf(err), availabilityMessage(idpkg.ReasonOf(err), err.Error()))
		return
	}
	if s.identities.Exists(handle + "." + domain) {
		respond(idpkg.ReasonTaken, "That name is already taken.")
		return
	}
	respond(idpkg.ReasonAvailable, "That name is available.")
}

// availabilityMessage turns a reason into something a person can act on. The
// validator's own text is used where it already says the useful thing (the
// exact minimum length, for instance).
func availabilityMessage(reason idpkg.NameReason, detail string) string {
	switch reason {
	case idpkg.ReasonReserved:
		return "This name is reserved by the operator."
	case idpkg.ReasonBlocked:
		return "This name is not available."
	case idpkg.ReasonCharset:
		return "Use only a-z, 0-9 and hyphen."
	case idpkg.ReasonPunycode:
		return "Names starting with xn-- are not available."
	case idpkg.ReasonHyphen:
		return "Hyphens cannot start, end or double up."
	default:
		return detail
	}
}
