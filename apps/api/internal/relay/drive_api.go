package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/identity/drive"
)

// Drive API (EPIC-020 E20-T5):
//
//	GET  /drive/{identity}                                   root, usage, quota, journal position
//	POST /drive/{identity}/chunks/missing                    which chunks to upload, and where
//	PUT  /drive/{identity}/chunks/{chunk}                    upload one encrypted chunk
//	POST /drive/{identity}/commit                            manifest, append records or trim
//	GET  /drive/{identity}/changes?cursor=N                  changes after sequence N
//	GET  /drive/{identity}/nodes/{node}                      a node at its head
//	GET  /drive/{identity}/nodes/{node}/children             live children, cursor-paged
//	GET  /drive/{identity}/nodes/{node}/history              retained versions
//	GET  /drive/{identity}/nodes/{node}/records?from=N       an append file from a position
//	GET  /drive/{identity}/nodes/{node}/chunks/{chunk}       a chunk of an append record
//	GET  /drive/{identity}/nodes/{node}/versions/{version}   a signed manifest
//	GET  /drive/{identity}/nodes/{node}/versions/{version}/pages/{page}
//	GET  /drive/{identity}/nodes/{node}/versions/{version}/chunks/{chunk}
//
//	GET  /drive/{identity}/shares                            shares the caller may see
//	GET  /drive/{identity}/events                            drive.changed stream for owner and members
//
// Every request is authenticated like an inbox pickup (a one-shot challenge
// signed by the identity key or a session key), and the caller may be a
// member homed on another relay. The engine decides each request from the
// drive's shares (E20-T7): reads need read on the node, commits the role
// their operation needs. A chunk is only ever read through a version or
// record that references it: knowing its hash is not enough.
//
// Durable commits notify the owner's event stream (`GET /events/{identity}`)
// and every drive stream whose caller may see the change; appends of up to
// 16 KiB ride inline. Revoking a member's last share closes their stream.
// Events are advisory — a client that reconnects reads `changes`.

const (
	maxDriveCommitBytes  = 16 << 20
	maxInlineRecordBytes = 16 << 10
	driveUploadURLTTL    = 15 * time.Minute
	// driveCommitRetries bounds how often a commit that lost a publish race
	// to another relay process is re-validated and retried.
	driveCommitRetries = 3
)

// driveEvent is what a `drive.changed` event carries.
type driveEvent struct {
	Drive     string                    `json:"drive"`
	Seq       uint64                    `json:"seq"`
	Node      string                    `json:"node,omitempty"`
	Path      string                    `json:"path,omitempty"`
	Operation string                    `json:"operation"`
	Version   string                    `json:"version,omitempty"`
	Position  uint64                    `json:"position,omitempty"`
	Records   []engine.PositionedRecord `json:"records,omitempty"`
	Share     string                    `json:"share,omitempty"`
	Member    string                    `json:"member,omitempty"`
}

func newDriveEngine(store provider.Store, s *Server) *engine.Engine {
	return engine.New(engine.Options{
		Store: store,
		Keys: func(ctx context.Context, identity string) (ed25519.PublicKey, error) {
			return s.resolveIdentityPublicKey(ctx, identity)
		},
		Quota:    s.storageQuota,
		OnCommit: s.notifyDriveChange,
		Groups:   s.resolveGroup,
	})
}

// notifyDriveChange runs under the drive's lock, so it only queues.
func (s *Server) notifyDriveChange(driveID string, change engine.Change, records []engine.PositionedRecord, audience engine.Audience) {
	event := &driveEvent{Drive: driveID, Seq: change.Seq, Node: change.Node, Path: change.Path, Operation: change.Operation, Version: change.Version, Position: change.Position, Share: change.Share, Member: change.Member}
	if len(records) > 0 {
		if raw, err := json.Marshal(records); err == nil && len(raw) <= maxInlineRecordBytes {
			event.Records = records
		}
	}
	if change.Operation == "share" {
		s.noteGroupShare(change.Member, driveID)
	}
	stream := streamEvent{Type: "drive.changed", Identity: driveID, Drive: event, Timestamp: change.At.UTC().Format(time.RFC3339)}
	s.hub.publish(driveID, stream)
	s.driveStreams.publish(driveID, stream, change, audience)
}

// driveCaller authenticates the caller — the owner, a member homed on any
// relay — for a drive hosted here. What they may do is decided per request.
func (s *Server) driveCaller(w http.ResponseWriter, r *http.Request) (driveID, actor string, ok bool) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "drive_unavailable", "this relay has no drive storage")
		return "", "", false
	}
	driveID = strings.ToLower(strings.TrimSpace(r.PathValue("identity")))
	if !s.identities.Exists(driveID) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return "", "", false
	}
	if r.Header.Get(linkHeader) != "" {
		// Opening a link is listing its share; other requests only use it.
		actor, ok = s.linkCaller(w, r, driveID, r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/shares"))
		return driveID, actor, ok
	}
	actor, ok = s.authenticateCaller(w, r, false)
	if !ok {
		return "", "", false
	}
	actor = strings.ToLower(actor)
	if header := r.Header.Get(groupRosterHeader); header != "" {
		if err := s.presentRoster(r.Context(), header, actor); err != nil {
			writeError(w, http.StatusForbidden, "group_roster", "group roster refused: "+err.Error())
			return "", "", false
		}
	}
	return driveID, actor, true
}

// driveNode authenticates the caller and checks need on the path's node.
func (s *Server) driveNode(w http.ResponseWriter, r *http.Request, need string) (driveID, actor string, ok bool) {
	driveID, actor, ok = s.driveCaller(w, r)
	if !ok {
		return "", "", false
	}
	if err := s.engine.Authorize(r.Context(), driveID, actor, r.PathValue("node"), need); err != nil {
		s.writeDriveError(w, err)
		return "", "", false
	}
	return driveID, actor, true
}

// driveUploader lets the owner and members who may add content upload.
func (s *Server) driveUploader(w http.ResponseWriter, r *http.Request, driveID, actor string) bool {
	allowed, err := s.engine.CanUpload(r.Context(), driveID, actor)
	if err != nil {
		s.writeDriveError(w, err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "no role on this drive adds content")
		return false
	}
	return true
}

// handleDriveNodeShares lists the shares on a node and its ancestors to
// anyone who may read the node, so they can verify version authors.
func (s *Server) handleDriveNodeShares(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	shares, revoked, err := s.engine.SharesOn(r.Context(), driveID, r.PathValue("node"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if shares == nil {
		shares = []drive.Share{}
	}
	if revoked == nil {
		revoked = []engine.RevokedShare{}
	}
	noStore(w)
	// Revoked shares are evidence for versions written while they stood.
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares, "revoked": revoked})
}

// handleDriveShares lists the shares the caller may see: every share for the
// owner; for a member their own (with their sealed node keys) and those on
// nodes they administer.
func (s *Server) handleDriveShares(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	shares, err := s.engine.Shares(r.Context(), driveID, actor)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if len(shares) == 0 && !engine.IsOwner(driveID, actor) {
		writeError(w, http.StatusForbidden, "forbidden", "no shares on this drive")
		return
	}
	if shares == nil {
		shares = []drive.Share{}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

// writeDriveError maps engine errors to statuses.
func (s *Server) writeDriveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound), errors.Is(err, provider.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such node, version or chunk")
	case errors.Is(err, engine.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, engine.ErrExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, engine.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, engine.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_commit", err.Error())
	case errors.Is(err, engine.ErrQuota):
		s.writeQuotaExceeded(w)
	case errors.Is(err, engine.ErrCap):
		writeError(w, http.StatusTooManyRequests, "share_limit", err.Error())
	case errors.Is(err, engine.ErrResync):
		writeError(w, http.StatusGone, "resync", err.Error())
	case errors.Is(err, engine.ErrStale):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "retry", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "storage_error", "drive storage failed")
	}
}

func (s *Server) handleDriveGet(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if !engine.IsOwner(driveID, actor) {
		// Members find their way in through their shares.
		writeError(w, http.StatusForbidden, "forbidden", "only the owner reads the drive root; members list /shares")
		return
	}
	root, err := s.engine.Root(r.Context(), driveID)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	used, err := s.engine.Usage(r.Context(), driveID)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"drive": driveID, "root": root, "used": used, "quota": s.storageQuota(driveID)})
}

type chunkUpload struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type missingChunk struct {
	drive.ChunkRef
	Upload chunkUpload `json:"upload"`
}

func (s *Server) handleDriveMissing(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if !s.driveUploader(w, r, driveID, actor) {
		return
	}
	var body struct {
		Chunks []drive.ChunkRef `json:"chunks"`
	}
	if !readDriveJSON(w, r, 1<<20, &body) {
		return
	}
	if len(body.Chunks) > drive.PageSize {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", fmt.Sprintf("at most %d chunks per request", drive.PageSize))
		return
	}
	missing, err := s.engine.Missing(r.Context(), driveID, body.Chunks)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	out := make([]missingChunk, 0, len(missing))
	for _, ref := range missing {
		upload := chunkUpload{Method: http.MethodPut, URL: "/drive/" + driveID + "/chunks/" + ref.ID}
		signed, err := s.engine.UploadURL(r.Context(), driveID, ref, driveUploadURLTTL)
		switch {
		case err == nil:
			upload = chunkUpload{Method: http.MethodPut, URL: signed.URL, Headers: signed.Headers}
		case errors.Is(err, provider.ErrUnsupported):
		default:
			s.writeDriveError(w, err)
			return
		}
		out = append(out, missingChunk{ChunkRef: ref, Upload: upload})
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"missing": out})
}

func (s *Server) handleDriveChunkPut(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if !s.driveUploader(w, r, driveID, actor) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, drive.MaxChunkBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read chunk")
		return
	}
	if len(data) > drive.MaxChunkBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("a chunk is at most %d bytes", drive.MaxChunkBytes))
		return
	}
	if drive.ChunkID(data) != r.PathValue("chunk") {
		writeError(w, http.StatusUnprocessableEntity, "hash_mismatch", "chunk bytes do not match the chunk ID")
		return
	}
	ref, err := s.engine.PutChunk(r.Context(), driveID, data)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

type driveCommitBody struct {
	ID       string               `json:"id"`
	Manifest *drive.Manifest      `json:"manifest,omitempty"`
	Pages    []drive.ChunkPage    `json:"pages,omitempty"`
	Records  []drive.AppendRecord `json:"records,omitempty"`
	Trim     *engine.Trim         `json:"trim,omitempty"`
	Share    *drive.Share         `json:"share,omitempty"`
	Unshare  *engine.Unshare      `json:"unshare,omitempty"`
	Transfer *engine.Transfer     `json:"transfer,omitempty"`
}

func (s *Server) handleDriveCommit(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	var body driveCommitBody
	if !readDriveJSON(w, r, maxDriveCommitBytes, &body) {
		return
	}
	// The caller commits only what it signed itself: a signed manifest or
	// record someone else obtained cannot be replayed through their session.
	// A link holder signs as a guest (a key it made for this write).
	link := strings.HasPrefix(actor, "link:")
	ownWrite := func(author string) bool {
		if link {
			return drive.IsGuest(author)
		}
		return author == actor
	}
	if body.Manifest != nil && !ownWrite(body.Manifest.Author) {
		writeError(w, http.StatusForbidden, "forbidden", "the manifest author must be the caller")
		return
	}
	for _, record := range body.Records {
		if !ownWrite(record.Author) || record.Author != body.Records[0].Author {
			writeError(w, http.StatusForbidden, "forbidden", "every record author must be the caller")
			return
		}
	}
	if link && !s.linkProofOfWork(w, r, driveID, strings.TrimPrefix(actor, "link:")) {
		return
	}
	if body.Share != nil && body.Share.Issuer != actor {
		writeError(w, http.StatusForbidden, "forbidden", "the share issuer must be the caller")
		return
	}
	req := engine.Request{ID: body.ID, Manifest: body.Manifest, Pages: body.Pages, Records: body.Records, Trim: body.Trim,
		Share: body.Share, Unshare: body.Unshare, Transfer: body.Transfer, Author: actor}
	var result engine.Result
	var err error
	for attempt := 0; attempt < driveCommitRetries; attempt++ {
		if result, err = s.engine.Commit(r.Context(), driveID, req); !errors.Is(err, engine.ErrStale) {
			break
		}
	}
	if errors.Is(err, engine.ErrConflict) && body.Manifest != nil {
		// Tell the writer which head it lost to, so it can merge.
		if info, nodeErr := s.engine.Node(r.Context(), driveID, body.Manifest.Node); nodeErr == nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "conflict", "detail": err.Error(), "head": info.Head})
			return
		}
	}
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": result.Seq, "head": result.Head, "positions": result.Positions})
}

func (s *Server) handleDriveChanges(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	cursor, ok := queryUint(w, r, "cursor")
	if !ok {
		return
	}
	limit, ok := queryUint(w, r, "limit")
	if !ok {
		return
	}
	changes, next, err := s.engine.ChangesFor(r.Context(), driveID, actor, cursor, int(limit))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if changes == nil {
		changes = []engine.Change{}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes, "cursor": strconv.FormatUint(next, 10)})
}

func (s *Server) handleDriveNode(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if err := s.engine.Authorize(r.Context(), driveID, actor, r.PathValue("node"), drive.RoleRead); err != nil {
		s.writeDriveError(w, err)
		return
	}
	// Forwarding metadata is private too. Revoked members must discover
	// their destination through the new share, not their retired grant.
	if moved, found, err := s.engine.Moved(r.Context(), driveID, r.PathValue("node")); err == nil && found {
		noStore(w)
		writeJSON(w, http.StatusGone, map[string]any{"error": "moved", "detail": "this node moved to another drive", "moved_to": moved})
		return
	}
	info, err := s.engine.Node(r.Context(), driveID, r.PathValue("node"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	noStore(w)
	w.Header().Set("ETag", `"`+info.Head+`"`)
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDriveChildren(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	limit, ok := queryUint(w, r, "limit")
	if !ok {
		return
	}
	children, next, err := s.engine.Children(r.Context(), driveID, r.PathValue("node"), r.URL.Query().Get("cursor"), int(limit))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if children == nil {
		children = []engine.NodeInfo{}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"children": children, "cursor": next})
}

func (s *Server) handleDriveHistory(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	versions, err := s.engine.History(r.Context(), driveID, r.PathValue("node"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if versions == nil {
		versions = []string{}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) handleDriveRecords(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	from, ok := queryUint(w, r, "from")
	if !ok {
		return
	}
	limit, ok := queryUint(w, r, "limit")
	if !ok {
		return
	}
	nodeID := r.PathValue("node")
	records, next, err := s.engine.Records(r.Context(), driveID, nodeID, from, int(limit))
	if errors.Is(err, engine.ErrResync) {
		// Name the snapshot that replaces the trimmed prefix.
		info, _ := s.engine.Node(r.Context(), driveID, nodeID)
		writeJSON(w, http.StatusGone, map[string]any{"error": "resync", "detail": err.Error(), "trimmed_before": info.TrimmedBefore, "snapshot": info.TrimSnapshot})
		return
	}
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	if records == nil {
		records = []engine.PositionedRecord{}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "next": next})
}

func (s *Server) handleDriveVersion(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	manifest, err := s.engine.Version(r.Context(), driveID, r.PathValue("node"), r.PathValue("version"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	immutable(w)
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleDrivePage(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	page, err := s.engine.Page(r.Context(), driveID, r.PathValue("node"), r.PathValue("version"), r.PathValue("page"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	immutable(w)
	writeJSON(w, http.StatusOK, page)
}

// handleDriveChunk serves a chunk through a version (or, without one, an
// append record) of the node that references it.
func (s *Server) handleDriveChunk(w http.ResponseWriter, r *http.Request) {
	driveID, _, ok := s.driveNode(w, r, drive.RoleRead)
	if !ok {
		return
	}
	key, err := s.engine.ChunkKey(r.Context(), driveID, r.PathValue("node"), r.PathValue("version"), r.PathValue("chunk"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	obj, err := s.drive.Get(r.Context(), key, nil)
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	immutable(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.Data)))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(obj.Data))
}

func readDriveJSON(w http.ResponseWriter, r *http.Request, limit int64, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read body")
		return false
	}
	if int64(len(raw)) > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("request exceeds %d bytes", limit))
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func queryUint(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", name+" must be a non-negative integer")
		return 0, false
	}
	return value, true
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

// immutable marks content-addressed responses. They stay private: every read
// is authorized.
func immutable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
}
