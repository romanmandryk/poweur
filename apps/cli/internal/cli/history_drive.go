package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	driveclient "github.com/poweur/cli/internal/drive"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

// Conversations are append files under an encrypted `.poweur/private/messages`
// folder. The file name is a hash of the peer, so a listing does not show who
// the owner talks to. Each record is still sealed to the owner's own key.
// Read marks are one replace file beside that folder.

func (h *historyStore) drive() *driveclient.Files {
	if h.files == nil {
		home, _ := os.UserHomeDir()
		h.files = &driveclient.Files{Client: &driveclient.Client{
			Relay: h.relayURL, Identity: h.identity, Key: h.priv,
			Cache: driveclient.FileCache{Dir: filepath.Join(home, ".poweur", "cache", "drive-chunks")},
		}, EncryptionKey: h.encPriv}
	}
	return h.files
}

func driveGone(err error) bool {
	var status *driveclient.Error
	return errors.As(err, &status) && (status.Status == 503 || status.Code == "drive_unavailable")
}

func (h *historyStore) ensureFolder(ctx context.Context, parent *driveclient.File, name string) (*driveclient.File, error) {
	files := h.drive()
	children, err := files.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name && child.Manifest.Kind == protocol.KindFolder {
			return child, nil
		}
	}
	created, err := files.Create(ctx, parent, name, protocol.KindFolder, nil)
	if err == nil {
		return created, nil
	}
	if !isConflict(err) {
		return nil, err
	}
	children, err = files.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name {
			return child, nil
		}
	}
	return nil, errors.New("history folder was not created")
}

func isConflict(err error) bool {
	var status *driveclient.Error
	return errors.As(err, &status) && status.Status == 409
}

func (h *historyStore) messagesDir(ctx context.Context) (*driveclient.File, error) {
	if h.messages != nil {
		return h.messages, nil
	}
	files := h.drive()
	root, err := files.Root(ctx)
	if err != nil {
		return nil, err
	}
	poweur, err := h.ensureFolder(ctx, root, ".poweur")
	if err != nil {
		return nil, err
	}
	private, err := h.ensureFolder(ctx, poweur, "private")
	if err != nil {
		return nil, err
	}
	messages, err := h.ensureFolder(ctx, private, "messages")
	if err != nil {
		return nil, err
	}
	h.private, h.messages = private, messages
	return messages, nil
}

type positionedRecord struct {
	Position uint64
	Record   idpkg.HistoryRecord
}

func (h *historyStore) logFile(ctx context.Context, peer string) (*driveclient.File, error) {
	dir, err := h.messagesDir(ctx)
	if err != nil {
		return nil, err
	}
	name := idpkg.HistoryLogName(peer)
	files := h.drive()
	children, err := files.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name {
			return child, nil
		}
	}
	return files.CreateAppend(ctx, dir, name)
}

func (h *historyStore) readLog(ctx context.Context, file *driveclient.File) ([]positionedRecord, error) {
	if file.Manifest.Mode != protocol.ModeAppend {
		return nil, errors.New("history file is not an append log")
	}
	rows, err := h.drive().Tail(ctx, file, 1)
	if err != nil {
		return nil, err
	}
	out := make([]positionedRecord, 0, len(rows))
	for _, row := range rows {
		var record idpkg.HistoryRecord
		if err := h.open(row.Plain, &record); err != nil {
			return nil, err
		}
		out = append(out, positionedRecord{Position: row.Position, Record: record})
	}
	return out, nil
}

func (h *historyStore) appendRecord(ctx context.Context, record idpkg.HistoryRecord) error {
	record.Version = idpkg.HistoryVersion
	if err := record.Validate(); err != nil {
		return err
	}
	file, err := h.logFile(ctx, record.Peer(h.identity))
	if err != nil {
		if driveGone(err) {
			return errStorageUnavailable
		}
		return err
	}
	existing, err := h.readLog(ctx, file)
	if err != nil {
		return err
	}
	for _, row := range existing {
		if row.Record.ID == record.ID {
			return nil
		}
	}
	sealed, err := h.seal(record)
	if err != nil {
		return err
	}
	_, err = h.drive().Append(ctx, file, sealed)
	if driveGone(err) {
		return errStorageUnavailable
	}
	return err
}

func (h *historyStore) conversationRecords(ctx context.Context, peer string) ([]positionedRecord, error) {
	dir, err := h.messagesDir(ctx)
	if err != nil {
		if driveGone(err) {
			return nil, errStorageUnavailable
		}
		return nil, err
	}
	name := idpkg.HistoryLogName(peer)
	children, err := h.drive().List(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name {
			return h.readLog(ctx, child)
		}
	}
	return nil, nil
}

func (h *historyStore) allRecords(ctx context.Context) ([]idpkg.HistoryRecord, error) {
	dir, err := h.messagesDir(ctx)
	if err != nil {
		if driveGone(err) {
			return nil, errStorageUnavailable
		}
		return nil, err
	}
	children, err := h.drive().List(ctx, dir)
	if err != nil {
		return nil, err
	}
	var records []idpkg.HistoryRecord
	for _, child := range children {
		if child.Manifest.Mode != protocol.ModeAppend {
			continue
		}
		rows, err := h.readLog(ctx, child)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, row.Record)
		}
	}
	idpkg.SortHistory(records)
	return records, nil
}

func (h *historyStore) readMarks(ctx context.Context) (idpkg.ReadState, error) {
	empty := idpkg.ReadState{Version: idpkg.HistoryVersion, Conversations: map[string]idpkg.ReadMark{}}
	if h.private == nil {
		if _, err := h.messagesDir(ctx); err != nil {
			if driveGone(err) {
				return empty, nil
			}
			return empty, err
		}
	}
	children, err := h.drive().List(ctx, h.private)
	if err != nil {
		return empty, err
	}
	for _, child := range children {
		if child.Name != "read-state.json" {
			continue
		}
		var buf bytes.Buffer
		if err := h.drive().Read(ctx, child, &buf); err != nil {
			return empty, err
		}
		if buf.Len() == 0 {
			return empty, nil
		}
		var state idpkg.ReadState
		if err := h.open(buf.Bytes(), &state); err != nil {
			return empty, err
		}
		if state.Conversations == nil {
			state.Conversations = map[string]idpkg.ReadMark{}
		}
		return state, nil
	}
	return empty, nil
}

func (h *historyStore) writeMarks(ctx context.Context, state idpkg.ReadState) error {
	state.Version = idpkg.HistoryVersion
	if err := state.Validate(); err != nil {
		return err
	}
	if _, err := h.messagesDir(ctx); err != nil {
		if driveGone(err) {
			return errStorageUnavailable
		}
		return err
	}
	sealed, err := h.seal(state)
	if err != nil {
		return err
	}
	children, err := h.drive().List(ctx, h.private)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.Name == "read-state.json" {
			return h.drive().Replace(ctx, child, bytes.NewReader(sealed))
		}
	}
	_, err = h.drive().Create(ctx, h.private, "read-state.json", protocol.KindFile, bytes.NewReader(sealed))
	return err
}
