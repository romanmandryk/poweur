// Package drive is the authenticated storage-v2 client transport.
package drive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	protocol "github.com/poweur/identity/drive"
)

type Client struct {
	Relay, Identity, Drive string
	SessionID              string
	Key                    ed25519.PrivateKey
	// LinkID authenticates as a link holder instead of an identity (with
	// LinkVerifier for a password-protected link); Key is then only a guest
	// signing key.
	LinkID       string
	LinkVerifier []byte
	HTTP         *http.Client
	Cache        ChunkCache
}

type Error struct {
	Status       int
	Code, Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("drive: HTTP %d %s: %s", e.Status, e.Code, e.Detail)
}
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (c *Client) auth(ctx context.Context) (http.Header, error) {
	if c.LinkID != "" {
		h := http.Header{}
		h.Set("X-Poweur-Link", c.LinkID)
		if len(c.LinkVerifier) > 0 {
			h.Set("X-Poweur-Link-Verifier", base64.RawURLEncoding.EncodeToString(c.LinkVerifier))
		}
		return h, nil
	}
	if len(c.Key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid signing key")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Relay, "/")+"/auth/challenge?identity="+url.QueryEscape(c.Identity), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, responseError(resp)
	}
	var body struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Challenge == "" {
		return nil, fmt.Errorf("empty auth challenge")
	}
	h := http.Header{}
	if c.SessionID != "" {
		h.Set("X-Poweur-Session-Id", c.SessionID)
	}
	h.Set("X-Poweur-Identity", c.Identity)
	h.Set("X-Poweur-Challenge", body.Challenge)
	h.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(c.Key, []byte(body.Challenge))))
	return h, nil
}
func responseError(resp *http.Response) error {
	var body struct {
		Code   string `json:"error"`
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	return &Error{resp.StatusCode, body.Code, body.Detail}
}
func (c *Client) request(ctx context.Context, method, suffix string, body []byte) ([]byte, error) {
	h, err := c.auth(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Relay, "/")+"/drive/"+url.PathEscape(c.driveID())+suffix, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = h
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("drive response too large")
	}
	return data, err
}

// Get reads metadata through an authenticated drive-relative API path.
func (c *Client) Get(ctx context.Context, suffix string, out any) error {
	raw, err := c.request(ctx, "GET", suffix, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type Commit struct {
	ID       string                  `json:"id"`
	Manifest *protocol.Manifest      `json:"manifest,omitempty"`
	Pages    []protocol.ChunkPage    `json:"pages,omitempty"`
	Records  []protocol.AppendRecord `json:"records,omitempty"`
	Trim     *Trim                   `json:"trim,omitempty"`
	Share    *protocol.Share         `json:"share,omitempty"`
	Unshare  *Unshare                `json:"unshare,omitempty"`
	Transfer *Transfer               `json:"transfer,omitempty"`
}

// Trim drops append records before a position covered by a snapshot version.
type Trim struct {
	Node     string   `json:"node"`
	Before   uint64   `json:"before"`
	Snapshot Snapshot `json:"snapshot"`
}
type Snapshot struct {
	Node    string `json:"node"`
	Version string `json:"version"`
}
type Unshare struct {
	ID string `json:"id"`
}

// Transfer retires a subtree after the caller has recreated it on To.
type Transfer struct {
	Node   string `json:"node"`
	To     string `json:"to"`
	ToNode string `json:"to_node"`
}
type Result struct {
	Seq       uint64   `json:"seq"`
	Head      string   `json:"head"`
	Positions []uint64 `json:"positions"`
}

// Commit retries transient failures with one idempotency key. Conflicts are
// returned to the caller for an explicit merge, never silently overwritten.
func (c *Client) Commit(ctx context.Context, input Commit) (Result, error) {
	if input.ID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Result{}, err
		}
		input.ID = hex.EncodeToString(id[:])
	}
	body, err := json.Marshal(input)
	if err != nil {
		return Result{}, err
	}
	for attempt := 0; ; attempt++ {
		raw, err := c.request(ctx, "POST", "/commit", body)
		if err == nil {
			var result Result
			err = json.Unmarshal(raw, &result)
			return result, err
		}
		retry := false
		if e, ok := err.(*Error); ok {
			retry = e.Status == 502 || e.Status == 503 || e.Status == 504
		} else {
			_, retry = err.(*url.Error)
		}
		if !retry || attempt == 2 {
			return Result{}, err
		}
		timer := time.NewTimer(time.Duration(100*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *Client) driveID() string {
	if c.Drive != "" {
		return c.Drive
	}
	return c.Identity
}

type chunkTarget struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}
type missingChunk struct {
	protocol.ChunkRef
	Upload chunkTarget `json:"upload"`
}

// Store uploads only chunks the relay does not already have. A returned URL
// may be a relay path or a presigned object-store URL; presigned requests
// carry the provider's headers and no Poweur credential.
func (c *Client) Store(ctx context.Context, blobs [][]byte) ([]protocol.ChunkRef, error) {
	refs := make([]protocol.ChunkRef, len(blobs))
	byID := map[string][]byte{}
	for i, blob := range blobs {
		refs[i] = protocol.ChunkRef{ID: protocol.ChunkID(blob), Size: uint64(len(blob))}
		byID[refs[i].ID] = blob
	}
	for start := 0; start < len(refs); start += protocol.PageSize {
		end := min(start+protocol.PageSize, len(refs))
		var body struct {
			Missing []missingChunk `json:"missing"`
		}
		raw, err := json.Marshal(map[string]any{"chunks": refs[start:end]})
		if err != nil {
			return nil, err
		}
		resp, err := c.request(ctx, "POST", "/chunks/missing", raw)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(resp, &body); err != nil {
			return nil, err
		}
		for _, item := range body.Missing {
			blob, ok := byID[item.ID]
			if !ok || uint64(len(blob)) != item.Size {
				return nil, fmt.Errorf("missing chunk is not in this upload")
			}
			if err = c.putChunk(ctx, item.Upload, blob); err != nil {
				return nil, err
			}
		}
	}
	if c.Cache != nil {
		for id, blob := range byID {
			_ = c.Cache.Put(id, blob)
		}
	}
	return refs, nil
}
func (c *Client) putChunk(ctx context.Context, target chunkTarget, blob []byte) error {
	method := target.Method
	if method == "" {
		method = http.MethodPut
	}
	var req *http.Request
	var err error
	authed := !strings.HasPrefix(target.URL, "http://") && !strings.HasPrefix(target.URL, "https://")
	if authed {
		if !strings.HasPrefix(target.URL, "/") {
			return fmt.Errorf("invalid upload url")
		}
		h, err := c.auth(ctx)
		if err != nil {
			return err
		}
		req, err = http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Relay, "/")+target.URL, bytes.NewReader(blob))
		if err != nil {
			return err
		}
		req.Header = h
	} else {
		req, err = http.NewRequestWithContext(ctx, method, target.URL, bytes.NewReader(blob))
		if err != nil {
			return err
		}
		for key, value := range target.Headers {
			req.Header.Set(key, value)
		}
	}
	// A redirect would send the ciphertext, and any credential, elsewhere.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}
func (c *Client) Upload(ctx context.Context, ciphertext []byte) (protocol.ChunkRef, error) {
	refs, err := c.Store(ctx, [][]byte{ciphertext})
	if err != nil || len(refs) != 1 {
		return protocol.ChunkRef{}, err
	}
	return refs[0], nil
}

// Event is one server-sent drive notification. Events are advisory: a client
// that misses one reads the changes cursor.
type Event struct {
	Type      string          `json:"type"`
	Identity  string          `json:"identity"`
	Timestamp string          `json:"timestamp"`
	Drive     json.RawMessage `json:"drive,omitempty"`
}

// Subscribe reads GET /drive/{id}/events until ctx ends or the relay closes
// the stream. onEvent returning an error stops the stream.
func (c *Client) Subscribe(ctx context.Context, onEvent func(Event) error) error {
	h, err := c.auth(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Relay, "/")+"/drive/"+url.PathEscape(c.driveID())+"/events", nil)
	if err != nil {
		return err
	}
	req.Header = h
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4096), 256<<10)
	var frame []string
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			frame = append(frame, line)
			continue
		}
		if event, ok := parseEvent(frame); ok {
			if err = onEvent(event); err != nil {
				return err
			}
		}
		frame = nil
	}
	return scanner.Err()
}
func parseEvent(frame []string) (Event, bool) {
	var data []string
	for _, line := range frame {
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimSpace(rest))
		}
	}
	if len(data) == 0 {
		return Event{}, false
	}
	var event Event
	if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) != nil || event.Type == "" {
		return Event{}, false
	}
	return event, true
}
func (c *Client) Chunk(ctx context.Context, node, version string, ref protocol.ChunkRef) ([]byte, error) {
	if c.Cache != nil {
		cached, err := c.Cache.Get(ref.ID)
		if err == nil && uint64(len(cached)) == ref.Size && protocol.ChunkID(cached) == ref.ID {
			return cached, nil
		}
	}
	path := "/nodes/" + url.PathEscape(node)
	if version != "" {
		path += "/versions/" + url.PathEscape(version)
	}
	data, err := c.request(ctx, "GET", path+"/chunks/"+url.PathEscape(ref.ID), nil)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != ref.Size || protocol.ChunkID(data) != ref.ID {
		return nil, fmt.Errorf("invalid chunk hash or size")
	}
	if c.Cache != nil {
		_ = c.Cache.Put(ref.ID, data)
	}
	return data, nil
}
