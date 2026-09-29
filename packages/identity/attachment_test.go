package identity

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestAttachmentPayloadKeepsTheNameOutOfMetadata(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	a := Attachment{Format: AttachmentFormat, Name: "secret-name.bin", MIME: "application/octet-stream", ContentKey: key, Caption: "see attached"}
	raw, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAttachment(raw)
	if err != nil || parsed.Name != a.Name || parsed.Caption != a.Caption {
		t.Fatalf("%+v %v", parsed, err)
	}
	meta := AttachmentMetadata(strings.Repeat("ab", 16), 40, strings.Repeat("cd", 32))
	for _, hidden := range []string{"secret-name.bin", "octet-stream", key, "see attached"} {
		for _, value := range meta {
			if strings.Contains(value, hidden) {
				t.Fatalf("metadata leaked %s", hidden)
			}
		}
	}
	if _, err := ParseAttachment([]byte(`{"format":1,"name":"../x","mime":"text/plain","content_key":"` + key + `"}`)); err == nil {
		t.Fatal("accepted a path name")
	}
}
