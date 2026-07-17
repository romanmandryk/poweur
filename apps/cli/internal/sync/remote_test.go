package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeUploadServer implements just enough of the relay's DAV PUT + chunked
// upload endpoints to exercise HTTPRemote's client logic.
type fakeUploadServer struct {
	davPuts  map[string][]byte
	uploads  map[string]*fakeUpload
	assembly map[string][]byte
}

type fakeUpload struct {
	path   string
	length int64
	buf    bytes.Buffer
}

func newFakeUploadServer() *fakeUploadServer {
	return &fakeUploadServer{
		davPuts:  map[string][]byte{},
		uploads:  map[string]*fakeUpload{},
		assembly: map[string][]byte{},
	}
}

func (s *fakeUploadServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /dav/alice.poweur.net/{path...}", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.davPuts[r.PathValue("path")] = raw
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /sync/alice.poweur.net/upload", func(w http.ResponseWriter, r *http.Request) {
		length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
		if err != nil {
			t.Errorf("missing Upload-Length")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := fmt.Sprintf("up%d", len(s.uploads)+1)
		s.uploads[id] = &fakeUpload{path: strings.TrimPrefix(r.URL.Query().Get("path"), "/"), length: length}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("PATCH /sync/alice.poweur.net/upload/{id}", func(w http.ResponseWriter, r *http.Request) {
		up := s.uploads[r.PathValue("id")]
		if up == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if offset != int64(up.buf.Len()) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = io.Copy(&up.buf, r.Body)
		if int64(up.buf.Len()) >= up.length {
			s.assembly[up.path] = up.buf.Bytes()
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func TestHTTPRemotePutUsesChunkedAboveThreshold(t *testing.T) {
	srv := newFakeUploadServer()
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()
	remote := &HTTPRemote{
		RelayURL: ts.URL, Identity: "alice.poweur.net", Token: "t",
		ChunkThreshold: 64, ChunkSize: 100,
	}
	body := bytes.Repeat([]byte("z"), 250) // 3 chunks of 100/100/50

	if err := remote.Put(context.Background(), "private/big.bin", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srv.assembly["private/big.bin"], body) {
		t.Fatalf("chunked upload must assemble the full body (%d bytes)", len(srv.assembly["private/big.bin"]))
	}
	if len(srv.davPuts) != 0 {
		t.Fatal("large file must not use plain DAV PUT")
	}

	small := []byte("small")
	if err := remote.Put(context.Background(), "private/small.txt", bytes.NewReader(small), int64(len(small))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srv.davPuts["private/small.txt"], small) {
		t.Fatal("small file must use plain DAV PUT")
	}
}
