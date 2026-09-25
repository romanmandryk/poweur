package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// quotaOverrides is the operator's per-identity storage quota file: a JSON
// object from identity to size, where a size is bytes or a string with a unit
// ("500MB", "2GiB"; 0 = unlimited). Support raises one identity's limit by
// editing it, so it is re-read whenever it changes on disk, and a broken edit
// keeps the last good version rather than dropping everyone to the default.
type quotaOverrides struct {
	path   string
	onBad  func(error)
	mu     sync.Mutex
	mod    time.Time
	size   int64
	values map[string]int64
}

func newQuotaOverrides(path string, onBad func(error)) *quotaOverrides {
	return &quotaOverrides{path: path, onBad: onBad}
}

// lookup returns the identity's own quota, if the file names one.
func (q *quotaOverrides) lookup(identity string) (int64, bool) {
	if q == nil || q.path == "" {
		return 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.refreshLocked()
	value, ok := q.values[strings.ToLower(identity)]
	return value, ok
}

func (q *quotaOverrides) refreshLocked() {
	info, err := os.Stat(q.path)
	if err != nil {
		// No file is the normal case: nobody has an override.
		q.values, q.mod, q.size = nil, time.Time{}, 0
		return
	}
	if info.ModTime().Equal(q.mod) && info.Size() == q.size {
		return
	}
	raw, err := os.ReadFile(q.path)
	if err == nil {
		var values map[string]int64
		if values, err = parseQuotaOverrides(raw); err == nil {
			q.values = values
		}
	}
	// Remember this version either way, so a broken file is reported once
	// per edit rather than on every upload.
	q.mod, q.size = info.ModTime(), info.Size()
	if err != nil && q.onBad != nil {
		q.onBad(err)
	}
}

func parseQuotaOverrides(raw []byte) (map[string]int64, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("storage quotas: %w", err)
	}
	values := make(map[string]int64, len(doc))
	for identity, value := range doc {
		size, err := parseQuotaValue(value)
		if err != nil {
			return nil, fmt.Errorf("storage quotas: %s: %w", identity, err)
		}
		values[strings.ToLower(strings.TrimSpace(identity))] = size
	}
	return values, nil
}

var quotaUnits = []struct {
	suffix string
	bytes  int64
}{
	// Longest first, so "MiB" is not read as "B".
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
	{"b", 1},
}

func parseQuotaValue(raw json.RawMessage) (int64, error) {
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number < 0 {
			return 0, fmt.Errorf("negative size")
		}
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, fmt.Errorf("size must be a number of bytes or a string like \"2GB\"")
	}
	return parseQuotaSize(text)
}

func parseQuotaSize(text string) (int64, error) {
	value := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	multiplier := int64(1)
	for _, unit := range quotaUnits {
		if strings.HasSuffix(value, unit.suffix) {
			value, multiplier = strings.TrimSuffix(value, unit.suffix), unit.bytes
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid size %q", text)
	}
	return int64(number * float64(multiplier)), nil
}

func logQuotaFileError(err error) {
	log.Printf("storage quotas: keeping the last good version: %v", err)
}

// storageQuota is the identity's quota in bytes: its override, or the relay
// default. 0 means unlimited.
func (s *Server) storageQuota(identity string) int64 {
	if value, ok := s.quotas.lookup(identity); ok {
		return value
	}
	return s.cfg.MaxIdentityBytes
}

// overQuota reports whether adding bytes would take the identity past its quota.
func (s *Server) overQuota(ctx context.Context, identity string, adding int64) bool {
	quota := s.storageQuota(identity)
	if quota <= 0 {
		return false
	}
	used, err := s.filesProvider.UsedBytes(ctx, identity)
	return err == nil && used+adding > quota
}

// writeQuotaExceeded is the 507 an upload over quota gets, naming who to ask
// for more space when the operator has said.
func (s *Server) writeQuotaExceeded(w http.ResponseWriter) {
	detail := "identity storage quota exceeded"
	if s.cfg.QuotaContact != "" {
		detail += "; message " + s.cfg.QuotaContact + " to ask for more space"
	}
	if tw, ok := w.(interface{ telemetryError(string) }); ok {
		tw.telemetryError("quota_exceeded")
	}
	writeJSON(w, http.StatusInsufficientStorage, ErrorResponse{Error: "quota_exceeded", Detail: detail, Contact: s.cfg.QuotaContact})
}
