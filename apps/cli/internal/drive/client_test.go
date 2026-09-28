package drive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	protocol "github.com/poweur/identity/drive"
)

func fixture(t *testing.T, handle http.HandlerFunc) (*Client, *int) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(nil)
	challenges := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/challenge" {
			challenges++
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": strconv.Itoa(challenges)})
			return
		}
		signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Poweur-Signature"))
		if err != nil || !ed25519.Verify(pub, []byte(strconv.Itoa(challenges)), signature) {
			t.Error("invalid challenge signature")
			w.WriteHeader(401)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	return &Client{Relay: server.URL, Identity: "alice.poweur.net", Key: key}, &challenges
}
func TestCommitRetry(t *testing.T) {
	var bodies []string
	c, challenges := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(Result{Seq: 7, Head: "head"})
	})
	result, err := c.Commit(context.Background(), Commit{})
	if err != nil || result.Seq != 7 {
		t.Fatalf("%+v %v", result, err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || *challenges != 2 {
		t.Fatalf("unstable retry: %v, challenges=%d", bodies, *challenges)
	}
	var body Commit
	_ = json.Unmarshal([]byte(bodies[0]), &body)
	if len(body.ID) != 32 {
		t.Fatal("missing idempotency key")
	}
}
func TestCommitFailures(t *testing.T) {
	for _, status := range []int{409, 403, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, challenges := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"failure"}`))
			})
			_, err := c.Commit(context.Background(), Commit{})
			var failure *Error
			if !errors.As(err, &failure) || failure.Status != status {
				t.Fatalf("%v", err)
			}
			want := 1
			if status == 503 {
				want = 3
			}
			if *challenges != want {
				t.Fatalf("challenges=%d", *challenges)
			}
		})
	}
}
func TestChunkIntegrity(t *testing.T) {
	data := []byte("ciphertext")
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	ref := protocol.ChunkRef{ID: protocol.ChunkID(data), Size: uint64(len(data))}
	got, err := c.Chunk(context.Background(), "node", "version", ref)
	if err != nil || string(got) != string(data) {
		t.Fatalf("%s %v", got, err)
	}
	ref.Size++
	if _, err := c.Chunk(context.Background(), "node", "", ref); err == nil {
		t.Fatal("accepted wrong size")
	}
	ref.Size--
	ref.ID = protocol.ChunkID([]byte("different"))
	if _, err := c.Chunk(context.Background(), "node", "", ref); err == nil {
		t.Fatal("accepted wrong hash")
	}
}
func TestUploadAndGet(t *testing.T) {
	data := []byte("ciphertext")
	puts := 0
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Poweur-Session-Id") != "session" {
			t.Error("missing session ID")
		}
		if r.URL.Path == "/drive/alice.poweur.net/chunks/missing" {
			_, _ = w.Write([]byte(`{"missing":[{"id":"` + protocol.ChunkID(data) + `","size":` + strconv.Itoa(len(data)) + `,"upload":{"method":"PUT","url":"/drive/alice.poweur.net/chunks/` + protocol.ChunkID(data) + `"}}]}`))
			return
		}
		if r.Method == "PUT" {
			body, _ := io.ReadAll(r.Body)
			puts++
			if string(body) != string(data) || r.URL.Path != "/drive/alice.poweur.net/chunks/"+protocol.ChunkID(data) {
				t.Error("invalid upload")
			}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"quota":123}`))
	})
	c.SessionID = "session"
	ref, err := c.Upload(context.Background(), data)
	if err != nil || ref.ID != protocol.ChunkID(data) || puts != 1 {
		t.Fatalf("%+v puts=%d %v", ref, puts, err)
	}
	var info struct{ Quota int }
	if err := c.Get(context.Background(), "", &info); err != nil || info.Quota != 123 {
		t.Fatalf("%+v %v", info, err)
	}
	c.Key = nil
	if err := c.Get(context.Background(), "", &info); err == nil {
		t.Fatal("accepted invalid signing key")
	}
}
func TestStoreSkipsPresentChunks(t *testing.T) {
	data := []byte("already")
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			t.Error("uploaded a chunk the relay already has")
		}
		_, _ = w.Write([]byte(`{"missing":[]}`))
	})
	refs, err := c.Store(context.Background(), [][]byte{data})
	if err != nil || len(refs) != 1 || refs[0].ID != protocol.ChunkID(data) {
		t.Fatalf("%+v %v", refs, err)
	}
}
func TestPresignedUploadOmitsCredentials(t *testing.T) {
	data := []byte("direct")
	var sawAuth bool
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("X-Poweur-Identity") != "" || r.Header.Get("X-Poweur-Signature") != ""
		if r.Header.Get("X-Amz-Checksum") != "abc" {
			t.Error("missing provider header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(data) {
			t.Error("wrong body")
		}
		w.WriteHeader(200)
	}))
	defer bucket.Close()
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"missing":[{"id":"%s","size":%d,"upload":{"method":"PUT","url":"%s","headers":{"X-Amz-Checksum":"abc"}}}]}`, protocol.ChunkID(data), len(data), bucket.URL)
	})
	if _, err := c.Upload(context.Background(), data); err != nil || sawAuth {
		t.Fatalf("presigned upload: %v auth=%v", err, sawAuth)
	}
}
func TestSubscribe(t *testing.T) {
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": padding\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"ready\",\"identity\":\"alice.poweur.net\",\"timestamp\":\"2026-09-28T00:00:00Z\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"drive.changed\",\"identity\":\"alice.poweur.net\",\"timestamp\":\"2026-09-28T00:00:01Z\",\"drive\":{\"operation\":\"append\",\"node\":\"abc\"}}\n\n"))
	})
	var types []string
	err := c.Subscribe(context.Background(), func(event Event) error {
		types = append(types, event.Type)
		if event.Type == "drive.changed" {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil || err.Error() != "stop" || len(types) != 2 || types[1] != "drive.changed" {
		t.Fatalf("%v %v", types, err)
	}
}
func TestLinkSecret(t *testing.T) {
	fragment := bytes.Repeat([]byte{1}, 32)
	half := bytes.Repeat([]byte{2}, 32)
	got, err := LinkSecret(fragment, half)
	if err != nil || got[0] != 3 || bytes.Equal(got, fragment) {
		t.Fatalf("%v %v", got, err)
	}
	plain, err := LinkSecret(fragment, nil)
	if err != nil || !bytes.Equal(plain, fragment) || &plain[0] == &fragment[0] {
		t.Fatalf("fragment reused: %v", err)
	}
}
