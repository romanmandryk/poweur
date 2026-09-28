package integration_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The temporary adapter must preserve conditional writes across real clients.
// Keep this scenario when SystemFiles is wired to the v2 drive.
func TestINT_SYSTEMFILES_ConcurrentEdit(t *testing.T) {
	zone := newZone(t)
	relay, _ := newHostedRelay(t, zone, t.TempDir())
	defer relay.Close()
	const owner = "conditional.poweur.net"
	home := t.TempDir()
	runCLI(t, home, "identity", "create", owner, "--hosted", "--relay", relay.URL, "--json")
	key := loadIdentityKey(t, home, owner)
	request := func(body, etag string) *http.Request {
		t.Helper()
		resp, err := http.Get(relay.URL + "/auth/challenge?identity=" + owner)
		if err != nil {
			t.Fatal(err)
		}
		var challenge struct {
			Challenge string `json:"challenge"`
		}
		err = json.NewDecoder(resp.Body).Decode(&challenge)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("challenge: %d %v", resp.StatusCode, err)
		}
		req, err := http.NewRequest(http.MethodPut, relay.URL+"/identities/"+owner+"/system/.poweur/public/profile.json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Poweur-Identity", owner)
		req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
		req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(challenge.Challenge))))
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		return req
	}
	resp, err := http.DefaultClient.Do(request(`{"version":1,"display_name":"base"}`, ""))
	if err != nil {
		t.Fatal(err)
	}
	tag := resp.Header.Get("ETag")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || tag == "" {
		t.Fatalf("initial write: %d", resp.StatusCode)
	}
	requests := []*http.Request{}
	for i := 0; i < 8; i++ {
		requests = append(requests, request(fmt.Sprintf(`{"version":1,"display_name":"client %d"}`, i), tag))
	}
	start := make(chan struct{})
	results := make(chan int, len(requests))
	var wg sync.WaitGroup
	for _, req := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for code := range results {
		if code == 200 {
			wins++
		} else if code == 412 {
			conflicts++
		} else {
			t.Errorf("unexpected response %d", code)
		}
	}
	if wins != 1 || conflicts != len(requests)-1 {
		t.Fatalf("wins %d conflicts %d", wins, conflicts)
	}
}
