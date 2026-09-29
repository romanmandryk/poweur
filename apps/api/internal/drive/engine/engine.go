package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/identity/drive"
)

// KeyResolver returns an identity's Ed25519 signing key.
type KeyResolver func(ctx context.Context, identity string) (ed25519.PublicKey, error)

// Options configure an Engine.
type Options struct {
	Store provider.Store
	Keys  KeyResolver
	// Quota returns a drive's byte quota; 0 is unlimited.
	Quota func(driveID string) int64
	// Retention keeps superseded versions this long (default 30 days).
	Retention time.Duration
	// UploadTTL is how long an uploaded chunk no version references survives
	// (default 24 hours).
	UploadTTL time.Duration
	// SnapshotEvery writes a snapshot after this many segments (default 64).
	SnapshotEvery uint64
	// OnCommit is told about each change after it is durable, while the
	// drive is still locked (it must not call back into the engine). For an
	// append, records are the appended records with their positions.
	// audience answers who may see the change, against the state it made.
	OnCommit func(driveID string, change Change, records []PositionedRecord, audience Audience)
	Now      func() time.Time
	// Groups resolves local group identities for shares naming a group.
	Groups Groups
}

// Engine serves many drives; each drive is serialized by its own lock.
type Engine struct {
	opts   Options
	mu     sync.Mutex
	drives map[string]*driveHandle
}

type driveHandle struct {
	mu     sync.Mutex
	prefix string
	st     *state
	// segments caches loaded segments for record reads.
	segments map[uint64]segment
	// blobs caches small system documents by hash.
	blobs map[string][]byte
	// manifests caches signed versions, which never change once written.
	// It has its own lock so reads fetch from the store without holding mu.
	cacheMu   sync.Mutex
	manifests map[string]drive.Manifest
}

// New returns an engine over opts.Store.
func New(opts Options) *Engine {
	if opts.Retention == 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if opts.UploadTTL == 0 {
		opts.UploadTTL = 24 * time.Hour
	}
	if opts.SnapshotEvery == 0 {
		opts.SnapshotEvery = 64
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Quota == nil {
		opts.Quota = func(string) int64 { return 0 }
	}
	return &Engine{opts: opts, drives: map[string]*driveHandle{}}
}

func (e *Engine) now() time.Time { return e.opts.Now().UTC() }

func (e *Engine) groups(group string) (members, admins []string, ok bool) {
	if e.opts.Groups == nil {
		return nil, nil, false
	}
	return e.opts.Groups(group)
}

// open returns a drive's handle, loading it on first use. The caller holds
// the handle's lock while touching its state.
func (e *Engine) open(ctx context.Context, driveID string) (*driveHandle, error) {
	driveID = strings.ToLower(strings.TrimSpace(driveID))
	prefix, err := drivePrefix(driveID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	h := e.drives[driveID]
	if h == nil {
		h = &driveHandle{prefix: prefix, segments: map[uint64]segment{}, blobs: map[string][]byte{}}
		e.drives[driveID] = h
	}
	e.mu.Unlock()
	h.mu.Lock()
	if h.st == nil {
		st, err := e.load(ctx, driveID, prefix)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		h.st = st
	}
	h.st.groups = e.opts.Groups
	return h, nil
}

// Forget drops every cached drive; the next access reloads from the store.
// Tests use it to prove statelessness; a relay could use it to shed memory.
func (e *Engine) Forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.drives = map[string]*driveHandle{}
}

// load rebuilds a drive from its latest valid snapshot and the journal
// after it. A snapshot that does not match its journal segment is ignored
// (full replay); a missing or corrupt segment inside the committed range
// fails closed rather than starting from partial state.
func (e *Engine) load(ctx context.Context, driveID, prefix string) (*state, error) {
	seqs, err := listSeqs(ctx, e.opts.Store, prefix+"journal/")
	if err != nil {
		return nil, err
	}
	st := newState(driveID)
	if snaps, err := listSeqs(ctx, e.opts.Store, prefix+"snapshots/"); err == nil {
		for i := len(snaps) - 1; i >= 0; i-- {
			if snap, ok := e.loadSnapshot(ctx, driveID, prefix, snaps[i]); ok {
				st = snap
				break
			}
		}
	}
	for _, seq := range seqs {
		if seq <= st.Seq {
			continue
		}
		if seq != st.Seq+1 {
			return nil, fmt.Errorf("drive %s: journal segment %d is missing", driveID, st.Seq+1)
		}
		seg, hash, err := loadSegment(ctx, e.opts.Store, prefix, seq)
		if err != nil {
			return nil, fmt.Errorf("drive %s: %w", driveID, err)
		}
		if err := replay(st, seq, seg, hash); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// replay applies the segment that follows st.
func replay(st *state, seq uint64, seg segment, hash string) error {
	if seg.Previous != st.SegmentHash || seg.Drive != st.Drive {
		return fmt.Errorf("drive %s: journal segment %d does not follow %d", st.Drive, seq, st.Seq)
	}
	for i := range seg.Ops {
		if err := applyOp(st, seq, i, seg.Ops[i]); err != nil {
			return fmt.Errorf("drive %s: replaying segment %d: %w", st.Drive, seq, err)
		}
	}
	st.Seq, st.SegmentHash = seq, hash
	return nil
}

// catchUp replays segments another relay process published since this one
// last read the journal, so writes validate against the latest state. It
// costs one missing-object read when nothing changed. Caller holds h.mu; on
// error the handle is dropped.
func (e *Engine) catchUp(ctx context.Context, h *driveHandle) error {
	for {
		seq := h.st.Seq + 1
		seg, hash, err := loadSegment(ctx, e.opts.Store, h.prefix, seq)
		if errors.Is(err, provider.ErrNotFound) {
			return nil
		}
		if err == nil {
			err = replay(h.st, seq, seg, hash)
		}
		if err != nil {
			e.drop(h, h.st.Drive)
			return err
		}
	}
}

func (e *Engine) loadSnapshot(ctx context.Context, driveID, prefix string, seq uint64) (*state, bool) {
	obj, err := e.opts.Store.Get(ctx, prefix+"snapshots/"+seqName(seq)+".json", nil)
	if err != nil {
		return nil, false
	}
	st := newState(driveID)
	if json.Unmarshal(obj.Data, st) != nil || st.Format != stateFormat || st.Drive != driveID || st.Seq != seq {
		return nil, false
	}
	// The snapshot must agree with the journal it claims to cover.
	if _, hash, err := loadSegment(ctx, e.opts.Store, prefix, seq); err != nil || hash != st.SegmentHash {
		return nil, false
	}
	return st, true
}

// ---------------------------------------------------------------- uploads

// PutChunk stores one encrypted chunk proxied through the relay and returns
// its reference. The ID is the SHA-256 of the bytes. A chunk no version
// references is collected after UploadTTL.
func (e *Engine) PutChunk(ctx context.Context, driveID string, data []byte) (drive.ChunkRef, error) {
	size := uint64(len(data))
	if size < 40+drive.PaddingBucket || size > drive.MaxChunkBytes || (size-40)%drive.PaddingBucket != 0 {
		return drive.ChunkRef{}, fmt.Errorf("%w: chunk size %d is not a valid encrypted chunk", ErrInvalid, size)
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return drive.ChunkRef{}, err
	}
	used, prefix := h.st.Used, h.prefix
	quota := e.opts.Quota(h.st.Drive)
	h.mu.Unlock()
	if quota > 0 && used+int64(size) > quota {
		return drive.ChunkRef{}, ErrQuota
	}
	ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: size}
	if _, err := e.opts.Store.PutIf(ctx, prefix+"chunks/"+ref.ID, data, ""); err != nil {
		return drive.ChunkRef{}, err
	}
	return ref, nil
}

// UploadURL presigns a direct upload of one chunk to the store, bound to its
// hash. It returns provider.ErrUnsupported when the store cannot presign (the
// filesystem, or S3 with presigning off); the client then uploads through
// the relay with PutChunk. The commit still checks the stored size.
func (e *Engine) UploadURL(ctx context.Context, driveID string, ref drive.ChunkRef, ttl time.Duration) (provider.SignedURL, error) {
	if ref.Size < 40+drive.PaddingBucket || ref.Size > drive.MaxChunkBytes || (ref.Size-40)%drive.PaddingBucket != 0 || len(ref.ID) != 64 || !isHex(ref.ID) {
		return provider.SignedURL{}, fmt.Errorf("%w: not a valid encrypted chunk reference", ErrInvalid)
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return provider.SignedURL{}, err
	}
	used, prefix := h.st.Used, h.prefix
	quota := e.opts.Quota(h.st.Drive)
	h.mu.Unlock()
	if quota > 0 && used+int64(ref.Size) > quota {
		return provider.SignedURL{}, ErrQuota
	}
	return e.opts.Store.PresignPut(ctx, prefix+"chunks/"+ref.ID, ref.ID, int64(ref.Size), ttl)
}

// Missing returns the refs whose chunks are not stored yet.
func (e *Engine) Missing(ctx context.Context, driveID string, refs []drive.ChunkRef) ([]drive.ChunkRef, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	prefix := h.prefix
	known := map[string]bool{}
	for _, ref := range refs {
		if c := h.st.Chunks[ref.ID]; c != nil && c.Size == ref.Size {
			known[ref.ID] = true
		}
	}
	h.mu.Unlock()
	var missing []drive.ChunkRef
	for _, ref := range refs {
		if known[ref.ID] {
			continue
		}
		if err := e.checkChunk(ctx, prefix, ref); err != nil {
			missing = append(missing, ref)
		}
	}
	return missing, nil
}

// checkChunk confirms a stored chunk exists with the referenced size. Its
// bytes were hashed on the relay path, or checksum-verified by the store on a
// presigned upload (the S3 probe refuses stores that do not).
func (e *Engine) checkChunk(ctx context.Context, prefix string, ref drive.ChunkRef) error {
	obj, err := e.opts.Store.Get(ctx, prefix+"chunks/"+ref.ID, &provider.Range{Offset: 0, Length: 1})
	if err != nil {
		return fmt.Errorf("%w: chunk %s is not uploaded", ErrInvalid, ref.ID)
	}
	if uint64(obj.Size) != ref.Size {
		return fmt.Errorf("%w: chunk %s is %d bytes, not %d", ErrInvalid, ref.ID, obj.Size, ref.Size)
	}
	return nil
}

// ---------------------------------------------------------------- commits

// Request is one commit: exactly one of a manifest (with any pages the
// relay does not have yet), a batch of append records for one node, or a trim.
type Request struct {
	// ID is the client's idempotency key: 16 random bytes, lowercase hex.
	ID       string
	Manifest *drive.Manifest
	Pages    []drive.ChunkPage
	Records  []drive.AppendRecord
	Trim     *Trim
	Share    *drive.Share
	Unshare  *Unshare
	Transfer *Transfer
	// Author is the authenticated committer. It authorizes trims and
	// revocations; manifests, records and shares name their own signer.
	Author string
}

// Trim drops an append file's records before a position, covered by a
// committed snapshot version.
type Trim struct {
	Node     string      `json:"node"`
	Before   uint64      `json:"before"`
	Snapshot SnapshotRef `json:"snapshot"`
}

// Result is what a commit produced.
type Result struct {
	Seq       uint64
	Head      string
	Positions []uint64
}

// Commit validates a request, writes its objects, publishes one journal
// segment and applies it. Retrying a request ID with the same content
// returns the first result; reusing an ID with different content is refused.
func (e *Engine) Commit(ctx context.Context, driveID string, req Request) (Result, error) {
	if len(req.ID) != 32 || !isHex(req.ID) {
		return Result{}, fmt.Errorf("%w: request ID must be 16 bytes of lowercase hex", ErrInvalid)
	}
	requestHash, err := hashRequest(req)
	if err != nil {
		return Result{}, err
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return Result{}, err
	}
	defer h.mu.Unlock()
	if err := e.catchUp(ctx, h); err != nil {
		return Result{}, err
	}
	st := h.st
	if prior, ok := st.Ops[req.ID]; ok {
		if prior.RequestHash != requestHash {
			return Result{}, fmt.Errorf("%w: request ID reused with different content", ErrInvalid)
		}
		return Result{Seq: prior.Seq, Head: prior.Head, Positions: prior.Positions}, nil
	}
	op, err := e.validate(ctx, h, req)
	if err != nil {
		return Result{}, err
	}
	op.ID, op.RequestHash, op.At = req.ID, requestHash, e.now()
	if err := e.writeObjects(ctx, h.prefix, op); err != nil {
		return Result{}, err
	}
	if err := e.publish(ctx, h, op); err != nil {
		return Result{}, err
	}
	result := st.Ops[req.ID]
	return Result{Seq: result.Seq, Head: result.Head, Positions: result.Positions}, nil
}

// publish writes op as the next segment, applies it and notifies. The
// caller holds h.mu and has written op's objects.
func (e *Engine) publish(ctx context.Context, h *driveHandle, op journalOp) error {
	st := h.st
	seq := st.Seq + 1
	hash, err := publishSegment(ctx, e.opts.Store, h.prefix, segment{Format: segmentFormat, Drive: st.Drive, Seq: seq, Previous: st.SegmentHash, Ops: []journalOp{op}})
	if err != nil {
		if errors.Is(err, ErrStale) {
			e.drop(h, st.Drive) // another writer: reload before the next request
		}
		return err
	}
	changesBefore := len(st.Changes)
	if err := applyOp(st, seq, 0, op); err != nil {
		// The segment is durable; a relay that cannot apply it must reload.
		e.drop(h, st.Drive)
		return fmt.Errorf("apply committed segment %d: %w", seq, err)
	}
	st.Seq, st.SegmentHash = seq, hash
	if seq%e.opts.SnapshotEvery == 0 {
		_ = e.writeSnapshot(ctx, h)
	}
	if e.opts.OnCommit != nil {
		for _, change := range st.Changes[changesBefore:] {
			var records []PositionedRecord
			if op.Kind == kindAppend && change.Operation == "append" {
				first := change.Position + 1 - uint64(len(op.Records))
				for i, r := range op.Records {
					records = append(records, PositionedRecord{Position: first + uint64(i), Record: r.Record})
				}
			}
			e.opts.OnCommit(st.Drive, change, records, e.audience(st, change))
		}
	}
	return nil
}

// drop forgets a drive's cached state so the next request reloads it.
func (e *Engine) drop(h *driveHandle, driveID string) {
	h.st = nil
	e.mu.Lock()
	if e.drives[driveID] == h {
		delete(e.drives, driveID)
	}
	e.mu.Unlock()
}

// writeObjects stores the manifest and new pages before the segment that
// makes them visible. An interruption leaves collectible orphans only.
func (e *Engine) writeObjects(ctx context.Context, prefix string, op journalOp) error {
	for _, page := range op.NewPages {
		hash, _ := page.Hash()
		raw, err := json.Marshal(page)
		if err != nil {
			return err
		}
		if _, err := e.opts.Store.PutIf(ctx, prefix+"pages/"+hash+".json", raw, ""); err != nil {
			return err
		}
	}
	if op.Manifest != nil {
		raw, err := json.Marshal(op.Manifest)
		if err != nil {
			return err
		}
		if _, err := e.opts.Store.PutIf(ctx, prefix+"versions/"+op.Manifest.Node+"/"+op.Manifest.Version+".json", raw, ""); err != nil {
			if errors.Is(err, provider.ErrPrecondition) {
				return fmt.Errorf("%w: version %s already exists", ErrExists, op.Manifest.Version)
			}
			return err
		}
	}
	return nil
}

func (e *Engine) writeSnapshot(ctx context.Context, h *driveHandle) error {
	raw, err := json.Marshal(h.st)
	if err != nil {
		return err
	}
	_, err = e.opts.Store.PutIf(ctx, h.prefix+"snapshots/"+seqName(h.st.Seq)+".json", raw, "")
	return err
}

func hashRequest(req Request) (string, error) {
	raw, err := json.Marshal(struct {
		M *drive.Manifest      `json:"m"`
		P []drive.ChunkPage    `json:"p"`
		R []drive.AppendRecord `json:"r"`
		T *Trim                `json:"t"`
		S *drive.Share         `json:"s,omitempty"`
		U *Unshare             `json:"u,omitempty"`
		X *Transfer            `json:"x,omitempty"`
		A string               `json:"a"`
	}{req.Manifest, req.Pages, req.Records, req.Trim, req.Share, req.Unshare, req.Transfer, req.Author})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
