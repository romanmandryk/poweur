package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	idpkg "github.com/poweur/identity"
)

// Objects is where the relay's own registries persist: the identity index,
// the spool of undelivered mail and acks, and the keystore (EPIC-020 E20-T6).
// It is the drive's provider — a disk under POWEUR_DATA or an S3 bucket — so
// a relay keeps no durable state outside it. Keys live under `relay/`,
// beside the drives under `drives/`. A nil Objects keeps everything in
// memory.
type Objects interface {
	Get(context.Context, string, *provider.Range) (provider.Object, error)
	Put(context.Context, string, []byte) (string, error)
	Delete(context.Context, string) error
	List(context.Context, string, string, int) (provider.Page, error)
}

// objectTimeout bounds one registry read or write.
const objectTimeout = 30 * time.Second

func objectContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), objectTimeout)
}

// identityKey maps an identity to its key segment; the sanitizer keeps a
// name from climbing out of its prefix.
func identityKey(identity string) (string, error) {
	return idpkg.SanitizeIdentityDirName(identity)
}

// identityFromKey reverses identityKey (dots become "__").
func identityFromKey(segment string) string { return strings.ReplaceAll(segment, "__", ".") }

// listAll calls fn for every object under prefix, in key order.
func listAll(objects Objects, prefix string, fn func(provider.Info) error) error {
	ctx, cancel := objectContext()
	defer cancel()
	cursor := ""
	for {
		page, err := objects.List(ctx, prefix, cursor, 1000)
		if err != nil {
			return err
		}
		for _, info := range page.Objects {
			if err := fn(info); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		cursor = page.Next
	}
}

func getObject(objects Objects, key string) ([]byte, error) {
	ctx, cancel := objectContext()
	defer cancel()
	obj, err := objects.Get(ctx, key, nil)
	return obj.Data, err
}

func putObject(objects Objects, key string, data []byte) error {
	ctx, cancel := objectContext()
	defer cancel()
	_, err := objects.Put(ctx, key, data)
	return err
}

func deleteObject(objects Objects, key string) error {
	ctx, cancel := objectContext()
	defer cancel()
	err := objects.Delete(ctx, key)
	if errors.Is(err, provider.ErrNotFound) {
		return nil
	}
	return err
}
