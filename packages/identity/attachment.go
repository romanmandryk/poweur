package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Attachments (EPIC-020 E20-T11). The file lives on the sender's drive and is
// shared read-only with the recipient. The message payload, which is
// encrypted, carries the content key, filename and MIME type. The envelope
// metadata is plaintext and keeps only the node id, ciphertext size and hash.

const AttachmentFormat = 1

const (
	AttachmentMetaNode = "node"
	AttachmentMetaSize = "size"
	AttachmentMetaHash = "hash"
)

// MaxAttachmentBytes is the largest file a client will seal into one message.
const MaxAttachmentBytes = 20 << 20

// Attachment is the decrypted chat.attachment payload.
type Attachment struct {
	Format     int    `json:"format"`
	Name       string `json:"name"`
	MIME       string `json:"mime"`
	ContentKey string `json:"content_key"`
	Caption    string `json:"caption,omitempty"`
}

// Validate checks an attachment payload before it is encrypted to the recipient.
func (a Attachment) Validate() error {
	if a.Format != AttachmentFormat {
		return fmt.Errorf("unsupported attachment format %d", a.Format)
	}
	if a.Name == "" || len(a.Name) > 255 || strings.ContainsAny(a.Name, "/\\\x00") || a.Name == "." || a.Name == ".." {
		return fmt.Errorf("invalid attachment name")
	}
	if a.MIME == "" || len(a.MIME) > 127 || strings.ContainsAny(a.MIME, "\r\n") {
		return fmt.Errorf("invalid attachment type")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(a.ContentKey)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != a.ContentKey {
		return fmt.Errorf("invalid attachment content key")
	}
	return nil
}

// Marshal encodes a valid attachment payload.
func (a Attachment) Marshal() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

// ParseAttachment decodes a decrypted chat.attachment payload.
func ParseAttachment(raw []byte) (Attachment, error) {
	var a Attachment
	if err := json.Unmarshal(raw, &a); err != nil {
		return Attachment{}, fmt.Errorf("invalid attachment: %w", err)
	}
	if err := a.Validate(); err != nil {
		return Attachment{}, err
	}
	return a, nil
}

// AttachmentMetadata is the plaintext envelope map: node id, ciphertext size
// and ciphertext hash. It must not carry the name, type or key.
func AttachmentMetadata(node string, size uint64, hash string) map[string]string {
	return map[string]string{
		AttachmentMetaNode: node,
		AttachmentMetaSize: fmt.Sprintf("%d", size),
		AttachmentMetaHash: hash,
	}
}

// ContentKey decodes the payload's content key.
func (a Attachment) ContentKeyBytes() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return base64.RawURLEncoding.DecodeString(a.ContentKey)
}
