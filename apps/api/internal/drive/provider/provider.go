// Package provider defines the object-store boundary used by storage v2. Keys
// are provider-relative, never filesystem paths supplied by a client.
package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("object not found")
	ErrPrecondition = errors.New("object precondition failed")
	ErrInvalidKey   = errors.New("invalid object key")
	ErrRange        = errors.New("invalid object range")
	ErrUnsupported  = errors.New("provider operation unsupported")
)

// Range selects exactly Length bytes starting at Offset (both in stored bytes).
// A nil range selects the whole object. Length must be positive.
type Range struct{ Offset, Length int64 }
type Object struct {
	Data []byte
	ETag string
	Size int64
}
type Info struct {
	Key      string
	Size     int64
	Modified time.Time
}
type Page struct {
	Objects []Info
	Next    string
}
type SignedURL struct {
	URL     string
	Headers map[string]string
}

// Store provides strongly consistent single-object operations. PutIf with an
// empty match creates only if absent; otherwise match must equal the current
// opaque ETag. A caller must not interpret an ETag as a content hash. Lists
// are lexicographic, bounded, and exclusive of the preceding page's cursor.
// Cross-object transactions and crash recovery belong to the drive journal.
type Store interface {
	Get(context.Context, string, *Range) (Object, error)
	Put(context.Context, string, []byte) (string, error)
	PutIf(context.Context, string, []byte, string) (string, error)
	Delete(context.Context, string) error
	List(context.Context, string, string, int) (Page, error)
	PresignGet(context.Context, string, time.Duration) (SignedURL, error)
	PresignPut(context.Context, string, string, int64, time.Duration) (SignedURL, error)
}

func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > 1024 || strings.ContainsAny(key, "\\\x00") {
		return ErrInvalidKey
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".tmp-") {
			return ErrInvalidKey
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				return ErrInvalidKey
			}
		}
	}
	return nil
}

func ValidateList(prefix, cursor string, limit int) error {
	if prefix != "" {
		if err := ValidateKey(strings.TrimSuffix(prefix, "/")); err != nil {
			return err
		}
	}
	if cursor != "" {
		if err := ValidateKey(cursor); err != nil {
			return err
		}
		if !strings.HasPrefix(cursor, prefix) {
			return ErrInvalidKey
		}
	}
	if limit < 1 || limit > 1000 {
		return errors.New("list limit must be 1..1000")
	}
	return nil
}

func CheckRange(r *Range, size int64) error {
	if r == nil {
		return nil
	}
	if r.Offset < 0 || r.Length <= 0 || r.Offset > size || r.Length > size-r.Offset {
		return ErrRange
	}
	return nil
}
