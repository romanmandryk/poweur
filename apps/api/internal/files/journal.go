package files

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Journal ops (E04-T1). A move is journaled as delete(old) + put/mkdir per
// moved entry — documented moves-as-delete+put for v1.
const (
	OpPut    = "put"
	OpMkdir  = "mkdir"
	OpDelete = "delete"
)

// Journal retention: records beyond either bound are compacted away and
// clients whose cursor predates the compaction point must full-resync via
// the manifest.
const (
	journalMaxRecords = 10000
	journalRetention  = 30 * 24 * time.Hour
)

// JournalRecord is one entry in the per-identity changes journal (E04-T1).
// A delete record covers the path and everything under it.
type JournalRecord struct {
	ChangeID int64     `json:"change_id"`
	Op       string    `json:"op"`
	Path     string    `json:"path"`
	ETag     string    `json:"etag,omitempty"`
	Size     int64     `json:"size,omitempty"`
	ModTime  time.Time `json:"mtime,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Time     time.Time `json:"time"`
}

// journalHeader is the first line of the NDJSON journal file.
type journalHeader struct {
	Journal          int   `json:"journal"`
	CompactedThrough int64 `json:"compacted_through"`
}

// journalState is the in-memory per-identity journal.
type journalState struct {
	// compactedThrough is the highest change_id ever discarded by
	// compaction (0 = nothing discarded). A cursor below this value cannot
	// be served incrementally.
	compactedThrough int64
	records          []JournalRecord
}

func (ix *Index) journalPath(identity string) (string, error) {
	home, err := ix.homeDir(identity)
	if err != nil || home == "" {
		return "", err
	}
	return filepath.Join(home, "meta", "sync-journal.ndjson"), nil
}

// journal loads (lazily) the identity's journal. Caller holds ix.mu.
func (ix *Index) journal(identity string) *journalState {
	key := strings.ToLower(identity)
	if j, ok := ix.journals[key]; ok {
		return j
	}
	j := &journalState{}
	if p, err := ix.journalPath(identity); err == nil && p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			lines := strings.Split(string(raw), "\n")
			for i, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if i == 0 {
					var h journalHeader
					if json.Unmarshal([]byte(line), &h) == nil && h.Journal >= 1 {
						j.compactedThrough = h.CompactedThrough
						continue
					}
				}
				var rec JournalRecord
				if json.Unmarshal([]byte(line), &rec) == nil && rec.ChangeID > 0 {
					j.records = append(j.records, rec)
				}
			}
		}
	}
	ix.journals[key] = j
	if ix.compactLocked(identity, j) {
		ix.persistJournalLocked(identity, j)
	}
	return j
}

// compactLocked applies the retention bounds; reports whether records were
// dropped. Caller holds ix.mu.
func (ix *Index) compactLocked(identity string, j *journalState) bool {
	drop := 0
	cutoff := time.Now().Add(-journalRetention)
	for drop < len(j.records) && j.records[drop].Time.Before(cutoff) {
		drop++
	}
	if excess := len(j.records) - journalMaxRecords; excess > drop {
		drop = excess
	}
	if drop == 0 {
		return false
	}
	j.compactedThrough = j.records[drop-1].ChangeID
	j.records = append([]JournalRecord(nil), j.records[drop:]...)
	return true
}

// persistJournalLocked rewrites the identity's journal file. Caller holds
// ix.mu.
func (ix *Index) persistJournalLocked(identity string, j *journalState) {
	p, err := ix.journalPath(identity)
	if err != nil || p == "" {
		return
	}
	var b strings.Builder
	head, _ := json.Marshal(journalHeader{Journal: 1, CompactedThrough: j.compactedThrough})
	b.Write(head)
	b.WriteByte('\n')
	for _, rec := range j.records {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// appendJournalLocked records rec and appends it to the journal file
// (rewriting only when compaction fires). Caller holds ix.mu.
func (ix *Index) appendJournalLocked(identity string, rec JournalRecord) {
	j := ix.journal(identity)
	j.records = append(j.records, rec)
	if ix.compactLocked(identity, j) {
		ix.persistJournalLocked(identity, j)
		return
	}
	p, err := ix.journalPath(identity)
	if err != nil || p == "" {
		return
	}
	if _, err := os.Stat(p); err != nil {
		// First write: full rewrite creates the header line.
		ix.persistJournalLocked(identity, j)
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Changes returns journal records with change_id > since (ascending), up to
// limit (0 = no limit), keeping only records where visible(path) is true.
// gap reports that since predates the compaction horizon — the caller must
// full-resync from the manifest. latest is the identity's current change_id.
func (ix *Index) Changes(identity string, since int64, limit int, visible func(path string) bool) (recs []JournalRecord, latest int64, gap bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	j := ix.journal(identity)
	if since < j.compactedThrough {
		return nil, t.ChangeID, true
	}
	for _, rec := range j.records {
		if rec.ChangeID <= since {
			continue
		}
		if visible != nil && !visible(rec.Path) {
			continue
		}
		recs = append(recs, rec)
		if limit > 0 && len(recs) >= limit {
			break
		}
	}
	return recs, t.ChangeID, false
}

// Snapshot returns a copy of the identity's file metadata and the current
// change_id — the manifest source (E04-T1 full-resync path).
func (ix *Index) Snapshot(identity string) (map[string]FileMeta, int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	out := make(map[string]FileMeta, len(t.Files))
	for p, m := range t.Files {
		out[p] = m
	}
	return out, t.ChangeID
}
