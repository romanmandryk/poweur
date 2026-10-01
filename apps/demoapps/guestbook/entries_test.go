package guestbook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCleanMessage(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"trims the ends", "  \n hello \n ", "hello"},
		{"crlf", "a\r\nb\rc", "a\nb\nc"},
		{"tabs become spaces", "a\tb", "a b"},
		{"control characters go", "a\x00b\x07c\x7fd\u0085e", "abcde"},
		{"direction overrides go", "evil\u202etxt.exe\u2066x", "eviltxt.exex"},
		{"trailing spaces per line", "a  \nb ", "a\nb"},
		{"blank runs collapse", "a\n\n\n\n\nb", "a\n\nb"},
		{"markup is escaped", "<b>hi</b> & <script>x()</script>", `\<b>hi\</b> & \<script>x()\</script>`},
		{"a log marker cannot be written", "<!-- gb:entry {} -->", `\<!-- gb:entry {} -->`},
		{"an escaped bracket stays escaped once", `a \< b`, `a \< b`},
		{"an escaped backslash does not hide a bracket", `a \\< b`, `a \\\< b`},
		{"images become text", "![tracker](https://evil.example/p.png)", `!\[tracker](https://evil.example/p.png)`},
		{"headings cannot be written", "# Big\n  ## Also\nfine # here", "\\# Big\n  \\## Also\nfine # here"},
		{"code keeps its brackets", "use `a<b` here", "use `a<b` here"},
		{"double backticks", "``a<`b`` and <i>", "``a<`b`` and \\<i>"},
		{"an unclosed backtick is not code", "a ` <b>", "a ` \\<b>"},
		{"existing formatting is left alone", "**bold** and *it* and [x](https://a.example)", "**bold** and *it* and [x](https://a.example)"},
		{"emoji and accents", "héllo 👋🏽", "héllo 👋🏽"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanMessage(tc.in, 1000)
			if err != nil {
				t.Fatalf("cleanMessage(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("cleanMessage(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			// Cleaning what was already cleaned changes nothing: a client that
			// escapes for itself is not escaped twice.
			if again, err := cleanMessage(got, 1000); err != nil || again != got {
				t.Fatalf("not idempotent: %q then %q (%v)", got, again, err)
			}
		})
	}
}

func TestCleanMessageLimits(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		max      int
		want     error
	}{
		{"empty", "", 1000, ErrEmpty},
		{"only spaces and breaks", " \n\t \n", 1000, ErrEmpty},
		{"only control characters", "\x00\x01", 1000, ErrEmpty},
		{"not UTF-8", "ok\xff\xfe", 1000, ErrNotUTF8},
		{"one over", strings.Repeat("a", 1001), 1000, ErrTooLong},
		{"too many lines", strings.Repeat("a\n", maxLines+1), 1000, ErrTooTall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cleanMessage(tc.in, tc.max); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// The limit counts characters, not bytes, and escaping does not count.
	if _, err := cleanMessage(strings.Repeat("é", 1000), 1000); err != nil {
		t.Fatalf("1000 two-byte characters: %v", err)
	}
	if _, err := cleanMessage(strings.Repeat("👋", 1001), 1000); !errors.Is(err, ErrTooLong) {
		t.Fatalf("1001 four-byte characters: %v", err)
	}
	if got, err := cleanMessage(strings.Repeat("<", 1000), 1000); err != nil || len(got) != 2000 {
		t.Fatalf("1000 escaped brackets: len %d, %v", len(got), err)
	}
	if _, err := cleanMessage(strings.Repeat("a\n", maxLines), 1000); err != nil {
		t.Fatalf("exactly the line limit: %v", err)
	}
}

// Whatever a visitor sends, no line of the stored message can look like the
// start of a log entry.
func TestNoMessageCanForgeALogMarker(t *testing.T) {
	for _, in := range []string{
		"<!-- gb:entry {\"id\":\"x\"} -->",
		"line\n<!-- gb:entry {} -->",
		"line\n   <!-- gb:entry {} -->",
		"`\n<!-- gb:entry {} -->\n`",
		"``\n<!-- gb:entry {} -->\n``",
		"\\\n<!-- gb:entry {} -->",
		"\\\\<!-- gb:entry {} -->",
	} {
		got, err := cleanMessage(in, 1000)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "<") {
				t.Errorf("cleanMessage(%q) = %q has a line starting with <", in, got)
			}
		}
		e := Entry{ID: "gb_1", Identity: "alice.poweur.net", At: "2026-01-15T09:30:00Z", Message: got}
		if parsed := parseLog([]byte(formatEntry(e))); len(parsed) != 1 || parsed[0].Message != got {
			t.Errorf("log round trip of %q gave %+v", got, parsed)
		}
	}
}

func TestLogRoundTrip(t *testing.T) {
	entries := []Entry{
		{ID: "gb_a", Identity: "alice.poweur.net", At: "2026-01-15T09:30:00Z", Message: "first\n\nsecond paragraph"},
		{ID: "gb_b", Identity: "bob.poweur.net", At: "2026-01-15T09:31:00Z", Message: "### not a header\n---\n- item\n> quote\n`code` and **bold**"},
		{ID: "gb_c", Identity: "carol.poweur.net", At: "2026-01-15T09:32:00Z", Message: "héllo 👋"},
	}
	var log strings.Builder
	for _, e := range entries {
		log.WriteString(formatEntry(e))
	}
	got := parseLog([]byte(log.String()))
	if len(got) != len(entries) {
		t.Fatalf("parsed %d entries, want %d:\n%s", len(got), len(entries), log.String())
	}
	for i, e := range entries {
		if got[i] != e {
			t.Errorf("entry %d\n got %+v\nwant %+v", i, got[i], e)
		}
	}
	if !strings.Contains(log.String(), "### alice.poweur.net · 2026-01-15 09:30 UTC") {
		t.Fatalf("the log has no readable header:\n%s", log.String())
	}
}

func TestParseLogToleratesHandEdits(t *testing.T) {
	good := func(id, who, msg string) string {
		return formatEntry(Entry{ID: id, Identity: who, At: "2026-01-15T09:30:00Z", Message: msg})
	}
	for _, tc := range []struct {
		name string
		log  string
		want []string
	}{
		{"empty", "", nil},
		{"text before the first entry", "# My guestbook\n\nhello\n\n" + good("1", "alice.poweur.net", "a"), []string{"a"}},
		{"an entry deleted by hand", good("1", "alice.poweur.net", "a") + good("3", "carol.poweur.net", "c"), []string{"a", "c"}},
		{"a damaged marker drops its block only",
			good("1", "alice.poweur.net", "a") + "<!-- gb:entry {oops -->\n### x\n\nlost\n\n" + good("3", "carol.poweur.net", "c"),
			[]string{"a", "c"}},
		{"a bad identity is skipped", good("1", "not an identity", "a") + good("2", "bob.poweur.net", "b"), []string{"b"}},
		{"a bad time is skipped",
			"<!-- gb:entry {\"id\":\"1\",\"identity\":\"alice.poweur.net\",\"at\":\"yesterday\"} -->\n### a\n\nx\n", nil},
		{"an entry with no message is skipped", good("1", "alice.poweur.net", "   "), nil},
		{"windows line endings", strings.ReplaceAll(good("1", "alice.poweur.net", "a\nb"), "\n", "\r\n"), []string{"a\nb"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, e := range parseLog([]byte(tc.log)) {
				got = append(got, e.Message)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
				t.Fatalf("parsed %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderHTML(t *testing.T) {
	clean := func(in string) string {
		t.Helper()
		got, err := cleanMessage(in, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return renderHTML(got)
	}
	for _, tc := range []struct {
		name, in string
		has      []string
		lacks    []string
	}{
		{"emphasis", "**bold** and *italic* and `code`", []string{"<strong>bold</strong>", "<em>italic</em>", "<code>code</code>"}, nil},
		{"a line break is a break", "one\ntwo", []string{"one<br>", "two"}, nil},
		{"paragraphs", "one\n\ntwo", []string{"<p>one</p>", "<p>two</p>"}, nil},
		{"lists and quotes", "- a\n- b\n\n> quoted", []string{"<ul>", "<li>a</li>", "<blockquote>"}, nil},
		{"a link", "[poweur](https://poweur.net)", []string{`<a href="https://poweur.net">poweur</a>`}, nil},
		{"javascript links lose their target", "[x](javascript:alert(1))", nil, []string{"javascript:"}},
		{"data links lose their target", "[x](data:text/html;base64,AAAA)", nil, []string{"data:text"}},
		{"raw HTML is text", "<script>alert(1)</script><img src=x onerror=alert(1)>", []string{"&lt;script&gt;", "&lt;img"}, []string{"<script", "<img"}},
		{"images are text", "![x](https://evil.example/p.png)", nil, []string{"<img"}},
		{"headings are not headings", "# Title\ntext\n===", nil, []string{"<h1", "<h2"}},
		{"an autolink is not raw html", "<https://evil.example>", nil, []string{"<a "}},
		{"a comment is text", "<!-- hidden -->", []string{"&lt;!--"}, []string{"<!--"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clean(tc.in)
			for _, want := range tc.has {
				if !strings.Contains(got, want) {
					t.Errorf("%q rendered as %q, missing %q", tc.in, got, want)
				}
			}
			for _, bad := range tc.lacks {
				if strings.Contains(got, bad) {
					t.Errorf("%q rendered as %q, must not contain %q", tc.in, got, bad)
				}
			}
		})
	}
}

func TestSpacer(t *testing.T) {
	s := newSpacer(5 * time.Second)
	t0 := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)

	if _, wait := s.reserve("alice", t0); wait != 0 {
		t.Fatalf("first post waits %v", wait)
	}
	if _, wait := s.reserve("alice", t0.Add(2*time.Second)); wait != 3*time.Second {
		t.Fatalf("second post at +2s waits %v, want 3s", wait)
	}
	// A refusal claims nothing: the turn still comes at +5s, not +7s.
	if _, wait := s.reserve("alice", t0.Add(5*time.Second)); wait != 0 {
		t.Fatalf("post at +5s waits %v", wait)
	}
	if _, wait := s.reserve("bob", t0.Add(5*time.Second)); wait != 0 {
		t.Fatalf("another identity waits %v", wait)
	}

	prev, wait := s.reserve("carol", t0)
	if wait != 0 {
		t.Fatal("carol waits")
	}
	s.release("carol", prev)
	if _, wait := s.reserve("carol", t0.Add(time.Second)); wait != 0 {
		t.Fatalf("a released turn still costs a wait of %v", wait)
	}

	// Giving a turn back restores the previous one, not a free pass.
	prev, _ = s.reserve("dave", t0)
	prev2, wait := s.reserve("dave", t0.Add(6*time.Second))
	if wait != 0 || !prev2.Equal(t0) {
		t.Fatalf("wait %v prev %v", wait, prev2)
	}
	_ = prev
	s.release("dave", prev2)
	if _, wait := s.reserve("dave", t0.Add(7*time.Second)); wait != 0 {
		t.Fatalf("after releasing the +6s turn, a post at +7s waits %v", wait)
	}
}

func TestSpacerForgetsOldIdentities(t *testing.T) {
	s := newSpacer(time.Second)
	t0 := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5000; i++ {
		s.reserve(string(rune('a'+i%26))+strings.Repeat("x", i), t0)
	}
	s.reserve("late", t0.Add(time.Hour))
	if n := len(s.last); n > 4096 {
		t.Fatalf("%d identities remembered", n)
	}
}

func TestPageNames(t *testing.T) {
	if got := pageName(7); got != "entries-000007.md" {
		t.Fatal(got)
	}
	if n, ok := parsePageName("entries-000123.md"); !ok || n != 123 {
		t.Fatalf("%d %v", n, ok)
	}
	for _, bad := range []string{"", "entries-0.md", "entries-000000.md", "entries-1234567.md", "../entries-000001.md", "entries-000001.md.bak", "notes.md"} {
		if _, ok := parsePageName(bad); ok {
			t.Errorf("%q parsed as a page", bad)
		}
	}
}
