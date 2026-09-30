package sync

import (
	"bytes"
	"encoding/json"
	gopath "path"
	"strings"
)

// Driver names how a path merges when both sides changed it (EPIC-020
// "Sync and merging"): text three-way, JSON by key, append files by relay
// order, everything else as a conflicted copy.
type Driver string

const (
	DriverText   Driver = "text"
	DriverJSON   Driver = "json"
	DriverAppend Driver = "append"
	DriverCopy   Driver = "copy"
)

// textExts merge three-way; appendExts are created as append files.
var (
	textExts   = map[string]bool{".md": true, ".markdown": true, ".txt": true}
	appendExts = map[string]bool{".jsonl": true, ".log": true, ".csv": true}
)

// DriverFor picks the default driver for a path.
func DriverFor(path string) Driver {
	ext := strings.ToLower(gopath.Ext(path))
	switch {
	case textExts[ext]:
		return DriverText
	case ext == ".json":
		return DriverJSON
	case appendExts[ext]:
		return DriverAppend
	}
	return DriverCopy
}

// CreatesAppend reports whether a new local file at path becomes an append
// file on the drive.
func CreatesAppend(path string) bool { return DriverFor(path) == DriverAppend }

// MergeText is a line-level three-way merge. Changes to different lines
// combine (adjacent one-for-one rewrites merge line by line); the same line
// changed differently on both sides becomes a labelled conflict block that
// keeps both. It returns the merged text and the number of conflicts.
func MergeText(base, ours, theirs, oursLabel, theirsLabel string) (string, int) {
	b, o, t := lines(base), lines(ours), lines(theirs)
	mo, mt := lcsMatches(b, o), lcsMatches(b, t)
	var out []string
	conflicts := 0
	bi, oi, ti := 0, 0, 0
	block := func(os, ts []string) string {
		return "<<<<<<< " + oursLabel + "\n" + strings.Join(os, "") + "=======\n" + strings.Join(ts, "") + ">>>>>>> " + theirsLabel + "\n"
	}
	emit := func(bEnd, oEnd, tEnd int) {
		bs, os, ts := b[bi:bEnd], o[oi:oEnd], t[ti:tEnd]
		switch {
		case same(os, bs):
			out = append(out, ts...)
		case same(ts, bs), same(os, ts):
			out = append(out, os...)
		case len(bs) == len(os) && len(bs) == len(ts):
			for k := range bs {
				switch {
				case os[k] == bs[k] || os[k] == ts[k]:
					out = append(out, ts[k])
				case ts[k] == bs[k]:
					out = append(out, os[k])
				default:
					conflicts++
					out = append(out, block([]string{os[k]}, []string{ts[k]}))
				}
			}
		default:
			conflicts++
			out = append(out, block(os, ts))
		}
	}
	for i := range b {
		if mo[i] < oi || mt[i] < ti {
			continue
		}
		emit(i, mo[i], mt[i])
		out = append(out, b[i])
		bi, oi, ti = i+1, mo[i]+1, mt[i]+1
	}
	emit(len(b), len(o), len(t))
	return strings.Join(out, ""), conflicts
}

// lines splits text keeping each line's newline, and ends the last line
// with one so a missing final newline never reads as a change.
func lines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	} else {
		parts[len(parts)-1] += "\n"
	}
	return parts
}

func lcsMatches(base, other []string) []int {
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

func same(a, b []string) bool {
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

// MergeJSON merges top-level keys of JSON objects: start from theirs, then
// apply every key ours changed relative to base (including removals); local
// keys win where both changed one. ok is false when any side is not an
// object, and the caller falls back to a conflicted copy.
func MergeJSON(base, ours, theirs []byte) ([]byte, bool) {
	var b, o, t map[string]json.RawMessage
	if len(bytes.TrimSpace(base)) == 0 {
		b = map[string]json.RawMessage{}
	} else if json.Unmarshal(base, &b) != nil {
		return nil, false
	}
	if json.Unmarshal(ours, &o) != nil || json.Unmarshal(theirs, &t) != nil || o == nil || t == nil {
		return nil, false
	}
	merged := map[string]json.RawMessage{}
	for k, v := range t {
		merged[k] = v
	}
	for k, v := range o {
		if bv, ok := b[k]; !ok || !jsonEqual(bv, v) {
			merged[k] = v
		}
	}
	for k := range b {
		if _, kept := o[k]; !kept {
			delete(merged, k)
		}
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, false
	}
	return append(out, '\n'), true
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
