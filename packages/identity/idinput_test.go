package identity

import "testing"

// idInputCases are the vectors in the bridge spec, plus the edges that made
// each rule necessary.
var idInputCases = []struct {
	input     string
	allowHTTP bool
	want      string // empty means refused
}{
	{"Alice.Example.com", false, "alice.example.com"},
	{"alice.example.com.", false, "alice.example.com"},
	{"@alice.example.com", false, "alice.example.com"},
	{"  alice.example.com  ", false, "alice.example.com"},
	{"https://alice.example.com", false, "alice.example.com"},
	{"https://alice.example.com/", false, "alice.example.com"},
	{"HTTPS://Alice.Example.com/", false, "alice.example.com"},
	{"https://alice.example.com/blog", false, ""},
	{"https://alice.example.com:8443/", false, ""},
	{"https://alice.example.com/?x=1", false, ""},
	{"https://alice.example.com/?", false, ""},
	{"https://alice.example.com/#me", false, ""},
	{"https://bob@alice.example.com/", false, ""},
	{"http://alice.example.com/", false, ""},
	{"http://alice.example.com/", true, "alice.example.com"},
	{"ftp://alice.example.com/", true, ""},
	{"alice", false, ""},
	{"", false, ""},
	{"@", false, ""},
	{"alice.example.com/blog", false, ""},
	{"alice@example.com", false, ""},
	{"аlice.example.com", false, ""}, // Cyrillic а
	{"xn--80ak6aa92e.example", false, "xn--80ak6aa92e.example"},
	{"192.168.0.1", false, ""},
	{"https://192.168.0.1/", false, ""},
	{"www.example.com", false, ""}, // reserved leftmost label; never an alias
}

func TestNormalizeIDInput(t *testing.T) {
	for _, tc := range idInputCases {
		got, err := NormalizeIDInput(tc.input, tc.allowHTTP)
		if tc.want == "" {
			if err == nil {
				t.Errorf("NormalizeIDInput(%q, %v) = %q, want refusal", tc.input, tc.allowHTTP, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("NormalizeIDInput(%q, %v) = %q, %v; want %q", tc.input, tc.allowHTTP, got, err, tc.want)
		}
	}
}

func TestIDProjections(t *testing.T) {
	if got := IndieAuthProfileURL("alice.example.com"); got != "https://alice.example.com/" {
		t.Fatalf("profile URL = %q", got)
	}
	if got := DIDWebID("alice.example.com"); got != "did:web:alice.example.com" {
		t.Fatalf("did = %q", got)
	}
	// The profile URL normalizes back to the ID it came from.
	if got, err := NormalizeIDInput(IndieAuthProfileURL("alice.example.com"), false); err != nil || got != "alice.example.com" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
}

type idInputVector struct {
	Input      string `json:"input"`
	AllowHTTP  bool   `json:"allow_http,omitempty"`
	ID         string `json:"id,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
	DID        string `json:"did,omitempty"`
	Valid      bool   `json:"valid"`
}

func TestVectors_IDInput(t *testing.T) {
	var out []idInputVector
	for _, tc := range idInputCases {
		v := idInputVector{Input: tc.input, AllowHTTP: tc.allowHTTP}
		if id, err := NormalizeIDInput(tc.input, tc.allowHTTP); err == nil {
			v.Valid, v.ID = true, id
			v.ProfileURL, v.DID = IndieAuthProfileURL(id), DIDWebID(id)
		}
		out = append(out, v)
	}
	WriteVectors(t, vectorsDir, "id-input", out)
}
