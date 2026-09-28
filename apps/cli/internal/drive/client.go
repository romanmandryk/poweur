// Package drive is the authenticated storage-v2 client transport.
package drive

import (
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
	HTTP                   *http.Client
	Cache                  ChunkCache
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
	drive := c.Drive
	if drive == "" {
		drive = c.Identity
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Relay, "/")+"/drive/"+url.PathEscape(drive)+suffix, bytes.NewReader(body))
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
	Share    *protocol.Share         `json:"share,omitempty"`
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
func (c *Client) Upload(ctx context.Context, ciphertext []byte) (protocol.ChunkRef, error) {
	ref := protocol.ChunkRef{ID: protocol.ChunkID(ciphertext), Size: uint64(len(ciphertext))}
	_, err := c.request(ctx, "PUT", "/chunks/"+ref.ID, ciphertext)
	if err == nil && c.Cache != nil {
		_ = c.Cache.Put(ref.ID, ciphertext)
	}
	return ref, err
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
