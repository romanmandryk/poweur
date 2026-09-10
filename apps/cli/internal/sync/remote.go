package sync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Chunked-upload thresholds (E04-T3): files at or above ChunkThreshold go
// through /sync/{identity}/upload instead of a single DAV PUT.
const (
	DefaultChunkThreshold = int64(64) << 20 // 64 MiB
	DefaultChunkSize      = int64(8) << 20  // 8 MiB
)

// HTTPRemote talks to a relay's DAV + sync endpoints with a bearer token.
type HTTPRemote struct {
	RelayURL string // e.g. https://relay.example.org (no trailing slash)
	Identity string // tree owner (audience)
	Token    string // DAV bearer token
	Client   *http.Client

	// DeviceHeaders name this machine to the relay (EPIC-004 E04-T6), so
	// the changes feed can record a per-device sync cursor and the owner
	// can see how stale each device is. Optional: nil keeps the device
	// anonymous, which is the pre-registry behaviour.
	DeviceHeaders map[string]string

	// ChunkThreshold/ChunkSize override the chunked-upload defaults
	// (0 = default). Tests use tiny values.
	ChunkThreshold int64
	ChunkSize      int64
}

func (r *HTTPRemote) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (r *HTTPRemote) do(ctx context.Context, method, u string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	r.applyDevice(req)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return r.client().Do(req)
}

// applyDevice attaches the optional device identification headers.
func (r *HTTPRemote) applyDevice(req *http.Request) {
	for k, v := range r.DeviceHeaders {
		req.Header.Set(k, v)
	}
}

func (r *HTTPRemote) davURL(path string) string {
	base := strings.TrimSuffix(r.RelayURL, "/")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + "/dav/" + url.PathEscape(r.Identity) + "/" + strings.Join(segs, "/")
}

func (r *HTTPRemote) syncURL(suffix string) string {
	return strings.TrimSuffix(r.RelayURL, "/") + "/sync/" + url.PathEscape(r.Identity) + suffix
}

func httpError(op string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: %s: %s", op, resp.Status, strings.TrimSpace(string(raw)))
}

// Manifest streams /sync/{identity}/manifest (NDJSON).
func (r *HTTPRemote) Manifest(ctx context.Context) ([]Entry, string, error) {
	resp, err := r.do(ctx, http.MethodGet, r.syncURL("/manifest"), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", httpError("manifest", resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var (
		entries []Entry
		cursor  string
		first   = true
	)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if first {
			first = false
			var head struct {
				Manifest int    `json:"manifest"`
				Cursor   string `json:"cursor"`
			}
			if err := json.Unmarshal(line, &head); err != nil || head.Manifest < 1 {
				return nil, "", fmt.Errorf("manifest: malformed header %q", line)
			}
			cursor = head.Cursor
			continue
		}
		var en Entry
		if err := json.Unmarshal(line, &en); err != nil {
			return nil, "", fmt.Errorf("manifest: malformed entry: %w", err)
		}
		entries = append(entries, en)
	}
	return entries, cursor, scanner.Err()
}

// Changes drains /sync/{identity}/changes from since to the latest cursor.
func (r *HTTPRemote) Changes(ctx context.Context, since string) ([]Change, string, bool, error) {
	var all []Change
	cursor := since
	for {
		u := r.syncURL("/changes")
		if cursor != "" {
			u += "?since=" + url.QueryEscape(cursor)
		}
		resp, err := r.do(ctx, http.MethodGet, u, nil, nil)
		if err != nil {
			return nil, since, false, err
		}
		var out struct {
			Next       string   `json:"next"`
			Latest     string   `json:"latest"`
			FullResync bool     `json:"full_resync"`
			Changes    []Change `json:"changes"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			return nil, since, false, fmt.Errorf("changes: HTTP %d", status)
		}
		if decodeErr != nil {
			return nil, since, false, decodeErr
		}
		if out.FullResync {
			return nil, since, true, nil
		}
		all = append(all, out.Changes...)
		cursor = out.Next
		if out.Next == out.Latest || len(out.Changes) == 0 {
			return all, cursor, false, nil
		}
	}
}

func (r *HTTPRemote) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	resp, err := r.do(ctx, http.MethodGet, r.davURL(path), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, httpError("get "+path, resp)
	}
	return resp.Body, nil
}

func (r *HTTPRemote) Put(ctx context.Context, path string, body io.Reader, size int64) error {
	threshold := r.ChunkThreshold
	if threshold <= 0 {
		threshold = DefaultChunkThreshold
	}
	if size >= threshold {
		return r.putChunked(ctx, path, body, size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.davURL(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	r.applyDevice(req)
	req.ContentLength = size // relay requires Content-Length for quota checks
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return httpError("put "+path, resp)
	}
	return nil
}

// putChunked uploads through the resumable endpoint (E04-T3).
func (r *HTTPRemote) putChunked(ctx context.Context, path string, body io.Reader, size int64) error {
	chunk := r.ChunkSize
	if chunk <= 0 {
		chunk = DefaultChunkSize
	}
	resp, err := r.do(ctx, http.MethodPost, r.syncURL("/upload?path="+url.QueryEscape("/"+path)), nil,
		map[string]string{"Upload-Length": fmt.Sprint(size)})
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("upload create %s: HTTP %d", path, resp.StatusCode)
	}
	if decodeErr != nil {
		return decodeErr
	}
	uploadURL := r.syncURL("/upload/" + created.ID)
	var offset int64
	buf := make([]byte, chunk)
	for offset < size {
		n, err := io.ReadFull(body, buf)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			err = nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("upload %s: source ended at %d of %d bytes", path, offset, size)
		}
		presp, err := r.do(ctx, http.MethodPatch, uploadURL, bytes.NewReader(buf[:n]), map[string]string{
			"Upload-Offset": fmt.Sprint(offset),
			"Content-Type":  "application/offset+octet-stream",
		})
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(presp.Body, 4096)) //nolint:errcheck
		presp.Body.Close()
		if presp.StatusCode != http.StatusNoContent && presp.StatusCode != http.StatusOK {
			return fmt.Errorf("upload chunk %s @%d: HTTP %d", path, offset, presp.StatusCode)
		}
		offset += int64(n)
	}
	return nil
}

func (r *HTTPRemote) Mkdir(ctx context.Context, path string) error {
	resp, err := r.do(ctx, "MKCOL", r.davURL(path), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 405 = collection already exists — fine for ensure-parent semantics.
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed {
		return httpError("mkdir "+path, resp)
	}
	return nil
}

func (r *HTTPRemote) Delete(ctx context.Context, path string) error {
	resp, err := r.do(ctx, http.MethodDelete, r.davURL(path), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 404 = already gone.
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return httpError("delete "+path, resp)
	}
	return nil
}
