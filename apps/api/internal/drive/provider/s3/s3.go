// Package s3 implements the storage-v2 provider for S3-compatible stores with
// conditional PUT and SHA-256 checksum support. It never emulates conditional
// writes with a read/write pair: a backend lacking them is not safe to use.
package s3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/poweur/api/internal/drive/provider"
)

type Config struct {
	Endpoint     string
	Bucket       string
	Prefix       string
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Secure       bool
	Presign      bool
}

type Store struct {
	client         *minio.Client
	bucket, prefix string
	presign        bool
}

func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" {
		return nil, errors.New("S3 endpoint and bucket required")
	}
	prefix := strings.TrimSuffix(cfg.Prefix, "/")
	if prefix != "" {
		if err := provider.ValidateKey(prefix); err != nil {
			return nil, err
		}
		prefix += "/"
	}
	if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
		return nil, errors.New("S3 access key and secret key must be configured together")
	}
	var creds *credentials.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken)
	} else {
		creds = credentials.NewEnvAWS()
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: cfg.Secure, Region: cfg.Region})
	if err != nil {
		return nil, err
	}
	return &Store{client: client, bucket: cfg.Bucket, prefix: prefix, presign: cfg.Presign}, nil
}

func convertError(err error) error {
	if err == nil {
		return nil
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NotFound":
		return provider.ErrNotFound
	case "PreconditionFailed", "ConditionalRequestConflict":
		return provider.ErrPrecondition
	case "InvalidRange", "RequestedRangeNotSatisfiable":
		return provider.ErrRange
	}
	return err
}
func (s *Store) key(key string) (string, error) {
	if err := provider.ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func (s *Store) Get(ctx context.Context, key string, r *provider.Range) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	full, err := s.key(key)
	if err != nil {
		return provider.Object{}, err
	}
	stat, err := s.client.StatObject(ctx, s.bucket, full, minio.StatObjectOptions{})
	if err != nil {
		return provider.Object{}, convertError(err)
	}
	if err := provider.CheckRange(r, stat.Size); err != nil {
		return provider.Object{}, err
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetMatchETag(stat.ETag); err != nil {
		return provider.Object{}, err
	}
	if r != nil {
		if err := opts.SetRange(r.Offset, r.Offset+r.Length-1); err != nil {
			return provider.Object{}, err
		}
	}
	object, err := s.client.GetObject(ctx, s.bucket, full, opts)
	if err != nil {
		return provider.Object{}, convertError(err)
	}
	defer object.Close()
	raw, err := io.ReadAll(object)
	if err != nil {
		return provider.Object{}, convertError(err)
	}
	want := stat.Size
	if r != nil {
		want = r.Length
	}
	if int64(len(raw)) != want {
		return provider.Object{}, io.ErrUnexpectedEOF
	}
	return provider.Object{Data: raw, ETag: stat.ETag, Size: stat.Size}, nil
}
func (s *Store) Put(ctx context.Context, key string, data []byte) (string, error) {
	return s.put(ctx, key, data, nil)
}
func (s *Store) PutIf(ctx context.Context, key string, data []byte, match string) (string, error) {
	return s.put(ctx, key, data, &match)
}
func (s *Store) put(ctx context.Context, key string, data []byte, match *string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	full, err := s.key(key)
	if err != nil {
		return "", err
	}
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true, AutoChecksum: minio.ChecksumSHA256}
	if match != nil {
		if *match == "" {
			opts.SetMatchETagExcept("*")
		} else {
			opts.SetMatchETag(*match)
		}
	}
	result, err := s.client.PutObject(ctx, s.bucket, full, bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		return "", convertError(err)
	}
	return result.ETag, nil
}
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := s.key(key)
	if err != nil {
		return err
	}
	if _, err := s.client.StatObject(ctx, s.bucket, full, minio.StatObjectOptions{}); err != nil {
		return convertError(err)
	}
	return convertError(s.client.RemoveObject(ctx, s.bucket, full, minio.RemoveObjectOptions{}))
}
func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (provider.Page, error) {
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if err := provider.ValidateList(prefix, cursor, limit); err != nil {
		return provider.Page{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	after := ""
	if cursor != "" {
		after = s.prefix + cursor
	}
	page := provider.Page{Objects: []provider.Info{}}
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + prefix, StartAfter: after, Recursive: true, MaxKeys: limit + 1}) {
		if object.Err != nil {
			return provider.Page{}, convertError(object.Err)
		}
		key, ok := strings.CutPrefix(object.Key, s.prefix)
		if !ok {
			return provider.Page{}, errors.New("S3 listing escaped configured prefix")
		}
		if err := provider.ValidateKey(key); err != nil {
			return provider.Page{}, err
		}
		if key <= cursor {
			return provider.Page{}, errors.New("S3 listing cursor did not advance")
		}
		if len(page.Objects) == limit {
			page.Next = page.Objects[limit-1].Key
			break
		}
		page.Objects = append(page.Objects, provider.Info{Key: key, Size: object.Size, Modified: object.LastModified.UTC()})
	}
	return page, nil
}

func (s *Store) PresignGet(ctx context.Context, key string, expires time.Duration) (provider.SignedURL, error) {
	full, err := s.key(key)
	if err != nil {
		return provider.SignedURL{}, err
	}
	if !s.presign {
		return provider.SignedURL{}, provider.ErrUnsupported
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, full, expires, nil)
	if err != nil {
		return provider.SignedURL{}, err
	}
	return provider.SignedURL{URL: u.String()}, nil
}
func (s *Store) PresignPut(ctx context.Context, key, sha256hex string, size int64, expires time.Duration) (provider.SignedURL, error) {
	full, err := s.key(key)
	if err != nil {
		return provider.SignedURL{}, err
	}
	if !s.presign {
		return provider.SignedURL{}, provider.ErrUnsupported
	}
	hash, err := hex.DecodeString(sha256hex)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != sha256hex || size < 0 {
		return provider.SignedURL{}, errors.New("invalid upload hash or size")
	}
	headers := http.Header{"X-Amz-Checksum-Sha256": []string{base64.StdEncoding.EncodeToString(hash)}}
	u, err := s.client.PresignHeader(ctx, http.MethodPut, s.bucket, full, expires, url.Values{}, headers)
	if err != nil {
		return provider.SignedURL{}, err
	}
	// The checksum binds the exact bytes (and therefore size). Browsers set
	// Content-Length themselves. Commit still verifies the stored size/hash.
	return provider.SignedURL{URL: u.String(), Headers: map[string]string{"X-Amz-Checksum-Sha256": headers.Get("X-Amz-Checksum-Sha256")}}, nil
}

var _ provider.Store = (*Store)(nil)
