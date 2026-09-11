package identity

import "testing"

func TestAttachmentMetadataRoundTrip(t *testing.T) {
	want := AttachmentRef{Owner: "alice.example.org", Path: "shared/.attachments/att_1/photo.jpg",
		Name: "photo.jpg", Size: 123, MIME: "image/jpeg",
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ShareID: "shr_1"}
	got, err := ParseAttachmentMetadata(want.Metadata())
	if err != nil || got != want {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestAttachmentValidationRejectsOversizeAndBadPath(t *testing.T) {
	a := AttachmentRef{Owner: "alice.example.org", Path: "private/photo.jpg", Name: "photo.jpg",
		Size: MaxAttachmentBytes + 1, MIME: "image/jpeg",
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ShareID: "shr_1"}
	if err := a.Validate(); err == nil {
		t.Fatal("invalid attachment accepted")
	}
}

func TestAttachmentValidationRejectsNonCanonicalAndMismatchedPaths(t *testing.T) {
	base := AttachmentRef{Owner: "alice.example.org", Path: "shared/.attachments/att_1/photo.jpg",
		Name: "photo.jpg", Size: 1, MIME: "image/jpeg",
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ShareID: "shr_1"}
	for _, path := range []string{"/shared/.attachments/att_1/photo.jpg", "shared/.attachments/../photo.jpg", "shared/.attachments/att_1/other.jpg"} {
		got := base
		got.Path = path
		if err := got.Validate(); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}
