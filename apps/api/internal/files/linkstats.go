package files

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Link-share accounting (EPIC-005 E05-T4).
//
// A capability URL is served to anyone who holds it, so the owner's only
// levers are expiry, revocation and a download cap — and all three are
// worth nothing if the relay forgets how many downloads have happened. The
// counters therefore live next to the metadata index, in the identity's
// own meta directory (outside the visible tree, like files-index.json),
// and survive a relay restart.
//
// They are also the bandwidth accounting the epic asks for: bytes actually
// written to link visitors, per share, so an owner can see what a link is
// costing them before the bill does.

// LinkStat is one link share's lifetime counters.
type LinkStat struct {
	ShareID   string    `json:"share_id"`
	Downloads int64     `json:"downloads"`
	Bytes     int64     `json:"bytes"`
	FirstAt   time.Time `json:"first_at,omitempty"`
	LastAt    time.Time `json:"last_at,omitempty"`
}

type linkStatsFile struct {
	Shares map[string]*LinkStat `json:"shares"`
}

// LinkStats is the per-identity link accounting store.
type LinkStats struct {
	mu      sync.Mutex
	trees   map[string]*linkStatsFile
	homeDir func(identity string) (string, error)
}

// NewLinkStats builds a store rooted at the same per-identity home
// directories the metadata index uses.
func NewLinkStats(homeDir func(identity string) (string, error)) *LinkStats {
	return &LinkStats{trees: make(map[string]*linkStatsFile), homeDir: homeDir}
}

func (ls *LinkStats) path(identity string) (string, error) {
	if ls.homeDir == nil {
		return "", nil
	}
	home, err := ls.homeDir(identity)
	if err != nil || home == "" {
		return "", err
	}
	return filepath.Join(home, "meta", "link-stats.json"), nil
}

func (ls *LinkStats) treeLocked(identity string) *linkStatsFile {
	key := strings.ToLower(identity)
	if t, ok := ls.trees[key]; ok {
		return t
	}
	t := &linkStatsFile{Shares: make(map[string]*LinkStat)}
	if p, err := ls.path(identity); err == nil && p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(raw, t)
			if t.Shares == nil {
				t.Shares = make(map[string]*LinkStat)
			}
		}
	}
	ls.trees[key] = t
	return t
}

func (ls *LinkStats) persistLocked(identity string) {
	p, err := ls.path(identity)
	if err != nil || p == "" {
		return
	}
	raw, err := json.Marshal(ls.trees[strings.ToLower(identity)])
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// Get returns the counters for one share (zero value when unused).
func (ls *LinkStats) Get(identity, shareID string) LinkStat {
	if ls == nil {
		return LinkStat{ShareID: shareID}
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if st, ok := ls.treeLocked(identity).Shares[shareID]; ok {
		return *st
	}
	return LinkStat{ShareID: shareID}
}

// Record charges one download of n bytes to a share and returns the
// updated counters.
func (ls *LinkStats) Record(identity, shareID string, n int64) LinkStat {
	if ls == nil {
		return LinkStat{ShareID: shareID}
	}
	now := time.Now().UTC()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	tree := ls.treeLocked(identity)
	st, ok := tree.Shares[shareID]
	if !ok {
		st = &LinkStat{ShareID: shareID, FirstAt: now}
		tree.Shares[shareID] = st
	}
	st.Downloads++
	if n > 0 {
		st.Bytes += n
	}
	st.LastAt = now
	ls.persistLocked(identity)
	return *st
}

// All returns every share's counters, share-id ordered.
func (ls *LinkStats) All(identity string) []LinkStat {
	if ls == nil {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	tree := ls.treeLocked(identity)
	out := make([]LinkStat, 0, len(tree.Shares))
	for _, st := range tree.Shares {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ShareID < out[j].ShareID })
	return out
}

// Exhausted reports whether a share has hit its download cap
// (max <= 0 means unlimited).
func (ls *LinkStats) Exhausted(identity, shareID string, max int) bool {
	if max <= 0 {
		return false
	}
	return ls.Get(identity, shareID).Downloads >= int64(max)
}
