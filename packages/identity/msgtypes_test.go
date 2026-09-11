package identity

import (
	"strings"
	"testing"
)

func TestNormalizeMessageType(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", MsgTypeChatText},
		{"   ", MsgTypeChatText},
		{"chat.text", MsgTypeChatText},
		{"sys.contact.request", "sys.contact.request"},
		{"net.example.thing", "net.example.thing"},
	} {
		if got := NormalizeMessageType(tc.in); got != tc.want {
			t.Fatalf("NormalizeMessageType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateMessageType(t *testing.T) {
	valid := []string{
		"", // absent == chat.text
		"chat.text",
		"chat.attachment",
		"sys.contact.request",
		"net.poweur.tasks.assigned",
		"app.a-b.c9",
	}
	for _, v := range valid {
		if err := ValidateMessageType(v); err != nil {
			t.Fatalf("ValidateMessageType(%q): unexpected error %v", v, err)
		}
	}

	invalid := map[string]string{
		"bare":                    "must be namespaced",
		"chat.":                   "empty segment",
		".text":                   "empty segment",
		"chat..text":              "empty segment",
		"Chat.Text":               "invalid character",
		"chat.te xt":              "invalid character",
		"chat.te\nxt":             "invalid character",
		"chat.-text":              "invalid character", // leading hyphen in segment
		"chat.text-":              "invalid character", // trailing hyphen in segment
		strings.Repeat("a.b", 40): "too long",
	}
	for in, want := range invalid {
		err := ValidateMessageType(in)
		if err == nil {
			t.Fatalf("ValidateMessageType(%q): expected an error", in)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateMessageType(%q) = %v, want mention of %q", in, err, want)
		}
	}
}

func TestSystemTypeNamespace(t *testing.T) {
	if !IsSystemType("sys.anything.at.all") {
		t.Fatal("sys.* must be recognized as the reserved namespace")
	}
	if IsSystemType("chat.text") {
		t.Fatal("chat.text is not a system type")
	}
	if IsKnownSystemType("sys.anything.at.all") {
		t.Fatal("an unregistered sys.* type must not be known")
	}
	for _, known := range SystemMessageTypes() {
		if !IsKnownSystemType(known) {
			t.Fatalf("%q is in SystemMessageTypes but IsKnownSystemType says no", known)
		}
		if !IsSystemType(known) {
			t.Fatalf("%q must live in the sys.* namespace", known)
		}
	}
	// Sorted, so callers (and error messages) get a stable listing.
	got := SystemMessageTypes()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("SystemMessageTypes not sorted: %v", got)
		}
	}
}

func TestValidateThreadID(t *testing.T) {
	for _, ok := range []string{"", "thr_01j9", "a.b:c@d+e~f-g", strings.Repeat("t", MaxThreadIDLen)} {
		if err := ValidateThreadID(ok); err != nil {
			t.Fatalf("ValidateThreadID(%q): unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"has space", "new\nline", "tab\there", "slash/es", strings.Repeat("t", MaxThreadIDLen+1)} {
		if err := ValidateThreadID(bad); err == nil {
			t.Fatalf("ValidateThreadID(%q): expected an error", bad)
		}
	}
}

func TestValidateExpiresAt(t *testing.T) {
	if err := ValidateExpiresAt(""); err != nil {
		t.Fatalf("empty expires_at must be valid: %v", err)
	}
	if err := ValidateExpiresAt("2026-01-15T09:30:00Z"); err != nil {
		t.Fatalf("RFC3339 expires_at: %v", err)
	}
	for _, bad := range []string{"2026-01-15", "yesterday", "1768469400"} {
		if err := ValidateExpiresAt(bad); err == nil {
			t.Fatalf("ValidateExpiresAt(%q): expected an error", bad)
		}
	}
}

func TestValidateMetadata(t *testing.T) {
	if err := ValidateMetadata(nil); err != nil {
		t.Fatalf("nil metadata must be valid: %v", err)
	}
	if err := ValidateMetadata(map[string]string{"a": "", "mime.type": "image/png", "size-9": "1024"}); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	cases := map[string]struct {
		md   map[string]string
		want string
	}{
		"empty key":          {map[string]string{"": "x"}, "empty key"},
		"uppercase key":      {map[string]string{"Mime": "x"}, "invalid character"},
		"leading underscore": {map[string]string{"_mime": "x"}, "invalid character"},
		"long key":           {map[string]string{strings.Repeat("k", MaxMetadataKeyLen+1): "x"}, "too long"},
		"long value":         {map[string]string{"k": strings.Repeat("v", MaxMetadataValLen+1)}, "too long"},
		"newline value":      {map[string]string{"k": "a\nb"}, "control characters"},
		"tab value":          {map[string]string{"k": "a\tb"}, "control characters"},
		"del value":          {map[string]string{"k": "a\x7fb"}, "control characters"},
	}
	for name, tc := range cases {
		err := ValidateMetadata(tc.md)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want mention of %q", name, err, tc.want)
		}
	}

	tooMany := map[string]string{}
	for i := 0; i <= MaxMetadataKeys; i++ {
		tooMany[string(rune('a'+i))+"key"] = "v"
	}
	if err := ValidateMetadata(tooMany); err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("too many keys: got %v", err)
	}

	tooBig := map[string]string{}
	for i := 0; i < MaxMetadataKeys; i++ {
		tooBig[string(rune('a'+i))+"key"] = strings.Repeat("v", MaxMetadataValLen)
	}
	if err := ValidateMetadata(tooBig); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized metadata: got %v", err)
	}
}

func TestMetadataLinesSortedAndEscapeFree(t *testing.T) {
	if MetadataLines(nil) != nil {
		t.Fatal("nil metadata must produce no lines")
	}
	lines := MetadataLines(map[string]string{
		"zeta":    "last",
		"alpha":   "first",
		"m.i-d_9": "middle",
	})
	want := []string{"meta:alpha:first", "meta:m.i-d_9:middle", "meta:zeta:last"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	// Validation rules out control characters, which is what lets these
	// lines carry no escaping scheme at all.
	for _, line := range lines {
		if strings.ContainsAny(line, "\n\r") {
			t.Fatalf("metadata line %q must be a single line", line)
		}
	}
}

func TestValidateEnvelopeExtensionsCombines(t *testing.T) {
	if err := ValidateEnvelopeExtensions("", "", "", nil); err != nil {
		t.Fatalf("a bare envelope must validate: %v", err)
	}
	if err := ValidateEnvelopeExtensions("chat.text", "thr_1", "2026-01-15T09:30:00Z",
		map[string]string{"k": "v"}); err != nil {
		t.Fatalf("a fully populated envelope must validate: %v", err)
	}
	for name, args := range map[string][4]any{
		"type":     {"bare", "", "", map[string]string(nil)},
		"thread":   {"", "bad id", "", map[string]string(nil)},
		"expires":  {"", "", "nope", map[string]string(nil)},
		"metadata": {"", "", "", map[string]string{"K": "v"}},
	} {
		err := ValidateEnvelopeExtensions(
			args[0].(string), args[1].(string), args[2].(string), args[3].(map[string]string))
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}
