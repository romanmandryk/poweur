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
	ShareID      string    `json:"share_id"`
	Opens        int64     `json:"opens,omitempty"`
	Downloads    int64     `json:"downloads"`
	Bytes        int64     `json:"bytes"`
	Uploads      int64     `json:"uploads,omitempty"`
	UploadBytes  int64     `json:"upload_bytes,omitempty"`
	ClaimStarted int64     `json:"claim_started,omitempty"`
	IDClaimed    int64     `json:"id_claimed,omitempty"`
	FirstAt      time.Time `json:"first_at,omitempty"`
	LastAt       time.Time `json:"last_at,omitempty"`
}

// RecordClaimStarted records the funnel transition without visitor data.
func (ls *LinkStats) RecordClaimStarted(identity, shareID string) {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	st := ls.statLocked(identity, shareID)
	st.ClaimStarted++
	st.LastAt = time.Now().UTC()
	ls.persistLocked(identity)
}

// RecordIDClaimed records that a signed Poweur identity continued a public
// capability flow. No claimant identity is retained with this counter.
func (ls *LinkStats) RecordIDClaimed(identity, shareID string) {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	st := ls.statLocked(identity, shareID)
	st.IDClaimed++
	st.LastAt = time.Now().UTC()
	ls.persistLocked(identity)
}

// RecordOpen records an anonymous landing-page view without retaining a
// visitor address, user agent, token, path or filename.
func (ls *LinkStats) RecordOpen(identity, shareID string) {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	st := ls.statLocked(identity, shareID)
	st.Opens++
	st.LastAt = time.Now().UTC()
	ls.persistLocked(identity)
}

func (ls *LinkStats) statLocked(identity, shareID string) *LinkStat {
	tree := ls.treeLocked(identity)
	st, ok := tree.Shares[shareID]
	if !ok {
		st = &LinkStat{ShareID: shareID, FirstAt: time.Now().UTC()}
		tree.Shares[shareID] = st
	}
	return st
}

// ReserveUpload atomically charges a file-request upload against its count
// and byte caps. Failed/aborted transfers remain charged, which prevents an
// attacker from racing or repeatedly aborting the last quota slot.
func (ls *LinkStats) ReserveUpload(identity, shareID string, size int64, maxUploads int, maxBytes int64) (LinkStat, bool) {
	if ls == nil {
		return LinkStat{ShareID: shareID}, true
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	st := ls.statLocked(identity, shareID)
	if maxUploads > 0 && st.Uploads >= int64(maxUploads) {
		return *st, false
	}
	if maxBytes > 0 && (size > maxBytes || st.UploadBytes > maxBytes-size) {
		return *st, false
	}
	st.Uploads++
	st.UploadBytes += size
	st.LastAt = time.Now().UTC()
	ls.persistLocked(identity)
	return *st, true
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

// Reserve claims one download slot against a share's cap (max <= 0 means
// unlimited), returning the updated counters and whether the download may
// proceed.
//
// Check-and-increment happens under one lock because that is the only way
// the cap means anything: two visitors clicking the last download of a
// "max_downloads: 10" link at the same moment must not both get through.
// The slot is charged *before* the bytes flow, so a cap can never be
// overrun — an aborted transfer costs the visitor a slot, which is the
// safe direction to be wrong in for a capability URL.
func (ls *LinkStats) Reserve(identity, shareID string, max int) (LinkStat, bool) {
	if ls == nil {
		return LinkStat{ShareID: shareID}, true
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
	if max > 0 && st.Downloads >= int64(max) {
		return *st, false
	}
	st.Downloads++
	st.LastAt = now
	ls.persistLocked(identity)
	return *st, true
}

// AddBytes charges transferred bytes to a share — the bandwidth half of the
// accounting, recorded after the copy so it reflects what actually left the
// relay rather than what was requested.
func (ls *LinkStats) AddBytes(identity, shareID string, n int64) {
	if ls == nil || n <= 0 {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	tree := ls.treeLocked(identity)
	st, ok := tree.Shares[shareID]
	if !ok {
		st = &LinkStat{ShareID: shareID, FirstAt: time.Now().UTC()}
		tree.Shares[shareID] = st
	}
	st.Bytes += n
	ls.persistLocked(identity)
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
