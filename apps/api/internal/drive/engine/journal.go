package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

const segmentFormat = 1

// Operation kinds recorded in the journal.
const (
	kindManifest = "manifest"
	kindAppend   = "append"
	kindTrim     = "trim"
	kindGC       = "gc"
)

// positioned is an append record with the position the engine assigned it.
type positioned struct {
	Position uint64             `json:"position"`
	Hash     string             `json:"hash"`
	Record   drive.AppendRecord `json:"record"`
}

// journalOp is one accepted operation. It carries everything replay needs,
// so a restart never re-verifies signatures or refetches pages.
type journalOp struct {
	ID           string            `json:"id"`
	RequestHash  string            `json:"request_hash"`
	Kind         string            `json:"kind"`
	At           time.Time         `json:"at"`
	Manifest     *drive.Manifest   `json:"manifest,omitempty"`
	ManifestHash string            `json:"manifest_hash,omitempty"`
	NewPages     []drive.ChunkPage `json:"new_pages,omitempty"`
	Records      []positioned      `json:"records,omitempty"`
	Trim         *trimOp           `json:"trim,omitempty"`
	GC           *gcOp             `json:"gc,omitempty"`
	System       *systemOp         `json:"system,omitempty"`
	Share        *drive.Share      `json:"share,omitempty"`
	Unshare      *unshareOp        `json:"unshare,omitempty"`
	LinkUse      *linkUseOp        `json:"link_use,omitempty"`
	GroupRevoke  *groupRevokeOp    `json:"group_revoke,omitempty"`
	// Charge is what the commit spent against its share's caps.
	Charge *charge `json:"charge,omitempty"`
}

type trimOp struct {
	Node     string      `json:"node"`
	Before   uint64      `json:"before"`
	Snapshot SnapshotRef `json:"snapshot"`
}

// gcOp drops retained versions; their pages and chunks are released on apply.
type gcOp struct {
	Versions []string `json:"versions"`
}

// segment is one immutable journal object.
type segment struct {
	Format   int         `json:"format"`
	Drive    string      `json:"drive"`
	Seq      uint64      `json:"seq"`
	Previous string      `json:"previous"`
	Ops      []journalOp `json:"ops"`
}

func drivePrefix(driveID string) (string, error) {
	dir, err := idpkg.SanitizeIdentityDirName(driveID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return "drives/" + dir + "/", nil
}

func seqName(seq uint64) string { return fmt.Sprintf("%020d", seq) }

func parseSeqName(name string) (uint64, bool) {
	name = strings.TrimSuffix(name, ".json")
	if len(name) != 20 {
		return 0, false
	}
	seq, err := strconv.ParseUint(name, 10, 64)
	return seq, err == nil
}

func segmentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// publishSegment writes the next segment with a create-only write. A failed
// precondition means another process published this sequence first.
func publishSegment(ctx context.Context, store provider.Store, prefix string, seg segment) (string, error) {
	raw, err := json.Marshal(seg)
	if err != nil {
		return "", err
	}
	if _, err := store.PutIf(ctx, prefix+"journal/"+seqName(seg.Seq)+".json", raw, ""); err != nil {
		if errors.Is(err, provider.ErrPrecondition) {
			return "", ErrStale
		}
		return "", err
	}
	return segmentHash(raw), nil
}

func loadSegment(ctx context.Context, store provider.Store, prefix string, seq uint64) (segment, string, error) {
	obj, err := store.Get(ctx, prefix+"journal/"+seqName(seq)+".json", nil)
	if err != nil {
		return segment{}, "", err
	}
	var seg segment
	if err := json.Unmarshal(obj.Data, &seg); err != nil {
		return segment{}, "", fmt.Errorf("journal segment %d is corrupt: %w", seq, err)
	}
	if seg.Format != segmentFormat || seg.Seq != seq {
		return segment{}, "", fmt.Errorf("journal segment %d has the wrong format or sequence", seq)
	}
	return seg, segmentHash(obj.Data), nil
}

// listSeqs returns every sequence number stored under dir, ascending.
func listSeqs(ctx context.Context, store provider.Store, dir string) ([]uint64, error) {
	var out []uint64
	cursor := ""
	for {
		page, err := store.List(ctx, dir, cursor, 1000)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Objects {
			if seq, ok := parseSeqName(strings.TrimPrefix(obj.Key, dir)); ok {
				out = append(out, seq)
			}
		}
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
}
