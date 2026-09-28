package s3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/drive/provider/providertest"
	stores3 "github.com/poweur/api/internal/drive/provider/s3"
)

func testConfig(t *testing.T) stores3.Config {
	t.Helper()
	endpoint := os.Getenv("POWEUR_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set POWEUR_TEST_S3_ENDPOINT to run MinIO conformance")
	}
	cfg := stores3.Config{Endpoint: endpoint, AccessKey: os.Getenv("POWEUR_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("POWEUR_TEST_S3_SECRET_KEY"), Region: "us-east-1", Secure: os.Getenv("POWEUR_TEST_S3_SECURE") == "1", Presign: true}
	client, err := minio.New(cfg.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.Secure, Region: cfg.Region})
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	cfg.Bucket = "poweur-test-" + hex.EncodeToString(random)
	if err := client.MakeBucket(t.Context(), cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for item := range client.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{Recursive: true}) {
			if item.Err != nil {
				t.Error(item.Err)
				return
			}
			if err := client.RemoveObject(ctx, cfg.Bucket, item.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Error(err)
			}
		}
		if err := client.RemoveBucket(ctx, cfg.Bucket); err != nil {
			t.Error(err)
		}
	})
	return cfg
}

func TestConformance(t *testing.T) {
	cfg := testConfig(t)
	providertest.Run(t, func(t *testing.T) provider.Store {
		copy := cfg
		copy.Prefix = strings.ReplaceAll(t.Name(), "/", "-")
		s, err := stores3.New(copy)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestPresignedChecksumAndPrefix(t *testing.T) {
	cfg := testConfig(t)
	cfg.Prefix = "isolated/prefix"
	s, err := stores3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("ciphertext bytes")
	sum := sha256.Sum256(data)
	signed, err := s.PresignPut(t.Context(), "chunks/object", hex.EncodeToString(sum[:]), int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "x-amz-checksum-sha256") {
		t.Fatal("checksum is not signed")
	}
	upload := func(body []byte) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, signed.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range signed.Headers {
			req.Header.Set(key, value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := upload([]byte("wrong ciphertext")); code < 400 {
		t.Fatalf("wrong checksum accepted: %d", code)
	}
	if _, err := s.Get(t.Context(), "chunks/object", nil); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("failed upload published bytes: %v", err)
	}
	if code := upload(data); code != http.StatusOK {
		t.Fatalf("valid upload: %d", code)
	}
	got, err := s.Get(t.Context(), "chunks/object", nil)
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("upload mismatch: %+v %v", got, err)
	}
	download, err := s.PresignGet(t.Context(), "chunks/object", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(download.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(raw, data) {
		t.Fatalf("download %d %v", resp.StatusCode, err)
	}
	other := cfg
	other.Prefix = "different/prefix"
	outside, err := stores3.New(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outside.Get(t.Context(), "chunks/object", nil); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("prefix leak: %v", err)
	}
}

func TestConfigAndPresignValidation(t *testing.T) {
	for _, cfg := range []stores3.Config{{}, {Endpoint: "localhost:9000"}, {Endpoint: "localhost:9000", Bucket: "bucket", Prefix: "../escape"}, {Endpoint: "localhost:9000", Bucket: "bucket", AccessKey: "partial"}} {
		if _, err := stores3.New(cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	cfg := stores3.Config{Endpoint: "localhost:9000", Bucket: "test-bucket", Region: "us-east-1", AccessKey: "test", SecretKey: "test-secret"}
	s, err := stores3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PresignGet(t.Context(), "key", time.Minute); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatal(err)
	}
	cfg.Presign = true
	s, err = stores3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"", strings.Repeat("g", 64), strings.Repeat("A", 64)} {
		if _, err := s.PresignPut(t.Context(), "key", hash, 1, time.Minute); err == nil {
			t.Fatal("invalid checksum accepted")
		}
	}
	if _, err := s.PresignPut(t.Context(), "key", strings.Repeat("a", 64), -1, time.Minute); err == nil {
		t.Fatal("negative size accepted")
	}
	if _, err := s.PresignGet(t.Context(), "../escape", time.Minute); !errors.Is(err, provider.ErrInvalidKey) {
		t.Fatal(err)
	}
}
