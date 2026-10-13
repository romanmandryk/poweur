package signin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"github.com/poweur/identity"
)

// Delivery is what a signer POSTs to a request's response_uri.
//
// Response is the encoded, signed approval. Finish is FinishPoll when the
// user approved on a different device from the one that started the sign-in
// (a QR, a link, a pushed prompt): the starting page's poll completes it.
// Match is the number the user picked from MatchChoices when the sign-in was
// pushed to them (EPIC-028). Neither is part of the signed bytes.
//
// Older signers sent a cross-device approval as a Match alone, with no
// Finish; CrossDevice treats both forms alike.
type Delivery struct {
	Response string `json:"response"`
	Finish   string `json:"finish,omitempty"`
	Match    string `json:"match,omitempty"`
}

// FinishPoll marks a cross-device delivery.
const FinishPoll = "poll"

// CrossDevice reports whether the approval was made on another device.
func (d Delivery) CrossDevice() bool {
	return d.Finish == FinishPoll || d.Match != ""
}

// DeliveryReceipt is the relying party's answer to a Delivery.
//
// ResumeURI is set for a same-device approval: the signer navigates the
// browser there and the RP completes the sign-in only if that browser also
// holds the transaction cookie set when the sign-in started. A signer MUST
// NOT put the approval itself in a URL instead.
type DeliveryReceipt struct {
	Status    string `json:"status"`
	Identity  string `json:"identity,omitempty"`
	ResumeURI string `json:"resume_uri,omitempty"`
}

// MaxDeliveryBytes caps a delivery body. An approval with a session proof is
// well under 4 KiB.
const MaxDeliveryBytes = 64 * 1024

// MatchCodeDigits is the length of a match code. It is an attention check,
// not a secret, and since EPIC-028 it is asked only for a sign-in that was
// pushed to the user's app: the approver picks it from MatchChoiceCount
// numbers. A relying party allows exactly one pick per sign-in, so a guess
// succeeds with probability 1/MatchChoiceCount and a miss kills the
// transaction.
const MatchCodeDigits = 2

// MatchChoiceCount is how many numbers a pushed prompt offers.
const MatchChoiceCount = 3

// ErrMatchCode is returned when a cross-device delivery carries a code that
// does not match the one the initiating screen showed.
var ErrMatchCode = errors.New("sign-in: the code does not match the screen that started this sign-in")

// ParseDelivery reads a delivery in any of the shapes signers send: a JSON
// envelope {"response": …, "match": …} (under any content type, since a
// browser signer uses text/plain to stay a CORS-simple request), a form, a
// bare response object, or the encoded response as the whole body.
func ParseDelivery(r *http.Request) (Delivery, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(nil, r.Body, MaxDeliveryBytes)
		if err := r.ParseForm(); err != nil {
			return Delivery{}, errors.New("malformed form body")
		}
		d := Delivery{
			Response: strings.TrimSpace(r.PostForm.Get("response")),
			Finish:   normalizeFinish(r.PostForm.Get("finish")),
			Match:    NormalizeMatchCode(r.PostForm.Get("match")),
		}
		if d.Response == "" {
			return Delivery{}, errors.New("missing response field")
		}
		return d, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxDeliveryBytes+1))
	if err != nil {
		return Delivery{}, fmt.Errorf("read body: %w", err)
	}
	if len(raw) > MaxDeliveryBytes {
		return Delivery{}, errors.New("delivery body too large")
	}
	return ParseDeliveryBody(string(raw))
}

// ParseDeliveryBody is ParseDelivery for a body already in hand.
func ParseDeliveryBody(body string) (Delivery, error) {
	raw := strings.TrimSpace(body)
	if raw == "" {
		return Delivery{}, errors.New("empty body")
	}
	if !strings.HasPrefix(raw, "{") {
		return Delivery{Response: raw}, nil
	}
	var envelope struct {
		Response json.RawMessage `json:"response"`
		Finish   string          `json:"finish"`
		Match    json.RawMessage `json:"match"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil || len(envelope.Response) == 0 {
		// A bare response object.
		return Delivery{Response: raw}, nil
	}
	d := Delivery{Finish: normalizeFinish(envelope.Finish)}
	var asString string
	if err := json.Unmarshal(envelope.Response, &asString); err == nil {
		d.Response = strings.TrimSpace(asString)
	} else {
		d.Response = string(envelope.Response)
	}
	if len(envelope.Match) > 0 {
		var match string
		if err := json.Unmarshal(envelope.Match, &match); err != nil {
			return Delivery{}, errors.New("match must be a string")
		}
		d.Match = NormalizeMatchCode(match)
	}
	if d.Response == "" {
		return Delivery{}, errors.New("missing response field")
	}
	return d, nil
}

// normalizeFinish keeps the one value a signer may send; anything else is the
// same-device default.
func normalizeFinish(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), FinishPoll) {
		return FinishPoll
	}
	return ""
}

// NewMatchChoices returns a fresh match code and MatchChoiceCount distinct
// codes that include it, in random order. The relying party shows the code on
// the starting screen and gives the signer only the choices.
func NewMatchChoices() (code string, choices []string, err error) {
	seen := map[string]bool{}
	for len(choices) < MatchChoiceCount {
		c, err := NewMatchCode()
		if err != nil {
			return "", nil, err
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		choices = append(choices, c)
	}
	// choices are independent uniform draws, so any position is as good as a
	// shuffle; pick which one is right uniformly too.
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(choices))))
	if err != nil {
		return "", nil, err
	}
	return choices[n.Int64()], choices, nil
}

// NewMatchCode returns a fresh MatchCodeDigits-digit code, zero-padded.
func NewMatchCode() (string, error) {
	limit := big.NewInt(1)
	for i := 0; i < MatchCodeDigits; i++ {
		limit.Mul(limit, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", MatchCodeDigits, n.Int64()), nil
}

// NormalizeMatchCode strips what a person types around digits.
func NormalizeMatchCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MatchCodesEqual compares two codes in constant time. An empty presented
// code never matches.
func MatchCodesEqual(want, presented string) bool {
	presented = NormalizeMatchCode(presented)
	if want == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}

// NewSecret returns 32 random bytes, base64url. Used for poll handles, resume
// codes and browser bindings — every value whose possession is the proof.
func NewSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashSecret is what a relying party stores instead of a NewSecret value.
// A fast hash is right here: the input has 256 bits of entropy.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SecretMatches reports whether presented hashes to storedHash, in constant
// time. An empty value on either side never matches.
func SecretMatches(storedHash, presented string) bool {
	if storedHash == "" || presented == "" {
		return false
	}
	got := HashSecret(presented)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(got)) == 1
}

// CheckResumeURI is the signer-side check on a receipt: the RP may only send
// the browser back to its own origin, over the same scheme, and never with a
// fragment or credentials. A signer that follows an off-origin resume_uri
// would let a compromised callback redirect the user anywhere.
func CheckResumeURI(audience, resumeURI string) error {
	resumeURI = strings.TrimSpace(resumeURI)
	if resumeURI == "" {
		return nil
	}
	u, err := url.Parse(resumeURI)
	if err != nil {
		return fmt.Errorf("%w: resume_uri is not a URL", identity.ErrSignInAudience)
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: resume_uri must not carry credentials or a fragment", identity.ErrSignInAudience)
	}
	if !identity.SameOrigin(audience, resumeURI) {
		return fmt.Errorf("%w: resume_uri %q is not same-origin with %s", identity.ErrSignInAudience, resumeURI, audience)
	}
	return nil
}
