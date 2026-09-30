// Package docs is the headless collaborative Markdown app of EPIC-026 (E26-T2).
//
// It is real app code, not a fixture: the folder layout, the comment records
// and their reducer, and the paragraph-level three-way merge an editor runs
// when its save loses a race. Storage, sharing, links and change streams are
// the drive's — every action has a `poweur drive` command (see the scenario
// table in epics/EPIC-026-reference-app-scenarios.md), and this package never
// talks to a relay.
//
// Layout of one document:
//
//	Docs/<title>/doc.md          replace-mode file, the document
//	Docs/<title>/comments.jsonl  append-mode file, one comment record each
//	Docs/<title>/assets/         images and attachments
package docs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	Root         = "Docs"
	DocFile      = "doc.md"
	CommentsFile = "comments.jsonl"
	AssetsDir    = "assets"
)

// Paths returns the drive paths of a document's folder and parts.
func Paths(title string) (folder, doc, comments, assets string) {
	folder = "/" + Root + "/" + title
	return folder, folder + "/" + DocFile, folder + "/" + CommentsFile, folder + "/" + AssetsDir
}

// Record is one line of comments.jsonl. The author is not in the record: it
// is the signed author of the append record that carries it.
type Record struct {
	Type   string `json:"type"` // comment.add | comment.resolve
	ID     string `json:"id"`
	Anchor string `json:"anchor,omitempty"` // the paragraph text a comment refers to
	Text   string `json:"text,omitempty"`
}

const (
	TypeAdd     = "comment.add"
	TypeResolve = "comment.resolve"
)

// NewComment returns the record for a new comment on the paragraph anchor.
func NewComment(anchor, text string) (Record, error) {
	if strings.TrimSpace(text) == "" {
		return Record{}, errors.New("empty comment")
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return Record{}, err
	}
	return Record{Type: TypeAdd, ID: hex.EncodeToString(id), Anchor: anchor, Text: text}, nil
}

// ResolveComment returns the record that resolves comment id.
func ResolveComment(id string) Record { return Record{Type: TypeResolve, ID: id} }

// Entry is an append record as the drive returns it: the signed author and
// the record's text.
type Entry struct {
	Author string
	Text   string
}

type Comment struct {
	ID, Author, Anchor, Text string
	Resolved                 bool
	ResolvedBy               string
}

// Reduce folds the comment log in order. A comment is resolved only by its
// author or the document owner; anything malformed, duplicated or resolved by
// someone else is ignored, so a commenter cannot hide another's comment.
func Reduce(owner string, entries []Entry) []Comment {
	var out []Comment
	index := map[string]int{}
	for _, entry := range entries {
		var r Record
		if json.Unmarshal([]byte(entry.Text), &r) != nil || r.ID == "" {
			continue
		}
		switch r.Type {
		case TypeAdd:
			if _, dup := index[r.ID]; dup || strings.TrimSpace(r.Text) == "" {
				continue
			}
			index[r.ID] = len(out)
			out = append(out, Comment{ID: r.ID, Author: entry.Author, Anchor: r.Anchor, Text: r.Text})
		case TypeResolve:
			i, ok := index[r.ID]
			if !ok || out[i].Resolved || (entry.Author != out[i].Author && entry.Author != owner) {
				continue
			}
			out[i].Resolved, out[i].ResolvedBy = true, entry.Author
		}
	}
	return out
}

// Paragraphs splits Markdown into blank-line separated blocks.
func Paragraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, block := range strings.Split(text, "\n\n") {
		if block = strings.Trim(block, "\n"); block != "" {
			out = append(out, block)
		}
	}
	return out
}

func join(paragraphs []string) string {
	if len(paragraphs) == 0 {
		return ""
	}
	return strings.Join(paragraphs, "\n\n") + "\n"
}

// Merge is a paragraph-level three-way merge (diff3). Changes to different
// paragraphs combine; the same paragraph changed differently on both sides
// becomes a conflict block holding both versions, labelled, so nothing is
// silently lost. It returns the merged text and the number of conflicts.
func Merge(base, ours, theirs, oursLabel, theirsLabel string) (string, int) {
	b, o, t := Paragraphs(base), Paragraphs(ours), Paragraphs(theirs)
	mo, mt := matches(b, o), matches(b, t)
	var out []string
	conflicts := 0
	bi, oi, ti := 0, 0, 0
	emit := func(bEnd, oEnd, tEnd int) {
		bs, os, ts := b[bi:bEnd], o[oi:oEnd], t[ti:tEnd]
		switch {
		case equal(os, bs):
			out = append(out, ts...)
		case equal(ts, bs), equal(os, ts):
			out = append(out, os...)
		case len(bs) == len(os) && len(bs) == len(ts):
			// Both sides rewrote the same run of paragraphs one for one
			// (adjacent edits): merge paragraph by paragraph.
			for k := range bs {
				switch {
				case os[k] == bs[k] || os[k] == ts[k]:
					out = append(out, ts[k])
				case ts[k] == bs[k]:
					out = append(out, os[k])
				default:
					conflicts++
					out = append(out, conflictBlock([]string{os[k]}, []string{ts[k]}, oursLabel, theirsLabel))
				}
			}
		default:
			conflicts++
			out = append(out, conflictBlock(os, ts, oursLabel, theirsLabel))
		}
	}
	for i := range b {
		// A sync point: base paragraph i kept by both sides, at or after
		// where each side has got to.
		if mo[i] < oi || mt[i] < ti {
			continue
		}
		emit(i, mo[i], mt[i])
		out = append(out, b[i])
		bi, oi, ti = i+1, mo[i]+1, mt[i]+1
	}
	emit(len(b), len(o), len(t))
	return join(out), conflicts
}

func conflictBlock(ours, theirs []string, oursLabel, theirsLabel string) string {
	return "<<<<<<< " + oursLabel + "\n" + strings.Join(ours, "\n\n") + "\n=======\n" + strings.Join(theirs, "\n\n") + "\n>>>>>>> " + theirsLabel
}

// matches maps each base paragraph to its position in other along a longest
// common subsequence, or -1.
func matches(base, other []string) []int {
	n, m := len(base), len(other)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if base[i] == other[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	out := make([]int, n)
	for i := range out {
		out[i] = -1
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case base[i] == other[j]:
			out[i] = j
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
