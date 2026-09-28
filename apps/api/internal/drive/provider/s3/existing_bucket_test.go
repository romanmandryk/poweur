package s3_test

import (
	"bytes"
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

	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/drive/provider/providertest"
	stores3 "github.com/poweur/api/internal/drive/provider/s3"
)

// TestExistingBucket runs the provider against an operator's real bucket
// (Hetzner, R2, AWS, …) the way the relay would use it. It does not create or
// delete the bucket, and it leaves drive-verify/kept/ in place so the objects
// can be inspected.
//
//	POWEUR_TEST_S3_ENDPOINT, POWEUR_TEST_S3_BUCKET, POWEUR_TEST_S3_ACCESS_KEY,
//	POWEUR_TEST_S3_SECRET_KEY, POWEUR_TEST_S3_REGION (default hel1),
//	POWEUR_TEST_S3_SECURE=0 for plain http, POWEUR_TEST_S3_PRESIGN=0 for a
//	store that does not verify presigned checksums (Hetzner).
func TestExistingBucket(t *testing.T) {
	endpoint := os.Getenv("POWEUR_TEST_S3_ENDPOINT")
	bucket := os.Getenv("POWEUR_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("set POWEUR_TEST_S3_ENDPOINT and POWEUR_TEST_S3_BUCKET")
	}
	region := os.Getenv("POWEUR_TEST_S3_REGION")
	if region == "" {
		region = "hel1"
	}
	cfg := stores3.Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		Region:    region,
		AccessKey: os.Getenv("POWEUR_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("POWEUR_TEST_S3_SECRET_KEY"),
		Secure:    os.Getenv("POWEUR_TEST_S3_SECURE") != "0",
		Presign:   os.Getenv("POWEUR_TEST_S3_PRESIGN") != "0",
		Prefix:    "drive-verify",
	}
	kept, err := stores3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := kept.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	for {
		page, err := kept.List(t.Context(), "", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Objects) == 0 {
			break
		}
		for _, obj := range page.Objects {
			if err := kept.Delete(t.Context(), obj.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	hello := []byte("Poweur drive provider verification.\nBucket " + bucket + " at " + endpoint + ".\nPutObject, conditional replace, and listing wrote the objects under drive-verify/.\n")
	if _, err := kept.Put(t.Context(), "kept/hello.txt", hello); err != nil {
		t.Fatal(err)
	}
	providertest.Run(t, func(t *testing.T) provider.Store {
		copy := cfg
		copy.Prefix = "drive-verify/" + strings.ReplaceAll(t.Name(), "/", "-")
		s, err := stores3.New(copy)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Probe(t.Context()); err != nil {
			t.Fatal(err)
		}
		return s
	})

	data := []byte("ciphertext bytes kept in the bucket")
	if _, err := kept.Put(t.Context(), "kept/sample.bin", data); err != nil {
		t.Fatal(err)
	}
	if !cfg.Presign {
		// Probe already proved this store does not need presigned uploads
		// verified; the relay proxies chunk bytes instead.
		if _, err := kept.PresignPut(t.Context(), "kept/presigned.bin", strings.Repeat("0", 64), 1, time.Minute); !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("presign disabled but PresignPut returned %v", err)
		}
		return
	}
	sum := sha256.Sum256(data)
	signed, err := kept.PresignPut(t.Context(), "kept/presigned.bin", hex.EncodeToString(sum[:]), int64(len(data)), time.Minute)
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
		t.Errorf("presigned PUT with the wrong SHA-256 was stored: HTTP %d", code)
		if err := kept.Delete(t.Context(), "kept/presigned.bin"); err != nil && !errors.Is(err, provider.ErrNotFound) {
			t.Error(err)
		}
	} else if _, err := kept.Get(t.Context(), "kept/presigned.bin", nil); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("failed upload published bytes: %v", err)
	} else if code := upload(data); code != http.StatusOK {
		t.Errorf("valid upload: %d", code)
	} else if got, err := kept.Get(t.Context(), "kept/presigned.bin", nil); err != nil || !bytes.Equal(got.Data, data) {
		t.Errorf("presigned object: %+v %v", got, err)
	}
	page, err := kept.List(t.Context(), "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range page.Objects {
		t.Logf("stored drive-verify/%s (%d bytes)", obj.Key, obj.Size)
	}
	if len(page.Objects) < 2 {
		t.Fatalf("expected hello.txt and presigned.bin to remain, got %+v", page.Objects)
	}
}
