package engine

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"

	"github.com/poweur/identity/drive"
)

// validate checks a request against the drive's current state and returns
// the journal operation it becomes. It does not change state.
func (e *Engine) validate(ctx context.Context, h *driveHandle, req Request) (journalOp, error) {
	count := 0
	for _, set := range []bool{req.Manifest != nil, len(req.Records) > 0, req.Trim != nil, req.Share != nil, req.Unshare != nil, req.Transfer != nil} {
		if set {
			count++
		}
	}
	if count != 1 {
		return journalOp{}, fmt.Errorf("%w: a commit is one manifest, one batch of records, one trim, one share, one revocation or one transfer", ErrInvalid)
	}
	switch {
	case req.Share != nil:
		return e.validateShare(ctx, h, *req.Share, req.Author)
	case req.Unshare != nil:
		return e.validateUnshare(h, req.Unshare.ID, req.Author)
	case req.Transfer != nil:
		return e.validateTransfer(h, *req.Transfer, req.Author)
	case req.Manifest != nil:
		return e.validateManifest(ctx, h, req)
	case len(req.Records) > 0:
		return e.validateAppend(ctx, h, req.Records, req.Author)
	default:
		return e.validateTrim(ctx, h, req)
	}
}

// writeSubject is who a write is authorized and charged as: its author, or
// for a guest-authored write the link that committed it. Guests write only
// through links; an identity writes only as itself.
func writeSubject(author, committer string) (string, error) {
	if drive.IsGuest(author) {
		if !strings.HasPrefix(committer, linkActorPrefix) {
			return "", fmt.Errorf("%w: guest authors write only through a link", ErrForbidden)
		}
		return committer, nil
	}
	if committer != "" && !strings.EqualFold(committer, author) {
		return "", fmt.Errorf("%w: %s cannot commit %s's writes", ErrForbidden, committer, author)
	}
	return author, nil
}

// authorKey resolves a signing key; guest keys are read from the name.
func (e *Engine) authorKey(ctx context.Context, author string) (ed25519.PublicKey, error) {
	if key, ok := drive.GuestKey(author); ok {
		return key, nil
	}
	return e.opts.Keys(ctx, author)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// authorizeManifest checks the author's role for a manifest: creating needs
// create on the folder, content changes and removal need write, a move also
// needs create on the destination, and a key rotation needs admin. New
// content under a key a revoked member still holds is refused until the
// node rotates.
func (e *Engine) authorizeManifest(st *state, m drive.Manifest, subject string) error {
	switch m.Operation {
	case drive.OpCreate:
		if m.Folder == "" {
			return e.permit(st, subject, "", needOwner)
		}
		if err := e.permit(st, subject, m.Folder, drive.RoleCreate); err != nil {
			return err
		}
		return rotationRequired(st, m.Folder)
	case drive.OpReplace:
		if err := e.permit(st, subject, m.Node, drive.RoleWrite); err != nil {
			return err
		}
		return rotationRequired(st, m.Node)
	case drive.OpMove:
		if err := e.permit(st, subject, m.Node, drive.RoleWrite); err != nil {
			return err
		}
		return e.permit(st, subject, m.Folder, drive.RoleCreate)
	case drive.OpRotate:
		return e.permit(st, subject, m.Node, drive.RoleAdmin)
	default:
		return e.permit(st, subject, m.Node, drive.RoleWrite)
	}
}

func (e *Engine) validateManifest(ctx context.Context, h *driveHandle, req Request) (journalOp, error) {
	st, m := h.st, *req.Manifest
	if err := m.Validate(); err != nil {
		return journalOp{}, invalid("%v", err)
	}
	if m.Drive != st.Drive {
		return journalOp{}, invalid("manifest is for drive %s", m.Drive)
	}
	subject, err := writeSubject(m.Author, req.Author)
	if err != nil {
		return journalOp{}, err
	}
	if err := e.authorizeManifest(st, m, subject); err != nil {
		return journalOp{}, err
	}
	key, err := e.authorKey(ctx, m.Author)
	if err != nil {
		return journalOp{}, fmt.Errorf("%w: cannot resolve %s: %v", ErrForbidden, m.Author, err)
	}
	if err := m.Verify(key); err != nil {
		return journalOp{}, invalid("%v", err)
	}
	if _, used := st.Versions[m.Version]; used {
		return journalOp{}, fmt.Errorf("%w: version %s", ErrExists, m.Version)
	}
	current := st.Nodes[m.Node]
	switch m.Operation {
	case drive.OpCreate:
		if current != nil {
			return journalOp{}, fmt.Errorf("%w: node %s", ErrExists, m.Node)
		}
		if m.Folder == "" {
			if st.Root != "" || m.Kind != drive.KindFolder || m.Author != st.Drive {
				return journalOp{}, invalid("only the owner creates the one root folder")
			}
		}
	default:
		if current == nil || current.Removed {
			return journalOp{}, fmt.Errorf("%w: node %s", ErrNotFound, m.Node)
		}
		if m.Parent != current.Head {
			return journalOp{}, ErrConflict
		}
		if m.Public != current.Public {
			return journalOp{}, invalid("a node cannot switch between public and private; publish a copy instead")
		}
		if m.Kind != current.Kind || m.Kind == drive.KindFile && m.Mode != current.Mode {
			return journalOp{}, invalid("a node keeps its kind and mode")
		}
		if m.Operation == drive.OpRotate && m.Generation <= current.Generation {
			return journalOp{}, invalid("a rotation raises the key generation")
		}
		if m.Operation != drive.OpRotate && m.Generation != current.Generation {
			return journalOp{}, invalid("only a rotation changes the key generation")
		}
		if m.Operation == drive.OpReplace && current.Mode == drive.ModeAppend {
			return journalOp{}, invalid("an append file changes through records and trims")
		}
		if m.Operation == drive.OpRemove && m.Kind == drive.KindFolder && len(st.children(m.Node)) > 0 {
			return journalOp{}, fmt.Errorf("%w: folder is not empty", ErrConflict)
		}
		if m.Operation == drive.OpRemove && m.Node == st.Root {
			return journalOp{}, invalid("the root cannot be removed")
		}
	}
	if m.Folder != "" {
		folder := st.Nodes[m.Folder]
		if folder == nil || folder.Removed || folder.Kind != drive.KindFolder {
			return journalOp{}, fmt.Errorf("%w: folder %s", ErrNotFound, m.Folder)
		}
		if m.Operation == drive.OpMove && st.isAncestor(m.Node, m.Folder) {
			return journalOp{}, invalid("a folder cannot move into itself")
		}
		// Public trees (E20-T5): everything under a public folder is public,
		// and a public tree's top sits directly under the root, so /pub/<name>
		// is unambiguous.
		if folder.Public && !m.Public {
			return journalOp{}, invalid("a public folder holds only public nodes")
		}
		if m.Public && !folder.Public && m.Folder != st.Root {
			return journalOp{}, invalid("a public folder's top sits directly under the drive root")
		}
		if owner, taken := st.Names[nameKey(m.Folder, m.NameHash)]; taken && owner != m.Node {
			return journalOp{}, fmt.Errorf("%w: a sibling already has this name", ErrExists)
		}
	}
	// Content: every page is known or supplied, and every chunk is stored.
	supplied := map[string]drive.ChunkPage{}
	for _, page := range req.Pages {
		hash, err := page.Hash()
		if err != nil {
			return journalOp{}, invalid("%v", err)
		}
		supplied[hash] = page
	}
	pages := make([]drive.ChunkPage, 0, len(m.Pages))
	var newPages []drive.ChunkPage
	for _, hash := range m.Pages {
		if refs, ok := st.Pages[hash]; ok {
			pages = append(pages, drive.ChunkPage{Format: 1, Drive: m.Drive, Node: m.Node, Chunks: refs})
			continue
		}
		page, ok := supplied[hash]
		if !ok {
			return journalOp{}, invalid("page %s is neither stored nor supplied", hash)
		}
		pages = append(pages, page)
		newPages = append(newPages, page)
	}
	refs, err := m.VerifyPages(pages)
	if err != nil {
		return journalOp{}, invalid("%v", err)
	}
	var added int64
	checked := map[string]bool{}
	for _, ref := range refs {
		if checked[ref.ID] {
			continue
		}
		checked[ref.ID] = true
		if c := st.Chunks[ref.ID]; c != nil {
			if c.Size != ref.Size {
				return journalOp{}, invalid("chunk %s size mismatch", ref.ID)
			}
			continue
		}
		if err := e.checkChunk(ctx, h.prefix, ref); err != nil {
			return journalOp{}, err
		}
		added += int64(ref.Size)
	}
	if quota := e.opts.Quota(st.Drive); quota > 0 && added > 0 && st.Used+added > quota {
		return journalOp{}, ErrQuota
	}
	target, need, files := m.Node, drive.RoleWrite, uint64(0)
	switch m.Operation {
	case drive.OpCreate:
		target, need, files = m.Folder, drive.RoleCreate, 1
	case drive.OpRotate:
		need = drive.RoleAdmin
	}
	spent, err := st.chargeFor(subject, target, need, charge{Bytes: uint64(added), Files: files}, e.now)
	if err != nil {
		return journalOp{}, err
	}
	hash, err := m.Hash()
	if err != nil {
		return journalOp{}, invalid("%v", err)
	}
	return journalOp{Kind: kindManifest, Manifest: &m, ManifestHash: hash, NewPages: newPages, Charge: spent}, nil
}

func (e *Engine) validateAppend(ctx context.Context, h *driveHandle, records []drive.AppendRecord, committer string) (journalOp, error) {
	st := h.st
	if len(records) > 1024 {
		return journalOp{}, invalid("at most 1024 records per commit")
	}
	target := records[0].Node
	n := st.Nodes[target]
	if n == nil || n.Removed {
		return journalOp{}, fmt.Errorf("%w: node %s", ErrNotFound, target)
	}
	if n.Kind != drive.KindFile || n.Mode != drive.ModeAppend {
		return journalOp{}, invalid("node %s is not an append file", target)
	}
	if err := rotationRequired(st, target); err != nil {
		return journalOp{}, err
	}
	cursors := map[string]authorCursor{}
	for author, c := range n.Authors {
		cursors[author] = c
	}
	var out []positioned
	var added int64
	seen := map[string]bool{}
	position := n.Position
	for _, record := range records {
		if record.Drive != st.Drive || record.Node != target {
			return journalOp{}, invalid("records in one commit are for one node")
		}
		if record.Generation != n.Generation {
			return journalOp{}, invalid("record generation %d is not the node's %d", record.Generation, n.Generation)
		}
		subject, err := writeSubject(record.Author, committer)
		if err != nil {
			return journalOp{}, err
		}
		if err := e.permit(st, subject, target, drive.RoleAppend); err != nil {
			return journalOp{}, err
		}
		key, err := e.authorKey(ctx, record.Author)
		if err != nil {
			return journalOp{}, fmt.Errorf("%w: cannot resolve %s: %v", ErrForbidden, record.Author, err)
		}
		cursor := cursors[record.Author]
		if err := record.VerifyNext(key, cursor.Sequence, cursor.Hash); err != nil {
			return journalOp{}, invalid("%v", err)
		}
		hash, _ := record.Hash()
		cursors[record.Author] = authorCursor{Sequence: record.Sequence, Hash: hash}
		for _, ref := range record.Chunks {
			if seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			if c := st.Chunks[ref.ID]; c != nil {
				if c.Size != ref.Size {
					return journalOp{}, invalid("chunk %s size mismatch", ref.ID)
				}
				continue
			}
			if err := e.checkChunk(ctx, h.prefix, ref); err != nil {
				return journalOp{}, err
			}
			added += int64(ref.Size)
		}
		// Content sealed into the record counts like chunks do.
		if record.Sealed != nil {
			added += int64(len(record.Sealed.Ciphertext))
		}
		position++
		out = append(out, positioned{Position: position, Hash: hash, Record: record})
	}
	if quota := e.opts.Quota(st.Drive); quota > 0 && added > 0 && st.Used+added > quota {
		return journalOp{}, ErrQuota
	}
	// One commit's records share an author (the relay requires it).
	subject, _ := writeSubject(records[0].Author, committer)
	spent, err := st.chargeFor(subject, target, drive.RoleAppend, charge{Bytes: uint64(added), Records: uint64(len(records))}, e.now)
	if err != nil {
		return journalOp{}, err
	}
	return journalOp{Kind: kindAppend, Records: out, Charge: spent}, nil
}

func (e *Engine) validateTrim(ctx context.Context, h *driveHandle, req Request) (journalOp, error) {
	st, t := h.st, req.Trim
	n := st.Nodes[t.Node]
	if n == nil || n.Removed || n.Mode != drive.ModeAppend {
		return journalOp{}, fmt.Errorf("%w: append file %s", ErrNotFound, t.Node)
	}
	if err := e.permit(st, req.Author, t.Node, drive.RoleAdmin); err != nil {
		return journalOp{}, err
	}
	if t.Before <= max(n.TrimmedBefore, 1) || t.Before > n.Position+1 {
		return journalOp{}, invalid("trim position %d is outside %d..%d", t.Before, max(n.TrimmedBefore, 1)+1, n.Position+1)
	}
	snap := st.Nodes[t.Snapshot.Node]
	if snap == nil || snap.Removed || snap.Kind != drive.KindFile {
		return journalOp{}, invalid("trim needs a committed snapshot file")
	}
	if _, ok := st.Versions[t.Snapshot.Version]; !ok || st.Versions[t.Snapshot.Version].Node != t.Snapshot.Node {
		return journalOp{}, invalid("snapshot version %s is not committed", t.Snapshot.Version)
	}
	return journalOp{Kind: kindTrim, Trim: &trimOp{Node: t.Node, Before: t.Before, Snapshot: t.Snapshot}}, nil
}

// applyOp changes state for one committed operation. It is the only code
// that mutates a drive, used both after publishing and during replay, so it
// must be deterministic and must not consult anything but the operation.
func applyOp(st *state, seq uint64, index int, op journalOp) error {
	result := opResult{RequestHash: op.RequestHash, Seq: seq}
	switch op.Kind {
	case kindManifest:
		m := op.Manifest
		if m == nil {
			return fmt.Errorf("manifest operation without a manifest")
		}
		for _, page := range op.NewPages {
			hash, err := page.Hash()
			if err != nil {
				return err
			}
			if _, ok := st.Pages[hash]; !ok {
				st.Pages[hash] = page.Chunks
			}
		}
		n := st.Nodes[m.Node]
		if n == nil {
			n = &node{ID: m.Node, Kind: m.Kind, Mode: m.Mode, Public: m.Public}
			st.Nodes[m.Node] = n
			if m.Folder == "" {
				st.Root = m.Node
			}
		} else if prev := st.Versions[n.Head]; prev != nil {
			prev.Superseded = op.At
		}
		for _, hash := range m.Pages {
			st.Used += st.refPage(hash)
		}
		st.Versions[m.Version] = &version{Node: m.Node, Hash: op.ManifestHash, Pages: append([]string(nil), m.Pages...)}
		if m.Folder != "" && (m.Operation == drive.OpCreate || m.Operation == drive.OpMove) {
			if n.NameHash != "" {
				delete(st.Names, nameKey(n.Folder, n.NameHash))
			}
			n.Folder, n.NameHash = m.Folder, m.NameHash
			st.Names[nameKey(n.Folder, n.NameHash)] = n.ID
		}
		if m.Operation == drive.OpRemove {
			n.Removed = true
			if n.NameHash != "" {
				delete(st.Names, nameKey(n.Folder, n.NameHash))
			}
			// A removed append file's records go with it.
			releaseRecords(st, n)
		}
		n.Head, n.HeadHash, n.Generation, n.Count, n.Pages, n.Updated = m.Version, op.ManifestHash, m.Generation, m.Count, append([]string(nil), m.Pages...), op.At
		if m.Operation == drive.OpRotate {
			n.RotateRequired = false
		}
		if m.NodeKey != nil {
			n.KeyVersion = m.Version
		}
		if m.Name != nil || m.PlainName != "" {
			n.NameVersion = m.Version
		}
		if m.ContentKey != nil || m.PlainKey != "" {
			n.ContentVersion = m.Version
		}
		result.Head = m.Version
		st.Changes = append(st.Changes, Change{Seq: seq, Node: m.Node, Operation: m.Operation, Version: m.Version, At: op.At})
	case kindAppend:
		if len(op.Records) == 0 {
			return fmt.Errorf("append operation without records")
		}
		n := st.Nodes[op.Records[0].Record.Node]
		if n == nil {
			return fmt.Errorf("append to unknown node")
		}
		if n.Authors == nil {
			n.Authors = map[string]authorCursor{}
		}
		if n.Records == nil {
			n.Records = map[uint64]recordLoc{}
		}
		for i, p := range op.Records {
			if p.Position != n.Position+1 {
				return fmt.Errorf("append position %d does not follow %d", p.Position, n.Position)
			}
			n.Position = p.Position
			n.Records[p.Position] = recordLoc{Segment: seq, Op: index, Index: i, Chunks: p.Record.Chunks}
			n.Authors[p.Record.Author] = authorCursor{Sequence: p.Record.Sequence, Hash: p.Hash}
			st.Used += st.refChunks(p.Record.Chunks)
			result.Positions = append(result.Positions, p.Position)
		}
		n.Updated = op.At
		last := op.Records[len(op.Records)-1]
		st.Changes = append(st.Changes, Change{Seq: seq, Node: n.ID, Operation: "append", Position: last.Position, At: op.At})
	case kindTrim:
		t := op.Trim
		n := st.Nodes[t.Node]
		if n == nil {
			return fmt.Errorf("trim of unknown node")
		}
		n.TrimmedBefore, n.TrimSnapshot = t.Before, &SnapshotRef{Node: t.Snapshot.Node, Version: t.Snapshot.Version}
		for pos, loc := range n.Records {
			if pos < t.Before {
				freed, _ := st.unrefChunks(loc.Chunks)
				st.Used -= freed
				delete(n.Records, pos)
			}
		}
		st.Changes = append(st.Changes, Change{Seq: seq, Node: n.ID, Operation: "trim", Position: t.Before, At: op.At})
	case kindGC:
		for _, id := range op.GC.Versions {
			v := st.Versions[id]
			if v == nil {
				continue
			}
			for _, hash := range v.Pages {
				freed, _ := st.unrefPage(hash)
				st.Used -= freed
			}
			delete(st.Versions, id)
		}
		for _, id := range op.GC.Stripped {
			v := st.Versions[id]
			if v == nil {
				continue
			}
			for _, hash := range v.Pages {
				freed, _ := st.unrefPage(hash)
				st.Used -= freed
			}
			v.Pages = nil
		}
	case kindSystem:
		if err := applySystem(st, seq, op); err != nil {
			return err
		}
	case kindShare:
		if err := applyShare(st, seq, op); err != nil {
			return err
		}
	case kindLinkUse:
		if err := applyLinkUse(st, op); err != nil {
			return err
		}
	case kindTransfer:
		if err := applyTransfer(st, seq, op); err != nil {
			return err
		}
	case kindGroupRevoke:
		if err := applyGroupRevoke(st, seq, op); err != nil {
			return err
		}
	case kindUnshare:
		if err := applyUnshare(st, seq, op); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown journal operation %q", op.Kind)
	}
	applyCharge(st, op.Charge)
	if op.ID != "" {
		st.Ops[op.ID] = result
	}
	return nil
}
