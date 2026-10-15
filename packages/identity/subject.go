package identity

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Optional subject lines (EPIC-009 E09-T13, PCP-0009).
//
// A subject is content, so it travels inside the end-to-end encrypted
// payload, never in `thread_id` or `metadata` (both plaintext to every relay
// on the path). A message with a subject has the plaintext
//
//	Subject: <subject>
//
//	<text>
//
// and the signed envelope metadata `body_format: headers-v1`. Without that
// flag the plaintext is plain text exactly as before, so a body that happens
// to start with "Subject:" is never misread, and a message without a subject
// is byte-identical to what every earlier client sends. An old client ignores
// the metadata and shows the header line above the text — readable, not JSON.
//
// The email bridge (EPIC-023) maps `Subject:` to the same field, which is why
// the encoding is an email-style header block.

const (
	// MetaBodyFormat is the envelope metadata key that flags a structured body.
	MetaBodyFormat = "body_format"
	// BodyFormatHeadersV1 marks a plaintext that starts with a header block.
	BodyFormatHeadersV1 = "headers-v1"
	// MaxSubjectBytes caps a subject (UTF-8, after trimming).
	MaxSubjectBytes = 256
	// SubjectKeyPrefix starts the grouping key of a message that has a
	// subject but no thread (ConversationKey).
	SubjectKeyPrefix = "subj:"
)

// ValidateSubject checks a subject after trimming: valid UTF-8, no control or
// line-separator characters, at most MaxSubjectBytes. The empty subject is
// valid and means "no subject".
func ValidateSubject(subject string) error {
	subject = strings.TrimSpace(subject)
	if !utf8.ValidString(subject) {
		return fmt.Errorf("subject is not valid UTF-8")
	}
	if len(subject) > MaxSubjectBytes {
		return fmt.Errorf("subject too long (max %d bytes)", MaxSubjectBytes)
	}
	for _, r := range subject {
		if subjectControl(r) {
			return fmt.Errorf("subject must not contain control characters")
		}
	}
	return nil
}

// subjectControl is every rune a one-line header value must not hold: C0 and
// C1 controls, DEL and the Unicode line and paragraph separators.
func subjectControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029
}

// FormatBody encodes a subject and text as one plaintext. With an empty
// subject it returns text unchanged and no metadata, so the message is exactly
// what a client without subjects would send; otherwise the plaintext carries
// the header block and metadata the flag to merge into the envelope's.
func FormatBody(subject, text string) (plaintext string, metadata map[string]string, err error) {
	subject = strings.TrimSpace(subject)
	if err := ValidateSubject(subject); err != nil {
		return "", nil, err
	}
	if subject == "" {
		return text, nil, nil
	}
	return "Subject: " + subject + "\n\n" + text, map[string]string{MetaBodyFormat: BodyFormatHeadersV1}, nil
}

// ParseBody splits a received plaintext into subject and text. Only a
// plaintext flagged `headers-v1` is parsed; anything else is all text. A
// flagged plaintext whose header block does not parse comes back whole as the
// text with no subject: content is never dropped.
//
// The block is `Name: value` lines up to the first blank line. v1 defines only
// `Subject` (case-insensitive); other names are ignored.
func ParseBody(plaintext string, metadata map[string]string) (subject, text string) {
	if metadata[MetaBodyFormat] != BodyFormatHeadersV1 {
		return "", plaintext
	}
	end := strings.Index(plaintext, "\n\n")
	if end < 0 {
		return "", plaintext
	}
	found := ""
	for _, line := range strings.Split(plaintext[:end], "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !headerName(name) {
			return "", plaintext
		}
		if strings.EqualFold(name, "subject") {
			found = strings.TrimSpace(value)
		}
	}
	if ValidateSubject(found) != nil {
		return "", plaintext
	}
	return found, plaintext[end+2:]
}

func headerName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// replyPrefixes are the reply and forward markers NormalizeSubject strips:
// English, German (Aw), Scandinavian (Sv) and the common forwards.
var replyPrefixes = []string{"re", "fwd", "fw", "aw", "sv"}

// NormalizeSubject is the form two subjects are compared in: trimmed,
// internal whitespace collapsed to one space, lowercased rune by rune, and any
// run of leading `Re:` / `Fwd:` / `Fw:` / `Aw:` / `Sv:` markers removed
// (including counted forms such as `Re[2]:`).
//
// Clients never add `Re:` themselves — the thread carries the link — but mail
// and other implementations do, and a reply must still land beside the
// message it answers.
func NormalizeSubject(subject string) string {
	fields := strings.Fields(subject)
	s := strings.Join(fields, " ")
	s = strings.Map(unicode.ToLower, s)
	for {
		stripped := stripReplyPrefix(s)
		if stripped == s {
			return s
		}
		s = stripped
	}
}

func stripReplyPrefix(s string) string {
	for _, p := range replyPrefixes {
		if !strings.HasPrefix(s, p) {
			continue
		}
		rest := s[len(p):]
		if strings.HasPrefix(rest, "[") {
			close := strings.Index(rest, "]")
			if close < 2 || strings.Trim(rest[1:close], "0123456789") != "" {
				continue
			}
			rest = rest[close+1:]
		}
		rest = strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		return strings.TrimLeft(rest[1:], " ")
	}
	return s
}

// ConversationKey is the key a message is grouped under inside one
// conversation (one peer or group): its thread when it has one, otherwise its
// normalized subject, otherwise the default thread "". A subject with nothing
// left after normalizing ("Re:") is no subject.
func ConversationKey(threadID, subject string) string {
	if threadID != "" {
		return threadID
	}
	if n := NormalizeSubject(subject); n != "" {
		return SubjectKeyPrefix + n
	}
	return ""
}

// crockford is the ULID alphabet.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewThreadID mints a fresh thread identifier for a new subject: a ULID, so
// threads sort by when they were started. Never derive one from the subject
// text — a hash of it in a plaintext field could be guessed by dictionary.
func NewThreadID() string {
	return newULID(time.Now())
}

func newULID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	// 128 bits as 26 base-32 digits, most significant first.
	out := make([]byte, 26)
	hi := binary.BigEndian.Uint64(b[0:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}
