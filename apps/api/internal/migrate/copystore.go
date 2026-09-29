package migrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

// CopyStore copies every object under the relay's prefixes (drives/ and
// relay/) from one provider to another — moving a relay from its disk to a
// bucket, or between buckets. Run it with the relay stopped. Each object is
// read back from the destination and compared before it counts; objects the
// destination already holds with identical bytes are skipped, so an
// interrupted copy is simply run again. Transient errors are retried.
func CopyStore(ctx context.Context, from, to provider.Store, dryRun bool, log io.Writer) (CopyReport, error) {
	var report CopyReport
	for _, prefix := range []string{"drives/", "relay/"} {
		cursor := ""
		for {
			var page provider.Page
			err := retry(ctx, func() error {
				var err error
				page, err = from.List(ctx, prefix, cursor, 1000)
				return err
			})
			if err != nil {
				return report, fmt.Errorf("list %s: %w", prefix, err)
			}
			for _, info := range page.Objects {
				if err := copyOne(ctx, from, to, info.Key, dryRun, &report); err != nil {
					return report, fmt.Errorf("%s: %w", info.Key, err)
				}
				if log != nil && (report.Copied+report.Skipped)%500 == 0 {
					fmt.Fprintf(log, "%d copied, %d already there\n", report.Copied, report.Skipped)
				}
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	if log != nil {
		fmt.Fprintf(log, "done: %d copied (%d bytes), %d already there\n", report.Copied, report.Bytes, report.Skipped)
	}
	return report, nil
}

// CopyReport counts what a copy did.
type CopyReport struct {
	Copied  int   `json:"copied"`
	Skipped int   `json:"skipped"`
	Bytes   int64 `json:"bytes"`
}

func copyOne(ctx context.Context, from, to provider.Store, key string, dryRun bool, report *CopyReport) error {
	var source provider.Object
	if err := retry(ctx, func() error {
		var err error
		source, err = from.Get(ctx, key, nil)
		return err
	}); err != nil {
		return err
	}
	var existing provider.Object
	err := retry(ctx, func() error {
		var err error
		existing, err = to.Get(ctx, key, nil)
		if errors.Is(err, provider.ErrNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	if existing.Data != nil && bytes.Equal(existing.Data, source.Data) {
		report.Skipped++
		return nil
	}
	if dryRun {
		report.Copied++
		report.Bytes += int64(len(source.Data))
		return nil
	}
	return retry(ctx, func() error {
		if _, err := to.Put(ctx, key, source.Data); err != nil {
			return err
		}
		back, err := to.Get(ctx, key, nil)
		if err != nil {
			return err
		}
		if !bytes.Equal(back.Data, source.Data) {
			return errors.New("destination returned different bytes")
		}
		report.Copied++
		report.Bytes += int64(len(source.Data))
		return nil
	})
}

// retry runs op up to five times with backoff, for object stores that fail
// transiently; a missing object or an invalid key is final.
func retry(ctx context.Context, op func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = op(); err == nil || errors.Is(err, provider.ErrInvalidKey) || errors.Is(err, provider.ErrNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(200*(1<<attempt)) * time.Millisecond):
		}
	}
	if strings.Contains(err.Error(), "Access Denied") {
		return fmt.Errorf("%w (the store keeps refusing these credentials)", err)
	}
	return err
}
