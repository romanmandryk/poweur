// Package drive opens the object store the drive engine will publish through.
// The engine itself is E20-T4; this package is the configuration seam.
package drive

import (
	"context"
	"fmt"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/drive/provider/fs"
	stores3 "github.com/poweur/api/internal/drive/provider/s3"
)

// Open returns the durable object store for cfg. A filesystem provider with
// an empty POWEUR_DATA returns a nil store: that relay keeps no durable drive.
// An S3 store is probed for conditional writes before it is returned. The
// bucket must already exist.
func Open(cfg config.Config) (provider.Store, error) {
	switch cfg.StorageProvider {
	case "", config.StorageFS:
		if cfg.DataDir == "" {
			return nil, nil
		}
		store, err := fs.Open(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	case config.StorageS3:
		store, err := stores3.New(stores3.Config{
			Endpoint:     cfg.S3Endpoint,
			Bucket:       cfg.S3Bucket,
			Prefix:       cfg.S3Prefix,
			Region:       cfg.S3Region,
			AccessKey:    cfg.S3AccessKey,
			SecretKey:    cfg.S3SecretKey,
			SessionToken: cfg.S3SessionToken,
			Secure:       cfg.S3Secure,
			Presign:      cfg.S3Presign,
		})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := store.Probe(ctx); err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("invalid STORAGE_PROVIDER: %s (use fs|s3)", cfg.StorageProvider)
	}
}
