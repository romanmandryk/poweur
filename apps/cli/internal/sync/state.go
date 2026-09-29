// Package sync is the reference sync client (EPIC-020 E20-T9): it keeps a
// local directory and a folder of an end-to-end encrypted drive in step, with
// per-type merge drivers and Dropbox-style conflicted copies. The relay only
// ever says 409; every merge happens here.
package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StateFileName is the sync-state DB kept at the sync root. It, the ignore
// file and the trash never sync.
const (
	StateFileName  = ".poweur-sync.json"
	IgnoreFileName = ".poweurignore"
	TrashDir       = ".poweur-trash"
)

// FileState is the last-synced snapshot of one path: the local content hash
// and size at that moment, and the remote version they correspond to. For an
// append file, Records is how many records the local file holds.
type FileState struct {
	SHA       string `json:"sha,omitempty"`
	Size      int64  `json:"size,omitempty"`
	MTimeUnix int64  `json:"mtime,omitempty"` // local mtime at last sync (dirty-check cache)
	Version   string `json:"version,omitempty"`
	Dir       bool   `json:"dir,omitempty"`
	Append    bool   `json:"append,omitempty"`
	Records   uint64 `json:"records,omitempty"`
}

// State is the per-root database.
type State struct {
	Identity string `json:"identity"`
	// Drive is the drive synced (the identity's own, or one shared with it);
	// Folder is the remote folder the root mirrors ("" is the drive root, or
	// /<node-id> for a folder shared with you).
	Drive  string `json:"drive"`
	Folder string `json:"folder,omitempty"`
	// Cursor is the drive's change sequence at the last complete pull.
	Cursor string               `json:"cursor"`
	Files  map[string]FileState `json:"files"`
	// Conflicts lists conflicted copies and merges with conflict markers
	// the user has not dealt with yet (shown by status).
	Conflicts []string `json:"conflicts,omitempty"`
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
