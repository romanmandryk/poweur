package drive_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/drive"
	"github.com/poweur/api/internal/drive/provider"
)

func TestOpenFilesystemAndMemory(t *testing.T) {
	none, err := drive.Open(config.Config{StorageProvider: config.StorageFS})
	if err != nil || none != nil {
		t.Fatalf("memory relay: %v %+v", err, none)
	}
	dir := t.TempDir()
	store, err := drive.Open(config.Config{StorageProvider: config.StorageFS, DataDir: dir})
	if err != nil || store == nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, ok := store.(interface{ Close() error }); ok {
			c.Close()
		}
	})
	if _, err := store.Put(t.Context(), "drives/owner/chunks/object", []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), "drives/owner/chunks/object", nil)
	if err != nil || !bytes.Equal(got.Data, []byte("cipher")) {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	if _, err := drive.Open(config.Config{StorageProvider: "relay-fs", DataDir: dir}); err == nil {
		t.Fatal("removed provider accepted")
	}
}

func TestOpenS3(t *testing.T) {
	endpoint := os.Getenv("POWEUR_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set POWEUR_TEST_S3_ENDPOINT to run MinIO conformance")
	}
	access := os.Getenv("POWEUR_TEST_S3_ACCESS_KEY")
	secret := os.Getenv("POWEUR_TEST_S3_SECRET_KEY")
	secure := os.Getenv("POWEUR_TEST_S3_SECURE") == "1"
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: secure, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	bucket := "poweur-open-" + hex.EncodeToString(random)
	if err := client.MakeBucket(t.Context(), bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for item := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if item.Err != nil {
				t.Error(item.Err)
				return
			}
			if err := client.RemoveObject(ctx, bucket, item.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Error(err)
			}
		}
		if err := client.RemoveBucket(ctx, bucket); err != nil {
			t.Error(err)
		}
	})
	store, err := drive.Open(config.Config{
		StorageProvider: config.StorageS3,
		S3Endpoint:      endpoint,
		S3Bucket:        bucket,
		S3Prefix:        "tenant",
		S3Region:        "us-east-1",
		S3AccessKey:     access,
		S3SecretKey:     secret,
		S3Secure:        secure,
		S3Presign:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := store.Put(t.Context(), "drives/owner/chunks/object", []byte("cipher"))
	if err != nil || tag == "" {
		t.Fatal(err)
	}
	if _, err := store.PutIf(t.Context(), "drives/owner/chunks/object", []byte("other"), ""); !errors.Is(err, provider.ErrPrecondition) {
		t.Fatal(err)
	}
}

func TestProbeTimeoutsAreNotIncompatibility(t *testing.T) {
	wrapped := fmt.Errorf("S3 conditional create: %w", &url.Error{Op: "Put", URL: "https://s3.test/x", Err: context.DeadlineExceeded})
	if !drive.TimedOut(wrapped) || !drive.TimedOut(fmt.Errorf("S3 conditional replace: %w", context.DeadlineExceeded)) {
		t.Fatal("a slow store was treated as incompatible")
	}
	if drive.TimedOut(errors.New("S3 conditional create is not enforced (<nil>)")) {
		t.Fatal("an incompatible store was treated as slow")
	}
}
