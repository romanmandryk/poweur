package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// Group keys (EPIC-024 E24-T3). A group identity gets one X25519 key per
// membership epoch. The keyring seals that epoch's private key to every
// member and admin (and to the group itself, so whoever holds the group's
// keys can always open it), and seals every earlier epoch's private key to
// the current public key, so a current member can open shares issued at any
// epoch. A share to a group seals its node key to the current public key.

const (
	// GroupKeysDoc is the keyring, in the group's own drive. The relay sees
	// only sealed keys; members read it through GET /groups/{group}/keys.
	GroupKeysDoc = ".poweur/relay/group-keys.json"
	// GroupPublicKeyDoc is the current public key, readable by anyone at
	// https://<group>/.well-known/poweur/group-key.json, so a person outside
	// the group can share with it.
	GroupPublicKeyDoc = ".poweur/public/group-key.json"
	// GroupKeyDomain separates group-key seals from every other seal.
	GroupKeyDomain = "poweur/group/key/v1"
	// MaxGroupKeyEpochs bounds the retained history of earlier epochs.
	MaxGroupKeyEpochs = 128
)

// GroupPreviousKey is an earlier epoch's key, its private half sealed to the
// current epoch's public key.
type GroupPreviousKey struct {
	Epoch  int           `json:"epoch"`
	Public string        `json:"public"`
	Sealed SealedPayload `json:"sealed"`
}

// GroupKeyring is GroupKeysDoc.
type GroupKeyring struct {
	Version  int                      `json:"version"`
	Group    string                   `json:"group"`
	Epoch    int                      `json:"epoch"`
	Public   string                   `json:"public"`
	Sealed   map[string]SealedPayload `json:"sealed"`
	Previous []GroupPreviousKey       `json:"previous,omitempty"`
	// UpdatedAt is RFC3339.
	UpdatedAt string `json:"updated_at"`
	Signature string `json:"signature"`
}

// GroupPublicKey is GroupPublicKeyDoc.
type GroupPublicKey struct {
	Version   int    `json:"version"`
	Group     string `json:"group"`
	Epoch     int    `json:"epoch"`
	Public    string `json:"public"`
	UpdatedAt string `json:"updated_at"`
	Signature string `json:"signature"`
}

// GroupEpochKey is one opened epoch key.
type GroupEpochKey struct {
	Epoch   int
	Public  []byte
	Private []byte
}

func groupMemberSealContext(group string, epoch int, recipient string) []byte {
	return []byte("member\n" + strings.ToLower(group) + "\n" + strconv.Itoa(epoch) + "\n" + strings.ToLower(recipient))
}

func groupPreviousSealContext(group string, epoch, current int) []byte {
	return []byte("previous\n" + strings.ToLower(group) + "\n" + strconv.Itoa(epoch) + "\n" + strconv.Itoa(current))
}

func sealedLine(p SealedPayload) string {
	return p.Ciphertext + "\t" + p.EphemeralPublicKey + "\t" + p.Nonce
}

// Canonical is the string the group signs.
func (k GroupKeyring) Canonical() string {
	recipients := make([]string, 0, len(k.Sealed))
	for r := range k.Sealed {
		recipients = append(recipients, r)
	}
	sort.Strings(recipients)
	lines := []string{
		"poweur-group-keys", strconv.Itoa(k.Version), strings.ToLower(k.Group), strconv.Itoa(k.Epoch), k.Public,
		strconv.Itoa(len(recipients)),
	}
	for _, r := range recipients {
		lines = append(lines, r+"\t"+sealedLine(k.Sealed[r]))
	}
	lines = append(lines, strconv.Itoa(len(k.Previous)))
	for _, p := range k.Previous {
		lines = append(lines, strconv.Itoa(p.Epoch)+"\t"+p.Public+"\t"+sealedLine(p.Sealed))
	}
	return strings.Join(append(lines, k.UpdatedAt), "\n")
}

// Canonical is the string the group signs.
func (k GroupPublicKey) Canonical() string {
	return strings.Join([]string{"poweur-group-key", strconv.Itoa(k.Version), strings.ToLower(k.Group), strconv.Itoa(k.Epoch), k.Public, k.UpdatedAt}, "\n")
}

func decodeKey32(value, what string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s must be a base64url 32-byte key", what)
	}
	return raw, nil
}

// Validate checks structure (not the signature).
func (k GroupKeyring) Validate() error {
	if k.Version != 1 {
		return errors.New("group keyring version must be 1")
	}
	if !IsGroupIdentityName(k.Group) || k.Group != strings.ToLower(k.Group) {
		return errors.New("group keyring must name a lowercase group identity")
	}
	if k.Epoch < 1 {
		return errors.New("group keyring epoch must be at least 1")
	}
	if _, err := decodeKey32(k.Public, "public"); err != nil {
		return err
	}
	if len(k.Sealed) == 0 || len(k.Sealed) > 2*MaxGroupMembers+1 {
		return errors.New("group keyring must seal the key to its members")
	}
	for r := range k.Sealed {
		if r != strings.ToLower(strings.TrimSpace(r)) || !IsGroupIdentityName(r) {
			return fmt.Errorf("group keyring recipient %q is not a lowercase Poweur ID", r)
		}
	}
	if len(k.Previous) > MaxGroupKeyEpochs {
		return fmt.Errorf("group keyring keeps at most %d earlier epochs", MaxGroupKeyEpochs)
	}
	last := k.Epoch
	for _, p := range k.Previous {
		if p.Epoch < 1 || p.Epoch >= last {
			return errors.New("earlier epochs must be listed newest first, below the current epoch")
		}
		if _, err := decodeKey32(p.Public, "previous public"); err != nil {
			return err
		}
		last = p.Epoch
	}
	return nil
}

// Validate checks structure (not the signature).
func (k GroupPublicKey) Validate() error {
	if k.Version != 1 || !IsGroupIdentityName(k.Group) || k.Group != strings.ToLower(k.Group) || k.Epoch < 1 {
		return errors.New("invalid group public key document")
	}
	_, err := decodeKey32(k.Public, "public")
	return err
}

// Sign fills Signature with the group identity's key.
func (k *GroupKeyring) Sign(priv ed25519.PrivateKey) error {
	if err := k.Validate(); err != nil {
		return err
	}
	k.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(k.Canonical())))
	return nil
}

// Sign fills Signature with the group identity's key.
func (k *GroupPublicKey) Sign(priv ed25519.PrivateKey) error {
	if err := k.Validate(); err != nil {
		return err
	}
	k.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(k.Canonical())))
	return nil
}

func verifyGroupSignature(pub ed25519.PublicKey, canonical, signature string) error {
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(pub, []byte(canonical), sig) {
		return errors.New("group key signature verification failed")
	}
	return nil
}

// VerifySignature checks the group's signature.
func (k GroupKeyring) VerifySignature(pub ed25519.PublicKey) error {
	return verifyGroupSignature(pub, k.Canonical(), k.Signature)
}

// VerifySignature checks the group's signature.
func (k GroupPublicKey) VerifySignature(pub ed25519.PublicKey) error {
	return verifyGroupSignature(pub, k.Canonical(), k.Signature)
}

// ParseGroupKeyring decodes and validates a signed keyring.
func ParseGroupKeyring(raw []byte) (GroupKeyring, error) {
	var k GroupKeyring
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("invalid group keyring: %w", err)
	}
	if err := k.Validate(); err != nil {
		return k, err
	}
	if k.Signature == "" {
		return k, errors.New("group keyring is unsigned")
	}
	return k, nil
}

// ParseGroupPublicKey decodes and validates a signed public key document.
func ParseGroupPublicKey(raw []byte) (GroupPublicKey, error) {
	var k GroupPublicKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("invalid group public key: %w", err)
	}
	if err := k.Validate(); err != nil {
		return k, err
	}
	if k.Signature == "" {
		return k, errors.New("group public key is unsigned")
	}
	return k, nil
}

// PublicDocument is the public key document matching this keyring.
func (k GroupKeyring) PublicDocument() GroupPublicKey {
	return GroupPublicKey{Version: 1, Group: k.Group, Epoch: k.Epoch, Public: k.Public, UpdatedAt: k.UpdatedAt}
}

// Recipients lists who the current epoch key is sealed to.
func (k GroupKeyring) Recipients() []string {
	out := make([]string, 0, len(k.Sealed))
	for r := range k.Sealed {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// NewGroupKeyring issues the key for epoch, sealed to every recipient
// (identity → X25519 encryption public key). earlier are the keys of all
// previous epochs (any order); they are re-sealed to the new public key.
// Call Sign on the result.
func NewGroupKeyring(group string, epoch int, recipients map[string][]byte, earlier []GroupEpochKey, updatedAt string) (GroupKeyring, GroupEpochKey, error) {
	group = strings.ToLower(strings.TrimSpace(group))
	public, private, err := GenerateX25519Keypair()
	if err != nil {
		return GroupKeyring{}, GroupEpochKey{}, err
	}
	k := GroupKeyring{Version: 1, Group: group, Epoch: epoch, Public: base64.RawURLEncoding.EncodeToString(public), Sealed: map[string]SealedPayload{}, UpdatedAt: updatedAt}
	for id, encPub := range recipients {
		id = strings.ToLower(strings.TrimSpace(id))
		sealed, err := Seal(encPub, private, GroupKeyDomain, groupMemberSealContext(group, epoch, id))
		if err != nil {
			return GroupKeyring{}, GroupEpochKey{}, fmt.Errorf("seal the group key to %s: %w", id, err)
		}
		k.Sealed[id] = sealed
	}
	sort.Slice(earlier, func(i, j int) bool { return earlier[i].Epoch > earlier[j].Epoch })
	for _, e := range earlier {
		if e.Epoch >= epoch || len(k.Previous) == MaxGroupKeyEpochs {
			continue
		}
		sealed, err := Seal(public, e.Private, GroupKeyDomain, groupPreviousSealContext(group, e.Epoch, epoch))
		if err != nil {
			return GroupKeyring{}, GroupEpochKey{}, err
		}
		k.Previous = append(k.Previous, GroupPreviousKey{Epoch: e.Epoch, Public: base64.RawURLEncoding.EncodeToString(e.Public), Sealed: sealed})
	}
	return k, GroupEpochKey{Epoch: epoch, Public: public, Private: private}, k.Validate()
}

// Open returns every epoch key the recipient can reach, newest first: the
// current key sealed to them, then each earlier key sealed to it.
func (k GroupKeyring) Open(recipient string, encryptionPrivateKey []byte) ([]GroupEpochKey, error) {
	sealed, ok := k.Sealed[strings.ToLower(strings.TrimSpace(recipient))]
	if !ok {
		return nil, fmt.Errorf("%s holds no key for group %s at epoch %d", recipient, k.Group, k.Epoch)
	}
	private, err := OpenSeal(encryptionPrivateKey, sealed, GroupKeyDomain, groupMemberSealContext(k.Group, k.Epoch, recipient))
	if err != nil {
		return nil, fmt.Errorf("open the group key: %w", err)
	}
	public, _ := decodeKey32(k.Public, "public")
	if derived, err := X25519PublicFromPrivate(private); err != nil || string(derived) != string(public) {
		return nil, errors.New("the group key does not match its public key")
	}
	out := []GroupEpochKey{{Epoch: k.Epoch, Public: public, Private: private}}
	for _, p := range k.Previous {
		prev, err := OpenSeal(private, p.Sealed, GroupKeyDomain, groupPreviousSealContext(k.Group, p.Epoch, k.Epoch))
		if err != nil {
			return nil, fmt.Errorf("open the key of epoch %d: %w", p.Epoch, err)
		}
		pub, _ := decodeKey32(p.Public, "previous public")
		if derived, err := X25519PublicFromPrivate(prev); err != nil || string(derived) != string(pub) {
			return nil, fmt.Errorf("the key of epoch %d does not match its public key", p.Epoch)
		}
		out = append(out, GroupEpochKey{Epoch: p.Epoch, Public: pub, Private: prev})
	}
	return out, nil
}

// X25519PublicFromPrivate derives an X25519 public key.
func X25519PublicFromPrivate(private []byte) ([]byte, error) {
	return curve25519.X25519(private, curve25519.Basepoint)
}

// WellKnownGroupKeyPath serves GroupPublicKeyDoc from the group's own host.
const WellKnownGroupKeyPath = "/.well-known/poweur/group-key.json"

// FetchGroupPublicKey reads a group's current public key from its own host
// and checks the group's signature on it. ErrPublicFileAbsent means the
// identity is not a group (or has no group key yet).
func FetchGroupPublicKey(ctx context.Context, group string, opts ResolveOptions) (GroupPublicKey, error) {
	raw, err := FetchPublicFile(ctx, group, WellKnownGroupKeyPath, opts)
	if err != nil {
		return GroupPublicKey{}, err
	}
	doc, err := ParseGroupPublicKey(raw)
	if err != nil {
		return GroupPublicKey{}, err
	}
	if !strings.EqualFold(doc.Group, group) {
		return GroupPublicKey{}, errors.New("group key names another group")
	}
	res, err := Resolve(ctx, group, opts)
	if err != nil {
		return GroupPublicKey{}, err
	}
	pub, err := ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		return GroupPublicKey{}, err
	}
	return doc, doc.VerifySignature(pub)
}
