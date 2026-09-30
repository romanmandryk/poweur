package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/poweur/api/internal/drive/provider"
	idpkg "github.com/poweur/identity"
)

// DeleteIdentityData erases everything this relay keeps for one hosted
// identity: its signed ID document, its encrypted key backups and device list,
// its waiting messages and acknowledgements, its drive (files, history, system
// files) and any storage-quota override. It is idempotent: run it again and it
// finishes whatever a failed run left. It returns the number of objects
// removed.
//
// It does not touch what other identities hold about this one (their contacts,
// shares of their own folders to it, messages it already delivered to them),
// and a running relay keeps the identity in memory until it restarts; the
// suspension that `identities delete` puts in place first is what keeps it
// unusable until then.
func DeleteIdentityData(ctx context.Context, store provider.Store, identity string) (int, error) {
	key, err := idpkg.SanitizeIdentityDirName(identity)
	if err != nil {
		return 0, err
	}
	removed := 0
	remove := func(object string) error {
		if err := store.Delete(ctx, object); err != nil {
			if errors.Is(err, provider.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("delete %s: %w", object, err)
		}
		removed++
		return nil
	}
	// The ID document first: a relay that restarts part-way no longer hosts it.
	for _, object := range []string{"relay/identities/" + key + ".json", "relay/keystore/" + key + ".json"} {
		if err := remove(object); err != nil {
			return removed, err
		}
	}
	for _, prefix := range []string{
		"relay/spool/messages/" + key + "/",
		"relay/spool/acks/" + key + "/",
		"drives/" + key + "/",
	} {
		// Collect first: deleting while a cursor is open can skip keys.
		var keys []string
		cursor := ""
		for {
			page, err := store.List(ctx, prefix, cursor, 1000)
			if err != nil {
				return removed, fmt.Errorf("list %s: %w", prefix, err)
			}
			for _, info := range page.Objects {
				keys = append(keys, info.Key)
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
		for _, object := range keys {
			if err := remove(object); err != nil {
				return removed, err
			}
		}
	}
	if _, err := EditQuotas(ctx, store, func(doc map[string]json.RawMessage) error {
		delete(doc, normalizeSuspended(identity))
		return nil
	}); err != nil {
		return removed, err
	}
	return removed, nil
}
