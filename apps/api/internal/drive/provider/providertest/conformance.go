// Package providertest runs the same contract against filesystem and S3 stores.
package providertest

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/poweur/api/internal/drive/provider"
)

func Run(t *testing.T, newStore func(*testing.T) provider.Store) {
	t.Helper()
	t.Run("roundtrip-range-delete", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		if _, err := s.Get(ctx, "missing", nil); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		tag, err := s.Put(ctx, "dir/object", []byte("abcdef"))
		if err != nil || tag == "" {
			t.Fatalf("put: %q %v", tag, err)
		}
		got, err := s.Get(ctx, "dir/object", nil)
		if err != nil || !bytes.Equal(got.Data, []byte("abcdef")) || got.ETag != tag || got.Size != 6 {
			t.Fatalf("get: %+v %v", got, err)
		}
		got, err = s.Get(ctx, "dir/object", &provider.Range{Offset: 2, Length: 3})
		if err != nil || string(got.Data) != "cde" || got.ETag != tag || got.Size != 6 {
			t.Fatalf("range: %+v %v", got, err)
		}
		for _, r := range []provider.Range{{-1, 1}, {0, 0}, {6, 1}, {0, 7}, {math.MaxInt64, math.MaxInt64}} {
			if _, err := s.Get(ctx, "dir/object", &r); !errors.Is(err, provider.ErrRange) {
				t.Fatalf("range %+v: %v", r, err)
			}
		}
		if err := s.Delete(ctx, "dir/object"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "dir/object", nil); !errors.Is(err, provider.ErrNotFound) {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "dir/object"); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("delete missing: %v", err)
		}
		if _, err := s.Put(ctx, "empty", nil); err != nil {
			t.Fatal(err)
		}
		got, err = s.Get(ctx, "empty", nil)
		if err != nil || len(got.Data) != 0 || got.Size != 0 {
			t.Fatalf("empty: %+v %v", got, err)
		}
	})
	t.Run("conditional", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		tag, err := s.PutIf(ctx, "object", []byte("one"), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutIf(ctx, "object", []byte("two"), ""); !errors.Is(err, provider.ErrPrecondition) {
			t.Fatalf("create existing: %v", err)
		}
		if _, err := s.PutIf(ctx, "missing", []byte("two"), tag); !errors.Is(err, provider.ErrPrecondition) {
			t.Fatalf("replace missing: %v", err)
		}
		next, err := s.PutIf(ctx, "object", []byte("two"), tag)
		if err != nil || next == tag {
			t.Fatalf("replace: %s %v", next, err)
		}
		if _, err := s.PutIf(ctx, "object", []byte("three"), tag); !errors.Is(err, provider.ErrPrecondition) {
			t.Fatalf("stale replace: %v", err)
		}
		got, err := s.Get(ctx, "object", nil)
		if err != nil || string(got.Data) != "two" {
			t.Fatalf("rejected write mutated bytes: %+v %v", got, err)
		}
	})
	t.Run("concurrent-conditional", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		tag, err := s.Put(ctx, "object", []byte("base"))
		if err != nil {
			t.Fatal(err)
		}
		const writers = 16
		results := make(chan error, writers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := s.PutIf(ctx, "object", []byte{byte(i)}, tag)
				results <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, provider.ErrPrecondition) {
				t.Error(err)
			}
		}
		if wins != 1 {
			t.Fatalf("conditional winners: %d", wins)
		}
	})
	t.Run("listing", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		for _, key := range []string{"drive/b", "drive/a/x", "other/z", "drive/a.b"} {
			if _, err := s.Put(ctx, key, []byte(key)); err != nil {
				t.Fatal(err)
			}
		}
		var keys []string
		cursor := ""
		for i := 0; i < 4; i++ {
			page, err := s.List(ctx, "drive/", cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, obj := range page.Objects {
				keys = append(keys, obj.Key)
				if obj.Size != int64(len(obj.Key)) || obj.Modified.IsZero() {
					t.Fatalf("metadata %+v", obj)
				}
			}
			if page.Next == "" {
				break
			}
			if page.Next == cursor {
				t.Fatal("cursor did not advance")
			}
			cursor = page.Next
		}
		if len(keys) != 3 || keys[0] != "drive/a.b" || keys[1] != "drive/a/x" || keys[2] != "drive/b" {
			t.Fatalf("listing: %v", keys)
		}
		if _, err := s.List(ctx, "drive/", "other/z", 1); err == nil {
			t.Fatal("foreign cursor accepted")
		}
		for _, limit := range []int{0, 1001} {
			if _, err := s.List(ctx, "", "", limit); err == nil {
				t.Fatal("bad limit accepted")
			}
		}
	})
	t.Run("invalid-keys", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		for _, key := range []string{"", "/absolute", "../escape", "a/../b", "a//b", "a/", "a\\b", "a\x00b", "a/.tmp-secret", "a/./b"} {
			if _, err := s.Get(ctx, key, nil); !errors.Is(err, provider.ErrInvalidKey) {
				t.Errorf("get %q: %v", key, err)
			}
			if _, err := s.Put(ctx, key, nil); !errors.Is(err, provider.ErrInvalidKey) {
				t.Errorf("put %q: %v", key, err)
			}
			if _, err := s.PutIf(ctx, key, nil, ""); !errors.Is(err, provider.ErrInvalidKey) {
				t.Errorf("putif %q: %v", key, err)
			}
			if err := s.Delete(ctx, key); !errors.Is(err, provider.ErrInvalidKey) {
				t.Errorf("delete %q: %v", key, err)
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := s.Put(ctx, "key", nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "key", nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := s.List(ctx, "", "", 1); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "key"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
