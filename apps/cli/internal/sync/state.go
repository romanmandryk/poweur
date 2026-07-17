// Package sync implements the reference sync client (EPIC-004 E04-T4):
// one-shot push/pull/status reconciliation of a local directory against a
// relay-hosted identity tree, with Dropbox-style conflicted-copy handling.
package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StateFileName is the local sync-state DB kept at the sync root. It (and
// the ignore file) never syncs.
const StateFileName = ".poweur-sync.json"

// IgnoreFileName holds ignore patterns (gitignore-flavored subset).
const IgnoreFileName = ".poweurignore"

// FileState is the last-synced snapshot of one path. SHA is the full
// content sha256 (the server's etag), which doubles as the local base
// hash — server etags ARE content hashes, so one value describes both
// sides at the moment of sync.
type FileState struct {
	SHA       string `json:"sha,omitempty"`
	Size      int64  `json:"size,omitempty"`
	MTimeUnix int64  `json:"mtime,omitempty"` // local file mtime at last sync (dirty-check cache)
	Dir       bool   `json:"dir,omitempty"`
}

// State is the per-sync-root database: the changes-feed cursor plus one
// entry per synced path (clean tree paths, slash-separated).
type State struct {
	Identity string               `json:"identity"`
	Cursor   string               `json:"cursor"`
	Files    map[string]FileState `json:"files"`
}

// LoadState reads the state DB at root (empty state when absent).
func LoadState(root string) (*State, error) {
	st := &State{Files: map[string]FileState{}}
	raw, err := os.ReadFile(filepath.Join(root, StateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, st); err != nil {
		return nil, err
	}
	if st.Files == nil {
		st.Files = map[string]FileState{}
	}
	return st, nil
}

// Save atomically persists the state DB at root.
func (st *State) Save(root string) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(root, StateFileName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
