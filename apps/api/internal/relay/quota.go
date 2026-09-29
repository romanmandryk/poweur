package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

// quotaOverrides is the operator's per-identity storage quota document: a
// JSON object from identity to size, where a size is bytes or a string with
// a unit ("500MB", "2GiB"; 0 = unlimited). Support raises one identity's
// limit by editing it, so it is re-read when it changes, and a broken edit
// keeps the last good version rather than dropping everyone to the default.
//
// On a relay with a store it lives there, at relay/storage-quotas.json (edit
// it with `poweur-relay quotas`), so an S3 relay needs no disk and every
// relay process sees the same limits; it is re-read at most every
// quotaStoreInterval. While the store has none, the local file
// (STORAGE_QUOTAS_FILE, or $POWEUR_DATA/storage-quotas.json) is used, as on
// a relay without a store.
type quotaOverrides struct {
	path   string
	store  provider.Store
	onBad  func(error)
	mu     sync.Mutex
	mod    time.Time
	size   int64
	values map[string]int64
	// Store source: when it was last checked and what it held.
	checked   time.Time
	storeHash string
	fromStore bool
}

// QuotaObjectKey is where a relay's quota overrides live in its store.
const QuotaObjectKey = "relay/storage-quotas.json"

const quotaStoreInterval = time.Minute

func newQuotaOverrides(path string, onBad func(error)) *quotaOverrides {
	return &quotaOverrides{path: path, onBad: onBad}
}

// lookup returns the identity's own quota, if the document names one.
func (q *quotaOverrides) lookup(identity string) (int64, bool) {
	if q == nil || q.path == "" && q.store == nil {
		return 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.refreshLocked()
	value, ok := q.values[strings.ToLower(identity)]
	return value, ok
}

func (q *quotaOverrides) refreshLocked() {
	if q.store != nil {
		if !q.checked.IsZero() && time.Since(q.checked) < quotaStoreInterval {
			return
		}
		q.checked = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		obj, err := q.store.Get(ctx, QuotaObjectKey, nil)
		cancel()
		switch {
		case err == nil:
			sum := sha256.Sum256(obj.Data)
			hash := hex.EncodeToString(sum[:])
			if q.fromStore && hash == q.storeHash {
				return
			}
			q.fromStore, q.storeHash = true, hash
			values, err := parseQuotaOverrides(obj.Data)
			if err == nil {
				q.values = values
			} else if q.onBad != nil {
				q.onBad(err)
			}
			return
		case !errors.Is(err, provider.ErrNotFound):
			// The store is briefly unreachable: keep what we have.
			return
		}
		if q.fromStore {
			q.fromStore, q.storeHash, q.values, q.mod, q.size = false, "", nil, time.Time{}, 0
		}
		if q.path == "" {
			return
		}
	}
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

// EditQuotas changes the quota document in a store: edit receives the
// current entries (identity → size, as written) and changes them in place.
// The result is validated before it is written, and the write is
// conditional, so two operators editing at once cannot lose an update.
func EditQuotas(ctx context.Context, store provider.Store, edit func(map[string]json.RawMessage) error) (map[string]int64, error) {
	obj, err := store.Get(ctx, QuotaObjectKey, nil)
	doc := map[string]json.RawMessage{}
	match := ""
	switch {
	case err == nil:
		if err := json.Unmarshal(obj.Data, &doc); err != nil {
			return nil, fmt.Errorf("stored quotas are not valid JSON: %w", err)
		}
		match = obj.ETag
	case !errors.Is(err, provider.ErrNotFound):
		return nil, err
	}
	if err := edit(doc); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	values, err := parseQuotaOverrides(raw)
	if err != nil {
		return nil, err
	}
	if _, err := store.PutIf(ctx, QuotaObjectKey, append(raw, '\n'), match); err != nil {
		if errors.Is(err, provider.ErrPrecondition) {
			return nil, errors.New("the quotas changed while editing; run the command again")
		}
		return nil, err
	}
	return values, nil
}

// ParseQuotaSize parses a size as the quota document accepts it.
func ParseQuotaSize(text string) (int64, error) { return parseQuotaSize(text) }

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
