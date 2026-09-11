package identity

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	MsgTypeChatAttachment = "chat.attachment"
	MaxAttachmentBytes    = 20 * 1024 * 1024
)

// AttachmentRef is the signed, plaintext pointer carried in envelope
// metadata. File contents remain outside the 512 KB message spool.
type AttachmentRef struct {
	Owner   string
	Path    string
	Name    string
	Size    int64
	MIME    string
	SHA256  string
	ShareID string
}

func (a AttachmentRef) Metadata() map[string]string {
	return map[string]string{
		"attachment_owner": a.Owner, "attachment_path": a.Path,
		"attachment_name": a.Name, "attachment_size": strconv.FormatInt(a.Size, 10),
		"attachment_mime": a.MIME, "attachment_sha256": a.SHA256,
		"attachment_share_id": a.ShareID,
	}
}

func ParseAttachmentMetadata(md map[string]string) (AttachmentRef, error) {
	size, err := strconv.ParseInt(md["attachment_size"], 10, 64)
	if err != nil {
		return AttachmentRef{}, fmt.Errorf("attachment_size must be an integer")
	}
	a := AttachmentRef{Owner: md["attachment_owner"], Path: md["attachment_path"],
		Name: md["attachment_name"], Size: size, MIME: md["attachment_mime"],
		SHA256: strings.ToLower(md["attachment_sha256"]), ShareID: md["attachment_share_id"]}
	if err := a.Validate(); err != nil {
		return AttachmentRef{}, err
	}
	return a, nil
}

func (a AttachmentRef) Validate() error {
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(a.Owner))); err != nil {
		return fmt.Errorf("attachment_owner: %w", err)
	}
	path, err := NormalizeGrantPath(a.Path)
	if err != nil || path != a.Path || !strings.HasPrefix(path, "shared/.attachments/") {
		return fmt.Errorf("attachment_path must be under shared/.attachments")
	}
	if a.Name == "" || strings.ContainsAny(a.Name, "/\\\r\n") {
		return fmt.Errorf("attachment_name must be a file name")
	}
	if path[strings.LastIndex(path, "/")+1:] != a.Name {
		return fmt.Errorf("attachment_name must match attachment_path")
	}
	if a.Size < 0 || a.Size > MaxAttachmentBytes {
		return fmt.Errorf("attachment_size out of range (max %d)", MaxAttachmentBytes)
	}
	if a.MIME == "" || len(a.MIME) > 128 || strings.ContainsAny(a.MIME, "\r\n") {
		return fmt.Errorf("attachment_mime is invalid")
	}
	if len(a.SHA256) != 64 {
		return fmt.Errorf("attachment_sha256 must be 64 lowercase hex characters")
	}
	for _, c := range a.SHA256 {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return fmt.Errorf("attachment_sha256 must be 64 lowercase hex characters")
		}
	}
	if strings.TrimSpace(a.ShareID) == "" || strings.ContainsAny(a.ShareID, "/\\\r\n") {
		return fmt.Errorf("attachment_share_id is invalid")
	}
	return ValidateMetadata(a.Metadata())
}
