package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	idpkg "github.com/poweur/identity"
)

// `sys.abuse.report` handling (EPIC-007 E07-T5).
//
// Inbox policy lets a person refuse a sender. Nothing until now let them tell
// that sender's *operator*, which means a relay hosting a spammer finds out
// from a blocklist, a peering complaint, or never — long after the damage is
// distributed. This is the missing channel, and it is deliberately the
// smallest thing that closes it: a signed report, verified, counted, logged.
//
// Three decisions worth stating.
//
//   - **The report goes to the subject's relay, not to an inbox.** Its
//     recipient is an operator, who is not an identity and has no key to seal
//     an envelope to. `sys.abuse.report` therefore names a document POSTed to
//     an endpoint rather than a message; the registry entry is the same either
//     way, and `packages/identity/abuse.go` carries the format.
//   - **Only for locally hosted subjects.** A relay accepts complaints about
//     people it is accountable for and refuses to be a mailbox for the rest.
//     Otherwise this endpoint becomes a way to store arbitrary text on any
//     relay in the network.
//   - **Signed, and one report per reporter per subject per day.** An
//     anonymous or repeatable complaint stream is itself an abuse vector: it
//     would let one attacker manufacture a reputation. The signature names the
//     reporter, and the dedup means the count reflects *people*, not clicks.
//
// v1 is log + counter, exactly as the epic scopes it. Nothing here suspends an
// identity, and nothing is exported to other relays — what an operator does
// with a count is an operator decision, and building enforcement before anyone
// has run the counter would be guessing.

// abuseReportDedup is how long a reporter's complaint about one subject
// suppresses another from the same pair.
const abuseReportDedup = 24 * time.Hour

// maxAbuseReportBytes caps the request body; a report carries IDs and a note,
// never content.
const maxAbuseReportBytes = 16 * 1024

// abuseRetention is how long a subject's counter survives with no new report.
//
// It has to be finite: this map is fed by remote input, and a counter that is
// never evicted is a slow memory leak with a signature on it. Thirty days is
// chosen against what the number is *for* — an operator asking "is there a
// problem with this account now" — and a count nobody has added to in a month
// is not evidence of a current problem. The dedup ledger is pruned on its own,
// much shorter, window: once a reporter can report again, remembering that
// they once did suppresses nothing.
const abuseRetention = 30 * 24 * time.Hour

// abuseSubject is what the relay knows about complaints against one identity.
type abuseSubject struct {
	Subject   string         `json:"subject"`
	Total     int            `json:"total"`
	Reporters int            `json:"reporters"`
	Reasons   map[string]int `json:"reasons"`
	FirstAt   time.Time      `json:"first_at"`
	LastAt    time.Time      `json:"last_at"`
}

// abuseLog counts verified reports in memory. Memory-only on purpose: a
// counter that outlives the process implies a retention policy, an expiry, and
// a way to contest an entry — none of which v1 has decided.
type abuseLog struct {
	mu       sync.Mutex
	subjects map[string]*abuseSubject
	// lastSeen[subject][reporter] backs the per-day dedup.
	lastSeen map[string]map[string]time.Time
}

func newAbuseLog() *abuseLog {
	return &abuseLog{
		subjects: make(map[string]*abuseSubject),
		lastSeen: make(map[string]map[string]time.Time),
	}
}

// Record counts a verified report. Returns false when the same reporter
// already reported this subject inside the dedup window.
func (a *abuseLog) Record(report idpkg.AbuseReport, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	reporters, ok := a.lastSeen[report.Subject]
	if !ok {
		reporters = make(map[string]time.Time)
		a.lastSeen[report.Subject] = reporters
	}
	if last, seen := reporters[report.Reporter]; seen && now.Sub(last) < abuseReportDedup {
		return false
	}
	reporters[report.Reporter] = now

	entry, ok := a.subjects[report.Subject]
	if !ok {
		entry = &abuseSubject{Subject: report.Subject, Reasons: map[string]int{}, FirstAt: now}
		a.subjects[report.Subject] = entry
	}
	entry.Total++
	entry.Reasons[report.Reason]++
	entry.LastAt = now
	entry.Reporters = len(reporters)
	return true
}

// Subject returns a copy of what is known about one subject.
func (a *abuseLog) Subject(subject string) (abuseSubject, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.subjects[subject]
	if !ok {
		return abuseSubject{}, false
	}
	out := *entry
	out.Reasons = make(map[string]int, len(entry.Reasons))
	for k, v := range entry.Reasons {
		out.Reasons[k] = v
	}
	return out, true
}

// Subjects returns every counted subject, most-reported first. Used by the
// operator-facing summary and by tests.
func (a *abuseLog) Subjects() []abuseSubject {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]abuseSubject, 0, len(a.subjects))
	for _, entry := range a.subjects {
		copied := *entry
		copied.Reasons = make(map[string]int, len(entry.Reasons))
		for k, v := range entry.Reasons {
			copied.Reasons[k] = v
		}
		out = append(out, copied)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// prune retires subjects nobody has reported in abuseRetention. Called from
// the relay's janitor.
//
// The counter and its dedup ledger are dropped together, never separately.
// `Reporters` is derived from the ledger — the number of distinct people who
// have reported this subject — so evicting a reporter from the ledger while
// keeping the counter would make the next report *lower* the reporter count.
// A ledger entry outliving its 24-hour dedup window is a few dozen bytes; a
// count that quietly under-reports how many people complained is the one
// number an operator would actually act on being wrong.
func (a *abuseLog) prune(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for subject, entry := range a.subjects {
		if now.Sub(entry.LastAt) > abuseRetention {
			delete(a.subjects, subject)
			delete(a.lastSeen, subject)
		}
	}
	// A ledger with no counter behind it can only come from a subject whose
	// every report was pruned; drop it too rather than leak the key.
	for subject := range a.lastSeen {
		if _, ok := a.subjects[subject]; !ok {
			delete(a.lastSeen, subject)
		}
	}
}

// handleAbuseReport accepts a signed `sys.abuse.report` about an identity this
// relay hosts.
func (s *Server) handleAbuseReport(w http.ResponseWriter, r *http.Request) {
	raw := http.MaxBytesReader(w, r.Body, maxAbuseReportBytes)
	defer raw.Close()

	var report idpkg.AbuseReport
	if err := json.NewDecoder(raw).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report", "malformed abuse report")
		return
	}
	parsed, err := idpkg.ParseAbuseReport(mustMarshal(report))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report", err.Error())
		return
	}
	report = parsed

	// This relay speaks for the identities it hosts and for nobody else. A
	// report about a stranger belongs on the stranger's relay — accepting it
	// here would turn the endpoint into free storage on every relay alive.
	if !s.isLocalIdentity(r.Context(), report.Subject) {
		writeError(w, http.StatusForbidden, "not_authorized",
			"this relay does not host the reported identity; send the report to their relay")
		return
	}

	// Verified reporter or nothing: an unauthenticated complaint stream is a
	// reputation-forgery tool, not a defence.
	reporterKey, err := s.resolveIdentityPublicKey(r.Context(), report.Reporter)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve reporter identity key")
		return
	}
	if err := report.VerifySignature(reporterKey); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "abuse report signature verification failed")
		return
	}

	// Charged to the reporter *after* the signature check, like every other
	// verified-sender path: charging an unverified field would let anyone
	// exhaust somebody else's quota.
	if decision := s.rateLimit.Allow(report.Reporter); !decision.Allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":    "rate_limit_exceeded",
			"scope":    decision.Scope,
			"window":   decision.Window,
			"limit":    decision.Limit,
			"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
		})
		return
	}

	counted := s.abuse.Record(report, time.Now().UTC())
	if !counted {
		// Not an error: the reporter's complaint was heard the first time.
		log.Printf("abuse: duplicate report from %s about %s (reason=%s) — not counted",
			report.Reporter, report.Subject, report.Reason)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "duplicate"})
		return
	}
	entry, _ := s.abuse.Subject(report.Subject)
	// The note is the reporter's own words and the message IDs are pointers
	// into this relay's own logs; no reported content is logged, because
	// there is none to log.
	log.Printf("abuse: report from %s about %s (reason=%s, evidence=%d ids) — %d report(s) from %d reporter(s)",
		report.Reporter, report.Subject, report.Reason, len(report.MessageIDs), entry.Total, entry.Reporters)

	// The response says only that the report landed. Returning the count
	// would let anyone probe how disliked a stranger is, and turn the
	// endpoint into the reputation oracle it deliberately is not.
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "recorded"})
}

// mustMarshal re-encodes a decoded report so ParseAbuseReport does the single
// validation pass, keeping the accepted shape identical to the one clients and
// tests validate against.
func mustMarshal(report idpkg.AbuseReport) []byte {
	raw, err := json.Marshal(report)
	if err != nil {
		return []byte("{}")
	}
	return raw
}
