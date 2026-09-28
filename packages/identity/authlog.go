package identity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The consent record (EPIC-008 E08-T3).
//
// Every approval a signer produces is appended to the user's own tree at
// AuthLogPath. Two reasons it lives there and not on a relay's side:
//
//   - It is the user's record of what they gave away, so it belongs in their
//     storage, syncs across their devices like any other file, and survives
//     the relay.
//   - E08-T4's "connected apps" UI is a rendering of this file plus the
//     relay's registrations — not a separate database that could disagree
//     with what actually happened.
//
// The format is JSON Lines: one record per line, append-only. A line that
// does not parse is skipped rather than failing the read — a truncated write
// on one device must not cost the user the rest of their history.

// AuthLogPath is where consent records live in the owner's tree.
const AuthLogPath = ".poweur/private/logs/auth.log"

// MaxAuthLogBytes caps the file. Past it, the oldest records are dropped on
// the next append: a consent log that grows without bound eventually stops
// being writable at all, and recent consent is what a user needs.
const MaxAuthLogBytes = 256 * 1024

// AuthLogRecord is one approval, as the signer saw it. It records what was
// *shown to the user* alongside what was signed: `app_name` and `verified`
// come from the RP metadata fetch, not from the request, so a later reader
// can tell an approval given against a verified origin from one given to a
// request whose origin the signer could not check.
type AuthLogRecord struct {
	At        string `json:"at"`
	Action    string `json:"action"`
	Audience  string `json:"audience"`
	AppID     string `json:"app_id,omitempty"`
	AppName   string `json:"app_name,omitempty"`
	RequestID string `json:"request_id"`
	// Verified is true when the signer fetched and validated the RP's
	// /.well-known/poweur.json at `audience` before showing anything.
	Verified bool `json:"verified"`
	// KeyID is "identity" or "session:<id>" — which key the user spent.
	KeyID string `json:"key_id"`
	// Scopes is what was granted, normalized and sorted.
	Scopes []string `json:"scopes,omitempty"`
	// Signer names the device or program that produced the approval
	// ("cli", "web", "ios"), so a user reading the log can tell where an
	// approval came from.
	Signer string `json:"signer,omitempty"`
	// Statement is the line the user was shown before approving.
	Statement string `json:"statement,omitempty"`
	// Denied records an approval the user refused. A consent log that only
	// holds yeses cannot answer "did I ever say no to this?".
	Denied bool `json:"denied,omitempty"`
}

// NewAuthLogRecord builds a record from an approval the signer just produced.
func NewAuthLogRecord(resp SignInResponse, appName, signer string, verified bool, at time.Time) AuthLogRecord {
	appID, _ := SignInAppID(resp.Audience)
	return AuthLogRecord{
		At:        at.UTC().Format(time.RFC3339),
		Action:    resp.Action,
		Audience:  resp.Audience,
		AppID:     appID,
		AppName:   appName,
		RequestID: resp.RequestID,
		Verified:  verified,
		KeyID:     resp.KeyID,
		Scopes:    resp.Scopes,
		Signer:    signer,
		Statement: resp.Statement,
	}
}

// Validate rejects a record that would corrupt the line-oriented file or
// that records nothing useful.
func (r AuthLogRecord) Validate() error {
	if strings.TrimSpace(r.At) == "" {
		return fmt.Errorf("%w: consent record needs a timestamp", ErrSignInMalformed)
	}
	if _, err := time.Parse(time.RFC3339, r.At); err != nil {
		return fmt.Errorf("%w: consent record timestamp must be RFC3339", ErrSignInMalformed)
	}
	if strings.TrimSpace(r.Audience) == "" {
		return fmt.Errorf("%w: consent record needs an audience", ErrSignInMalformed)
	}
	if _, err := NormalizeOrigin(r.Audience); err != nil {
		return err
	}
	return nil
}

// AppendAuthLog returns the new contents of the log with rec appended.
//
// The whole file is rewritten because the only durable store here is a DAV
// PUT, which has no append. That is fine at this size and it is why
// MaxAuthLogBytes exists: past the cap the oldest whole records are dropped,
// never a partial line.
func AppendAuthLog(existing []byte, rec AuthLogRecord) ([]byte, error) {
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	// json.Marshal never emits a raw newline, so one record is always one
	// line; the guard is here so a future field cannot silently break that.
	if bytes.ContainsAny(line, "\n\r") {
		return nil, fmt.Errorf("%w: consent record does not fit on one line", ErrSignInMalformed)
	}

	var buf bytes.Buffer
	trimmed := bytes.TrimRight(existing, "\n")
	if len(trimmed) > 0 {
		buf.Write(trimmed)
		buf.WriteByte('\n')
	}
	buf.Write(line)
	buf.WriteByte('\n')

	if buf.Len() <= MaxAuthLogBytes {
		return buf.Bytes(), nil
	}
	// Over the cap: keep the newest records that fit, oldest dropped first.
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	for len(lines) > 1 {
		lines = lines[1:]
		size := 0
		for _, l := range lines {
			size += len(l) + 1
		}
		if size <= MaxAuthLogBytes {
			break
		}
	}
	out := bytes.Join(lines, []byte("\n"))
	return append(out, '\n'), nil
}

// ParseAuthLog reads a consent log. Unparseable lines are skipped: a
// truncated write on one device must not cost the user the rest of their
// history.
func ParseAuthLog(raw []byte) []AuthLogRecord {
	var out []AuthLogRecord
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec AuthLogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Validate() != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// AuthLogByApp groups records by app id, newest first within each group.
// This is the shape the "connected apps" UI renders (E08-T4).
func AuthLogByApp(records []AuthLogRecord) map[string][]AuthLogRecord {
	byApp := make(map[string][]AuthLogRecord)
	for i := len(records) - 1; i >= 0; i-- {
		key := records[i].AppID
		if key == "" {
			key = records[i].Audience
		}
		byApp[key] = append(byApp[key], records[i])
	}
	return byApp
}
