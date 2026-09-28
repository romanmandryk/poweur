package engine

import (
	"context"
	"sort"
	"strings"
)

// CollectResult reports what one collection pass removed.
type CollectResult struct {
	Versions int
	Chunks   int
	Orphans  int
	Freed    int64
}

// Collect drops versions superseded longer than the retention period and
// deletes chunks nothing references any more, then sweeps uploads that were
// never committed and are older than UploadTTL. It runs under the drive's
// lock, so no commit can reference a chunk it is deleting.
func (e *Engine) Collect(ctx context.Context, driveID string) (CollectResult, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return CollectResult{}, err
	}
	defer h.mu.Unlock()
	if err := e.catchUp(ctx, h); err != nil {
		return CollectResult{}, err
	}
	var result CollectResult
	cutoff := e.now().Add(-e.opts.Retention)
	var expired []string
	for id, v := range h.st.Versions {
		if !v.Superseded.IsZero() && v.Superseded.Before(cutoff) {
			expired = append(expired, id)
		}
	}
	sort.Strings(expired)
	if len(expired) > 0 {
		before := make(map[string]bool, len(h.st.Chunks))
		for id := range h.st.Chunks {
			before[id] = true
		}
		usedBefore := h.st.Used
		nodes := map[string]string{}
		for _, id := range expired {
			nodes[id] = h.st.Versions[id].Node
		}
		if err := e.publish(ctx, h, journalOp{Kind: kindGC, At: e.now(), GC: &gcOp{Versions: expired}}); err != nil {
			return result, err
		}
		result.Versions, result.Freed = len(expired), usedBefore-h.st.Used
		for id := range before {
			if h.st.Chunks[id] == nil {
				_ = e.opts.Store.Delete(ctx, h.prefix+"chunks/"+id)
				result.Chunks++
			}
		}
		for id, nodeID := range nodes {
			_ = e.opts.Store.Delete(ctx, h.prefix+"versions/"+nodeID+"/"+id+".json")
		}
	}
	// Uploads no version or record references, past their grace period.
	uploadCutoff := e.now().Add(-e.opts.UploadTTL)
	dir := h.prefix + "chunks/"
	cursor := ""
	for {
		page, err := e.opts.Store.List(ctx, dir, cursor, 1000)
		if err != nil {
			return result, err
		}
		for _, obj := range page.Objects {
			id := strings.TrimPrefix(obj.Key, dir)
			if h.st.Chunks[id] == nil && obj.Modified.Before(uploadCutoff) {
				if err := e.opts.Store.Delete(ctx, obj.Key); err == nil {
					result.Orphans++
				}
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return result, nil
}
