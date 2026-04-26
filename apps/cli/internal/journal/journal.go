// Package journal persists the local-side delivery state ("ticks") of
// outbound messages so the CLI can render WhatsApp-style status checks
// after the fact, even after the relay has dropped its in-memory inbox.
//
// Storage is an append-only JSON-Lines file at
// ~/.eurything/pending/<identity>.jsonl, one record per state transition.
// Reads collapse the log into the latest state per message_id. This is
// resilient to concurrent CLI invocations (every record is a single short
// write) and trivially auditable by tail-ing the file.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State enumerates the per-message tick states the CLI tracks locally.
// Mirrors the protocol's progression: queued → home-relay accepted (tick
// 0.5, only when --via-home-relay is in use) → recipient relay accepted
// (tick 1) → recipient client decrypted (tick 2). Failure is sticky: once
// a message lands in "failed" no further state transitions are recorded.
type State string

const (
	StateQueued                  State = "queued"
	StateDeliveredHomeRelay      State = "delivered_home_relay"
	StateDeliveredRecipientRelay State = "delivered_recipient_relay"
	StateDeliveredClient         State = "delivered_client"
	StateFailed                  State = "failed"
)

// Entry is one line in the journal — a single state transition for a
// specific message id, with enough context that the inbox poll can
// correlate inbound acks back to outbound messages without consulting any
// other store.
type Entry struct {
	MessageID    string    `json:"message_id"`
	Sender       string    `json:"sender"`
	Recipient    string    `json:"recipient"`
	Timestamp    time.Time `json:"timestamp"`
	State        State     `json:"state"`
	Detail       string    `json:"detail,omitempty"`
	ViaHomeRelay bool      `json:"via_home_relay,omitempty"`
}

// Status is the collapsed view of a single message's history: the latest
// state plus a chronologically-ordered list of every transition.
type Status struct {
	MessageID    string    `json:"message_id"`
	Sender       string    `json:"sender"`
	Recipient    string    `json:"recipient"`
	State        State     `json:"state"`
	UpdatedAt    time.Time `json:"updated_at"`
	ViaHomeRelay bool      `json:"via_home_relay,omitempty"`
	History      []Entry   `json:"history"`
}

// Dir returns the pending-journal directory under the user's home.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".eurything", "pending"), nil
}

// Path returns the journal file path for a given identity.
func Path(identity string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identity+".jsonl"), nil
}

// Append writes a single entry to the journal file, creating the file
// (and parent directory) if needed. Atomic at the OS level for
// short-enough writes — a JSON-encoded Entry comfortably fits in a
// single page on every supported filesystem.
func Append(entry Entry) error {
	if entry.MessageID == "" {
		return errors.New("journal: message_id required")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	path, err := Path(entry.Sender)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

// Load returns every entry persisted for a given identity in chronological
// order (file order). Missing files are not an error; the caller gets a nil
// slice.
func Load(identity string) ([]Entry, error) {
	path, err := Path(identity)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("journal: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// Statuses collapses the journal for an identity into one Status per
// message_id, with the latest state taking precedence. Order of the
// returned slice mirrors the order of first appearance in the file.
func Statuses(identity string) ([]Status, error) {
	entries, err := Load(identity)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	var out []Status
	for _, e := range entries {
		if pos, ok := index[e.MessageID]; ok {
			s := &out[pos]
			s.History = append(s.History, e)
			if e.State != "" && stateRank(e.State) >= stateRank(s.State) {
				s.State = e.State
				s.UpdatedAt = e.Timestamp
			}
			if e.ViaHomeRelay {
				s.ViaHomeRelay = true
			}
			continue
		}
		index[e.MessageID] = len(out)
		out = append(out, Status{
			MessageID:    e.MessageID,
			Sender:       e.Sender,
			Recipient:    e.Recipient,
			State:        e.State,
			UpdatedAt:    e.Timestamp,
			ViaHomeRelay: e.ViaHomeRelay,
			History:      []Entry{e},
		})
	}
	return out, nil
}

// FindStatus returns the Status for one message_id (or nil if not found).
func FindStatus(identity, messageID string) (*Status, error) {
	statuses, err := Statuses(identity)
	if err != nil {
		return nil, err
	}
	for i := range statuses {
		if statuses[i].MessageID == messageID {
			return &statuses[i], nil
		}
	}
	return nil, nil
}

// stateRank orders states for "latest wins" collapse. failed is treated
// as the terminal/highest-rank state so it sticks once recorded.
func stateRank(s State) int {
	switch s {
	case StateQueued:
		return 1
	case StateDeliveredHomeRelay:
		return 2
	case StateDeliveredRecipientRelay:
		return 3
	case StateDeliveredClient:
		return 4
	case StateFailed:
		return 5
	}
	return 0
}
