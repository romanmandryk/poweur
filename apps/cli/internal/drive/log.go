package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	protocol "github.com/poweur/identity/drive"
)

// Entry is one decrypted append, in relay order.
type Entry struct {
	Position  uint64
	Author    string
	Sequence  uint64
	Plaintext []byte
}

// Reduce folds one entry into application state. State is opaque JSON.
type Reduce func(state json.RawMessage, entry Entry) (json.RawMessage, error)

type authorCursor struct {
	Sequence uint64 `json:"sequence"`
	Previous string `json:"previous"`
}
type snapDoc struct {
	Format  int                     `json:"format"`
	Log     string                  `json:"log"`
	Through uint64                  `json:"through"`
	State   json.RawMessage         `json:"state"`
	Cursors map[string]authorCursor `json:"cursors"`
}

// Log is an append file folded through a reducer, with snapshots and trim.
type Log struct {
	files   *Files
	file    *File
	state   json.RawMessage
	through uint64
	cursors map[string]authorCursor
	reduce  Reduce
}

func (f *Files) snapshotCursor(ctx context.Context, logID string, snap *Snapshot) (uint64, string, bool) {
	if snap == nil {
		return 0, "", false
	}
	file, err := f.Open(ctx, snap.Node)
	if err != nil {
		return 0, "", false
	}
	var buf bytes.Buffer
	if err = f.Read(ctx, file, &buf); err != nil {
		return 0, "", false
	}
	var doc snapDoc
	if json.Unmarshal(buf.Bytes(), &doc) != nil || doc.Format != 1 || doc.Log != logID {
		return 0, "", false
	}
	cur := doc.Cursors[f.Client.Identity]
	return cur.Sequence, cur.Previous, cur.Sequence > 0
}

// OpenLog loads the latest snapshot, then folds every record after it.
func OpenLog(ctx context.Context, files *Files, log *File, reduce Reduce, initial json.RawMessage) (*Log, error) {
	if log.Manifest.Mode != protocol.ModeAppend {
		return nil, errors.New("not an append file")
	}
	if reduce == nil {
		return nil, errors.New("reducer required")
	}
	if len(initial) == 0 {
		initial = json.RawMessage("null")
	}
	out := &Log{files: files, file: log, state: append(json.RawMessage(nil), initial...), cursors: map[string]authorCursor{}, reduce: reduce}
	info, err := files.nodeInfo(ctx, log.Manifest.Node)
	if err != nil {
		return nil, err
	}
	from := uint64(1)
	if info.TrimSnapshot != nil {
		file, err := files.Open(ctx, info.TrimSnapshot.Node)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err = files.Read(ctx, file, &buf); err != nil {
			return nil, err
		}
		var doc snapDoc
		if err = json.Unmarshal(buf.Bytes(), &doc); err != nil || doc.Format != 1 || doc.Log != log.Manifest.Node || doc.Through+1 != info.TrimmedBefore {
			return nil, errors.New("trim snapshot does not match the log")
		}
		out.state, out.through, out.cursors = doc.State, doc.Through, doc.Cursors
		if out.cursors == nil {
			out.cursors = map[string]authorCursor{}
		}
		from = info.TrimmedBefore
	}
	if info.Position >= from {
		records, err := files.Tail(ctx, log, from)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if err = out.fold(record); err != nil {
				return nil, err
			}
		}
	}
	seq, prev, err := files.authorCursor(ctx, log)
	if err != nil {
		return nil, err
	}
	if seq > 0 {
		out.cursors[files.Client.Identity] = authorCursor{Sequence: seq, Previous: prev}
	}
	return out, nil
}
func (l *Log) fold(record Record) error {
	next, err := l.reduce(l.state, Entry{Position: record.Position, Author: record.Author, Sequence: record.Sequence, Plaintext: record.Plain})
	if err != nil {
		return err
	}
	l.state, l.through = next, record.Position
	return nil
}
func (l *Log) State() json.RawMessage { return l.state }
func (l *Log) Through() uint64        { return l.through }

// Append writes one record and folds it into the local state.
func (l *Log) Append(ctx context.Context, plaintext []byte) error {
	pos, err := l.files.Append(ctx, l.file, plaintext)
	if err != nil {
		return err
	}
	seq, prev, err := l.files.authorCursor(ctx, l.file)
	if err != nil {
		return err
	}
	l.cursors[l.files.Client.Identity] = authorCursor{Sequence: seq, Previous: prev}
	next, err := l.reduce(l.state, Entry{Position: pos, Author: l.files.Client.Identity, Sequence: seq, Plaintext: plaintext})
	if err != nil {
		return err
	}
	l.state, l.through = next, pos
	return nil
}

// Snapshot writes the folded state and trims the log through that position.
// An existing file at parent/name is replaced.
func (l *Log) Snapshot(ctx context.Context, parent *File, name string) (*File, error) {
	doc := snapDoc{Format: 1, Log: l.file.Manifest.Node, Through: l.through, State: l.state, Cursors: l.cursors}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	children, err := l.files.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	normalized, err := protocol.NormalizeName(name)
	if err != nil {
		return nil, err
	}
	var file *File
	for _, child := range children {
		if child.Name == normalized {
			file = child
			break
		}
	}
	if file == nil {
		file, err = l.files.Create(ctx, parent, normalized, protocol.KindFile, bytes.NewReader(raw))
	} else {
		err = l.files.Replace(ctx, file, bytes.NewReader(raw))
	}
	if err != nil {
		return nil, err
	}
	if l.through >= 1 {
		_, err = l.files.Client.Commit(ctx, Commit{Trim: &Trim{Node: l.file.Manifest.Node, Before: l.through + 1, Snapshot: Snapshot{Node: file.Manifest.Node, Version: file.Manifest.Version}}})
		if err != nil {
			return nil, err
		}
	}
	return file, nil
}

// Follow subscribes to the drive and folds appends of this log.
func (l *Log) Follow(ctx context.Context, on func(Entry) error) error {
	return l.files.Client.Subscribe(ctx, func(event Event) error {
		if event.Type != "drive.changed" {
			return nil
		}
		var change struct {
			Node      string `json:"node"`
			Operation string `json:"operation"`
		}
		if json.Unmarshal(event.Drive, &change) != nil || change.Node != l.file.Manifest.Node || change.Operation != "append" {
			return nil
		}
		records, err := l.files.Tail(ctx, l.file, l.through+1)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Position <= l.through {
				continue
			}
			entry := Entry{Position: record.Position, Author: record.Author, Sequence: record.Sequence, Plaintext: record.Plain}
			if err = l.fold(record); err != nil {
				return err
			}
			seq, prev, err := l.files.authorCursor(ctx, l.file)
			if err != nil {
				return err
			}
			if seq > 0 {
				l.cursors[l.files.Client.Identity] = authorCursor{seq, prev}
			}
			if on != nil {
				if err = on(entry); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
