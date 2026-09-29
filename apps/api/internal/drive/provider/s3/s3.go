// Package s3 implements the storage-v2 provider for S3-compatible stores with
// conditional PUT and SHA-256 checksum support. It never emulates conditional
// writes with a read/write pair: a backend lacking them is not safe to use.
package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
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
	// bare is set when the bucket rejects a quoted If-Match and accepts the
	// bare MD5. AWS and MinIO require the quoted form. Hetzner Object Storage
	// (Ceph) rejects it.
	bare atomic.Bool
	// http sends the requests minio-go cannot shape (bare If-Match, the
	// presign probe).
	http *http.Client
}

func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("S3 endpoint and bucket required")
	}
	endpoint, secure, err := normalizeEndpoint(cfg.Endpoint, cfg.Secure)
	if err != nil {
		return nil, err
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
	client, err := minio.New(endpoint, &minio.Options{Creds: creds, Secure: secure, Region: cfg.Region})
	if err != nil {
		return nil, err
	}
	return &Store{client: client, bucket: cfg.Bucket, prefix: prefix, presign: cfg.Presign,
		http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// A scheme in the endpoint wins over Config.Secure so http://127.0.0.1:9000
// reaches a local MinIO and https:// is not accidentally sent in cleartext.
func normalizeEndpoint(endpoint string, secure bool) (string, bool, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, "://") {
		if endpoint == "" || strings.Contains(endpoint, "/") {
			return "", false, errors.New("invalid S3 endpoint")
		}
		return endpoint, secure, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false, errors.New("invalid S3 endpoint")
	}
	return u.Host, u.Scheme == "https", nil
}

// Probe checks conditional create and an ETag replace. A bucket that
// overwrites instead is refused. Ceph-based stores that reject a quoted
// If-Match are retried with the bare ETag and remembered for later writes.
func (s *Store) Probe(ctx context.Context) error {
	return s.probe(ctx, ProbeStepTimeout)
}

// ProbeStepTimeout bounds each probe request: a store slower than this is
// treated as unavailable.
const ProbeStepTimeout = 10 * time.Second

func (s *Store) probe(parent context.Context, step time.Duration) error {
	// Each request gets its own deadline; ctx follows the current step.
	ctx, cancel := context.WithTimeout(parent, step)
	next := func() {
		cancel()
		ctx, cancel = context.WithTimeout(parent, step)
	}
	defer func() { cancel() }()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	key := "relay/startup-probe/" + hex.EncodeToString(raw)
	tag, err := s.PutIf(ctx, key, []byte("probe"), "")
	if err != nil {
		return fmt.Errorf("S3 conditional create: %w", err)
	}
	defer func() { _ = s.Delete(context.WithoutCancel(parent), key) }()
	next()
	if _, err := s.PutIf(ctx, key, []byte("conflict"), ""); !errors.Is(err, provider.ErrPrecondition) {
		return fmt.Errorf("S3 conditional create is not enforced (%v)", err)
	}
	next()
	if _, err := s.PutIf(ctx, key, []byte("replaced"), tag); err != nil {
		if !errors.Is(err, provider.ErrPrecondition) {
			return fmt.Errorf("S3 conditional replace: %w", err)
		}
		s.bare.Store(true)
		next()
		if _, err := s.PutIf(ctx, key, []byte("replaced"), tag); err != nil {
			return fmt.Errorf("S3 conditional replace by ETag is not supported (%v)", err)
		}
	}
	next()
	got, err := s.Get(ctx, key, nil)
	if err != nil || string(got.Data) != "replaced" {
		return fmt.Errorf("S3 conditional replace did not store the new bytes (%v)", err)
	}
	if s.presign {
		next()
		if err := s.probePresignChecksum(ctx, key+"-presign"); err != nil {
			return err
		}
	}
	return nil
}

// probePresignChecksum uploads the wrong bytes to a URL presigned for a
// SHA-256. A store that stores them anyway does not verify client uploads,
// so presigned uploads would let a client publish bytes the relay never
// checked: refuse to start rather than run that way.
func (s *Store) probePresignChecksum(ctx context.Context, key string) error {
	want := sha256.Sum256([]byte("expected"))
	signed, err := s.PresignPut(ctx, key, hex.EncodeToString(want[:]), int64(len("expected")), time.Minute)
	if err != nil {
		return fmt.Errorf("S3 presign probe: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.URL, strings.NewReader("tampered"))
	if err != nil {
		return err
	}
	for k, v := range signed.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("S3 presign probe: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		_ = s.Delete(context.WithoutCancel(ctx), key)
		return errors.New("S3 presigned uploads do not verify x-amz-checksum-sha256 on this store; set S3_PRESIGN=0 so chunk bytes go through the relay")
	}
	return nil
}

// UseBareETag reports whether Put If-Match values are sent without quotes.
func (s *Store) UseBareETag() bool { return s.bare.Load() }

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
	return provider.Object{Data: raw, ETag: normalizeETag(stat.ETag), Size: stat.Size}, nil
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
	// No SDK checksum header here. Presigned client uploads carry
	// x-amz-checksum-sha256. A checksum on this PUT makes some Ceph
	// deployments reject a matching If-Match.
	if match != nil && *match != "" && s.bare.Load() {
		return s.putBareMatch(ctx, full, data, *match)
	}
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	if match != nil {
		if *match == "" {
			opts.SetMatchETagExcept("*")
		} else {
			opts.SetMatchETag(*match)
		}
	}
	result, err := s.client.PutObject(ctx, s.bucket, full, bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		err = convertError(err)
		// If-Match against a missing key is 404 on S3. Callers treat that as
		// a failed precondition, the same as the filesystem store.
		if match != nil && *match != "" && errors.Is(err, provider.ErrNotFound) {
			err = provider.ErrPrecondition
		}
		if errors.Is(err, provider.ErrPrecondition) {
			return s.resolvePrecondition(ctx, key, data)
		}
		return "", err
	}
	return normalizeETag(result.ETag), nil
}

// resolvePrecondition decides a conditional write that came back as a failed
// precondition. The SDK retries 5xx answers, and Ceph-based stores answer 500
// to some racing conditional writes, so a 412 can be the reply to a *retry*
// of a write that already succeeded. If the object now holds exactly these
// bytes the write is in place and reported as success; otherwise it lost.
// Identical bytes from a different writer mean the same outcome, which is
// what a content-addressed journal needs.
func (s *Store) resolvePrecondition(ctx context.Context, key string, data []byte) (string, error) {
	current, err := s.Get(ctx, key, nil)
	if err == nil && bytes.Equal(current.Data, data) {
		return current.ETag, nil
	}
	return "", provider.ErrPrecondition
}

// putBareMatch signs If-Match as the bare ETag. minio-go's PutObject always
// quotes that header, and Hetzner rejects the quoted form.
func (s *Store) putBareMatch(ctx context.Context, full string, data []byte, match string) (string, error) {
	match = strings.Trim(match, `"`)
	headers := http.Header{"If-Match": []string{match}}
	signed, err := s.client.PresignHeader(ctx, http.MethodPut, s.bucket, full, time.Minute, nil, headers)
	if err != nil {
		return "", err
	}
	send := func() (int, string, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.String(), bytes.NewReader(data))
		if err != nil {
			return 0, "", nil, err
		}
		req.Header.Set("If-Match", match)
		req.ContentLength = int64(len(data))
		resp, err := s.http.Do(req)
		if err != nil {
			return 0, "", nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, resp.Header.Get("ETag"), raw, nil
	}
	status, etag, raw, err := send()
	if err != nil {
		return "", err
	}
	// Ceph on Hetzner sometimes answers 500 to a racing If-Match. The write
	// may or may not have landed; one retry settles it, and a 412 on that
	// retry is checked against the stored bytes rather than trusted.
	if status == http.StatusInternalServerError {
		if status, etag, raw, err = send(); err != nil {
			return "", err
		}
	}
	key := strings.TrimPrefix(full, s.prefix)
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return normalizeETag(etag), nil
	case http.StatusPreconditionFailed, http.StatusNotFound:
		return s.resolvePrecondition(ctx, key, data)
	default:
		return "", fmt.Errorf("S3 conditional replace: HTTP %d %s", status, bytes.TrimSpace(raw))
	}
}

// S3 ETags are quoted. minio-go wraps the value we pass to If-Match in
// another pair of quotes, so a quoted tag would never match.
func normalizeETag(tag string) string {
	return strings.Trim(tag, `"`)
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
