package guestbook

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"

	idpkg "github.com/poweur/identity"
)

// Limits a guestbook starts with.
const (
	// DefaultMaxMessage is the longest message, in characters of Markdown.
	DefaultMaxMessage = 1000
	// DefaultMinInterval is the least time between two posts by one identity.
	DefaultMinInterval = 5 * time.Second
	// DefaultPageEntries is how many entries fill one log file before the next
	// one starts. Every post rewrites the newest file (a drive file is
	// replaced, not appended to, so it can be served at /pub), and a rewrite
	// keeps the superseded version until the relay collects it. A small page
	// keeps that history small however busy the guestbook gets.
	DefaultPageEntries = 25

	maxLines = 50
)

// Entry is one signed line in the guestbook.
type Entry struct {
	ID       string `json:"id"`
	Identity string `json:"identity"`
	// Message is Markdown, the way it is stored in the log.
	Message string `json:"message"`
	At      string `json:"at"`
	// HTML is Message rendered, for the page. Set only on API responses.
	HTML string `json:"html,omitempty"`
}

var (
	ErrEmpty    = errors.New("write a message first")
	ErrNotUTF8  = errors.New("the message is not valid text")
	ErrTooLong  = errors.New("the message is too long")
	ErrTooTall  = errors.New("the message has too many lines")
	errBadEntry = errors.New("not a guestbook entry")
)

// The log is a Markdown file anyone can read at /pub. Each entry is
//
//	<!-- gb:entry {"id":"…","identity":"…","at":"…"} -->
//	### alice.poweur.net · 2026-10-01 16:40 UTC
//
//	the message
//
// The comment is what a program reads back and is invisible when the file is
// rendered; the heading is for people. A line starting with "<!--" can only be
// ours: cleanMessage escapes every "<" a visitor could use to begin one.
const logMarker = "<!-- gb:entry "

var logMarkerLine = regexp.MustCompile(`^<!-- gb:entry (\{.*\}) -->$`)

func newEntryID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gb_" + hex.EncodeToString(b), nil
}

// formatEntry renders e as one block of the log, ending in a blank line.
func formatEntry(e Entry) string {
	meta, _ := json.Marshal(struct {
		ID       string `json:"id"`
		Identity string `json:"identity"`
		At       string `json:"at"`
	}{e.ID, e.Identity, e.At})
	when := e.At
	if t, err := time.Parse(time.RFC3339, e.At); err == nil {
		when = t.UTC().Format("2006-01-02 15:04 UTC")
	}
	return fmt.Sprintf("%s%s -->\n### %s · %s\n\n%s\n\n", logMarker, meta, e.Identity, when, e.Message)
}

// parseLog reads the entries back, oldest first. It is forgiving, because the
// file is also where an operator removes an entry by hand: text before the
// first marker and blocks with an unreadable marker are skipped.
func parseLog(data []byte) []Entry {
	var out []Entry
	var cur *Entry
	var body []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Message = strings.TrimSpace(strings.Join(body, "\n"))
		if cur.Message != "" {
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	skipHeading := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, logMarker) {
			flush()
			if m := logMarkerLine.FindStringSubmatch(line); m != nil {
				if e, err := parseMarker(m[1]); err == nil {
					cur, skipHeading = &e, true
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if skipHeading {
			skipHeading = false
			if strings.HasPrefix(line, "### ") {
				continue
			}
		}
		body = append(body, line)
	}
	flush()
	return out
}

func parseMarker(raw string) (Entry, error) {
	var e Entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return Entry{}, errBadEntry
	}
	if e.ID == "" || idpkg.ValidateIdentityName(e.Identity) != nil {
		return Entry{}, errBadEntry
	}
	if _, err := time.Parse(time.RFC3339, e.At); err != nil {
		return Entry{}, errBadEntry
	}
	e.Message = ""
	return e, nil
}

// cleanMessage turns what a visitor typed into what the log stores. max counts
// characters (runes) of the Markdown the visitor sent, before the escaping
// below adds any.
func cleanMessage(raw string, max int) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrNotUTF8
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
			// Direction overrides and BOMs: they can make one line read as another.
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	msg := strings.TrimSpace(strings.Join(lines, "\n"))
	for strings.Contains(msg, "\n\n\n") {
		msg = strings.ReplaceAll(msg, "\n\n\n", "\n\n")
	}
	if msg == "" {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(msg) > max {
		return "", fmt.Errorf("%w: at most %d characters", ErrTooLong, max)
	}
	if strings.Count(msg, "\n")+1 > maxLines {
		return "", fmt.Errorf("%w: at most %d", ErrTooTall, maxLines)
	}
	return neutralize(msg), nil
}

// neutralize keeps a message from being anything but text and light
// formatting, wherever the log is rendered. Raw HTML is escaped, images are
// turned into literal text (a remote image tracks every reader), and a line
// cannot become a heading, which would let a message pass for an entry header.
// Text between backticks stays as typed, so `a<b` reads right; it is checked
// per line, so it can never hold a line start.
func neutralize(msg string) string {
	lines := strings.Split(msg, "\n")
	for i, l := range lines {
		if rest := strings.TrimLeft(l, " "); strings.HasPrefix(rest, "#") {
			l = l[:len(l)-len(rest)] + `\` + rest
		}
		lines[i] = escapeOutsideCode(l)
	}
	return strings.Join(lines, "\n")
}

func escapeOutsideCode(line string) string {
	var out strings.Builder
	rs := []rune(line)
	for i := 0; i < len(rs); {
		switch rs[i] {
		case '`':
			n := 1
			for i+n < len(rs) && rs[i+n] == '`' {
				n++
			}
			if end := closingRun(rs, i+n, n); end >= 0 {
				out.WriteString(string(rs[i:end]))
				i = end
				continue
			}
			out.WriteString(string(rs[i : i+n]))
			i += n
		case '\\':
			// An existing escape stays as it is, and so does what it escapes.
			out.WriteRune(rs[i])
			if i+1 < len(rs) {
				out.WriteRune(rs[i+1])
			}
			i += 2
		case '<':
			out.WriteString(`\<`)
			i++
		case '!':
			if i+1 < len(rs) && rs[i+1] == '[' {
				out.WriteString(`!\[`)
				i += 2
				continue
			}
			out.WriteRune('!')
			i++
		default:
			out.WriteRune(rs[i])
			i++
		}
	}
	return out.String()
}

// closingRun finds a run of exactly n backticks at or after from and returns
// the index just past it, or -1.
func closingRun(rs []rune, from, n int) int {
	for i := from; i < len(rs); {
		if rs[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(rs) && rs[i+run] == '`' {
			run++
		}
		if run == n {
			return i + run
		}
		i += run
	}
	return -1
}

// markdown renders messages: paragraphs, emphasis, code, links, lists and
// quotes. Headings, raw HTML and setext underlines are switched off, a newline
// is a line break (it is what people type in a box like this), and the
// renderer drops javascript:, data: and similar link targets.
var markdown = goldmark.New(
	goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(
			util.Prioritized(parser.NewThematicBreakParser(), 200),
			util.Prioritized(parser.NewListParser(), 300),
			util.Prioritized(parser.NewListItemParser(), 400),
			util.Prioritized(parser.NewCodeBlockParser(), 500),
			util.Prioritized(parser.NewFencedCodeBlockParser(), 700),
			util.Prioritized(parser.NewBlockquoteParser(), 800),
			util.Prioritized(parser.NewParagraphParser(), 1000),
		),
		parser.WithInlineParsers(
			util.Prioritized(parser.NewCodeSpanParser(), 100),
			util.Prioritized(parser.NewLinkParser(), 200),
			util.Prioritized(parser.NewAutoLinkParser(), 300),
			util.Prioritized(parser.NewEmphasisParser(), 500),
		),
		parser.WithParagraphTransformers(),
	)),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// renderHTML is the one place a message becomes markup. The page puts the
// result in the document, so it must stay escaped, never "unsafe".
func renderHTML(md string) string {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(md), &buf); err != nil {
		return "<p>" + htmlEscape(md) + "</p>"
	}
	return buf.String()
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// spacer enforces a minimum gap between one identity's posts.
type spacer struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
}

func newSpacer(interval time.Duration) *spacer {
	return &spacer{interval: interval, last: map[string]time.Time{}}
}

// reserve claims id's next post. If the last one was too recent it returns how
// long to wait and claims nothing. prev lets the caller give the claim back
// when the post then fails, so a storage error does not cost a visitor a wait.
func (s *spacer) reserve(id string, now time.Time) (prev time.Time, wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev = s.last[id]
	if !prev.IsZero() {
		if gap := now.Sub(prev); gap < s.interval {
			return prev, s.interval - gap
		}
	}
	s.last[id] = now
	if len(s.last) > 4096 {
		for k, t := range s.last {
			if now.Sub(t) >= s.interval {
				delete(s.last, k)
			}
		}
	}
	return prev, 0
}

func (s *spacer) release(id string, prev time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev.IsZero() {
		delete(s.last, id)
		return
	}
	s.last[id] = prev
}
