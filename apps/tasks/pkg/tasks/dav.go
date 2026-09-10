package tasks

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// DAVClient is the smallest WebDAV client that can carry this convention:
// GET, PUT, DELETE, MKCOL and a depth-1 PROPFIND. Written against the relay's
// documented endpoint (EPIC-003) with no Poweur libraries, because the point
// of the dogfood is to find out what a third-party author actually needs.
type DAVClient struct {
	// BaseURL is the relay origin, e.g. https://relay.poweur.net.
	BaseURL string
	// Owner is the identity whose home is being addressed. For a shared
	// project this is the *other* person's identity — a grant is read through
	// the owner's tree, not copied into yours.
	Owner string
	// Token is a bearer token minted by the owner of the credential:
	//   poweur dav token --scope dav:rw:/apps/net.poweur.tasks/
	Token string
	// HTTP is optional; http.DefaultClient with a timeout is used otherwise.
	HTTP *http.Client
}

// StatusError carries a non-2xx DAV response.
type StatusError struct {
	Method string
	Path   string
	Code   int
	Body   string
}

func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	if body == "" {
		return fmt.Sprintf("%s %s: %d", e.Method, e.Path, e.Code)
	}
	return fmt.Sprintf("%s %s: %d (%s)", e.Method, e.Path, e.Code, body)
}

// NotFound reports whether err is a 404/410 from the remote.
func NotFound(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == http.StatusNotFound || se.Code == http.StatusGone
}

// Forbidden reports whether err is a 401/403 from the remote — almost always
// a token whose scope does not cover the path.
func Forbidden(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden
}

func (c *DAVClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// url builds the absolute request URL for a tree path ("/apps/…"). Each
// segment is escaped; the caller has already validated ids, this is the
// belt to that pair of braces.
func (c *DAVClient) url(treePath string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("relay base url is required")
	}
	owner := strings.ToLower(strings.TrimSpace(c.Owner))
	if owner == "" {
		return "", fmt.Errorf("owner identity is required")
	}
	if strings.ContainsAny(owner, "/\\") || strings.Contains(owner, "..") {
		return "", fmt.Errorf("invalid owner identity %q", c.Owner)
	}
	clean := path.Clean("/" + strings.TrimPrefix(treePath, "/"))
	if clean == "/" {
		return "", fmt.Errorf("empty path")
	}
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("path escapes the tree: %q", treePath)
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("/dav/")
	b.WriteString(url.PathEscape(owner))
	for _, seg := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		b.WriteString("/")
		b.WriteString(url.PathEscape(seg))
	}
	return b.String(), nil
}

func (c *DAVClient) do(ctx context.Context, method, treePath string, body []byte, hdr map[string]string) (*http.Response, error) {
	u, err := c.url(treePath)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.httpClient().Do(req)
}

func drain(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	return string(b)
}

// Get fetches a document.
func (c *DAVClient) Get(ctx context.Context, treePath string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, treePath, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Method: "GET", Path: treePath, Code: resp.StatusCode, Body: drain(resp)}
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, MaxDocBytes+1))
}

// Put writes a document.
func (c *DAVClient) Put(ctx context.Context, treePath string, body []byte) error {
	resp, err := c.do(ctx, http.MethodPut, treePath, body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Method: "PUT", Path: treePath, Code: resp.StatusCode, Body: drain(resp)}
	}
	drain(resp)
	return nil
}

// Delete removes a document or collection.
func (c *DAVClient) Delete(ctx context.Context, treePath string) error {
	resp, err := c.do(ctx, http.MethodDelete, treePath, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Method: "DELETE", Path: treePath, Code: resp.StatusCode, Body: drain(resp)}
	}
	drain(resp)
	return nil
}

// Mkcol creates a collection, treating "already exists" as success so the
// app can call it idempotently.
func (c *DAVClient) Mkcol(ctx context.Context, treePath string) error {
	resp, err := c.do(ctx, "MKCOL", treePath, nil, nil)
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299,
		resp.StatusCode == http.StatusMethodNotAllowed, // RFC 4918: collection exists
		resp.StatusCode == http.StatusConflict:
		drain(resp)
		return nil
	}
	return &StatusError{Method: "MKCOL", Path: treePath, Code: resp.StatusCode, Body: drain(resp)}
}

// multistatus is the sliver of DAV:multistatus this app needs.
type multistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		Href  string `xml:"DAV: href"`
		Props []struct {
			ResourceType struct {
				Collection *struct{} `xml:"DAV: collection"`
			} `xml:"DAV: resourcetype"`
		} `xml:"DAV: propstat>prop"`
	} `xml:"DAV: response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`

// ListEntry is one child of a collection.
type ListEntry struct {
	Name         string
	IsCollection bool
}

// List performs a depth-1 PROPFIND and returns the collection's children
// (the collection itself is filtered out).
func (c *DAVClient) List(ctx context.Context, treePath string) ([]ListEntry, error) {
	resp, err := c.do(ctx, "PROPFIND", treePath, []byte(propfindBody), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, &StatusError{Method: "PROPFIND", Path: treePath, Code: resp.StatusCode, Body: drain(resp)}
	}
	defer resp.Body.Close()
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("PROPFIND %s: %w", treePath, err)
	}
	self, err := c.url(treePath)
	if err != nil {
		return nil, err
	}
	selfPath := strings.TrimSuffix(mustPath(self), "/")
	var out []ListEntry
	for _, r := range ms.Responses {
		href := r.Href
		if u, err := url.Parse(strings.TrimSpace(href)); err == nil {
			href = u.Path
		}
		href = strings.TrimSuffix(href, "/")
		if href == "" || href == selfPath {
			continue
		}
		name, err := url.PathUnescape(path.Base(href))
		if err != nil {
			name = path.Base(href)
		}
		isColl := false
		for _, p := range r.Props {
			if p.ResourceType.Collection != nil {
				isColl = true
			}
		}
		out = append(out, ListEntry{Name: name, IsCollection: isColl})
	}
	return out, nil
}

func mustPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Path
}
