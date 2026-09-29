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
	// A version still carrying a live node's key, name or content-key
	// envelope is kept however old: the node cannot be opened without it.
	envelopes := map[string]bool{}
	for _, n := range h.st.Nodes {
		if n.Removed {
			continue
		}
		e.fillEnvelopes(ctx, h, n)
		for _, id := range []string{n.KeyVersion, n.NameVersion, n.ContentVersion} {
			envelopes[id] = true
		}
	}
	var expired, stripped []string
	for id, v := range h.st.Versions {
		if v.Superseded.IsZero() || !v.Superseded.Before(cutoff) {
			continue
		}
		if !envelopes[id] {
			expired = append(expired, id)
		} else if len(v.Pages) > 0 {
			stripped = append(stripped, id)
		}
	}
	sort.Strings(expired)
	sort.Strings(stripped)
	if len(expired) > 0 || len(stripped) > 0 {
		before := make(map[string]bool, len(h.st.Chunks))
		for id := range h.st.Chunks {
			before[id] = true
		}
		usedBefore := h.st.Used
		nodes := map[string]string{}
		for _, id := range expired {
			nodes[id] = h.st.Versions[id].Node
		}
		if err := e.publish(ctx, h, journalOp{Kind: kindGC, At: e.now(), GC: &gcOp{Versions: expired, Stripped: stripped}}); err != nil {
			return result, err
		}
		result.Versions, result.Freed = len(expired)+len(stripped), usedBefore-h.st.Used
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
	// Uploads no version or record references, and system documents no
	// path refers to, past their grace period.
	uploadCutoff := e.now().Add(-e.opts.UploadTTL)
	liveSystem := map[string]bool{}
	for _, f := range h.st.System {
		liveSystem[f.Hash] = true
	}
	sweeps := []struct {
		dir  string
		live func(string) bool
	}{
		{h.prefix + "chunks/", func(id string) bool { return h.st.Chunks[id] != nil }},
		{h.prefix + "system/", func(id string) bool { return liveSystem[id] }},
	}
	for _, sweep := range sweeps {
		cursor := ""
		for {
			page, err := e.opts.Store.List(ctx, sweep.dir, cursor, 1000)
			if err != nil {
				return result, err
			}
			for _, obj := range page.Objects {
				if !sweep.live(strings.TrimPrefix(obj.Key, sweep.dir)) && obj.Modified.Before(uploadCutoff) {
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
	}
	return result, nil
}
