package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Group keys (EPIC-024 E24-T3): every membership change issues the next
// epoch's key, sealed to the group, its members and its admins; a share to a
// group seals to the current public key; a member opens it with the epoch
// keys they can reach.

// issueGroupKeys writes the keyring and public key for roster's epoch. The
// group's own encryption key opens the previous keyring, so earlier epochs
// carry over. It runs on the device that holds the group's keys.
func (gs groupSession) issueGroupKeys(ctx context.Context, roster idpkg.ShareGroup) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	groupEnc, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, gs.groupID))
	if err != nil {
		return fmt.Errorf("no encryption key for group %s on this device: %w", gs.groupID, err)
	}
	var earlier []idpkg.GroupEpochKey
	if raw, status, err := readSysFile(ctx, gs.relayURL, gs.groupID, gs.priv, idpkg.GroupKeysDoc); err == nil && status == http.StatusOK {
		ring, err := idpkg.ParseGroupKeyring(raw)
		if err != nil {
			return fmt.Errorf("stored group keyring: %w", err)
		}
		if ring.Epoch == roster.Epoch {
			return nil // already issued for this membership
		}
		if earlier, err = ring.Open(gs.groupID, groupEnc); err != nil {
			return err
		}
	}
	recipients := map[string][]byte{}
	groupPub, err := idpkg.X25519PublicFromPrivate(groupEnc)
	if err != nil {
		return err
	}
	recipients[gs.groupID] = groupPub
	for _, list := range [][]string{roster.Members, roster.Admins} {
		for _, id := range list {
			id = strings.ToLower(strings.TrimSpace(id))
			if _, done := recipients[id]; done {
				continue
			}
			key, err := identity.LookupEncryptionKey(ctx, id)
			if err != nil || len(key) != 32 {
				return fmt.Errorf("cannot seal the group key to %s: no encryption key (%v)", id, err)
			}
			recipients[id] = key
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ring, _, err := idpkg.NewGroupKeyring(gs.groupID, roster.Epoch, recipients, earlier, now)
	if err != nil {
		return err
	}
	if err := ring.Sign(gs.priv); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(ring, "", "  ")
	if err := writeSysFile(ctx, gs.relayURL, gs.groupID, gs.priv, idpkg.GroupKeysDoc, raw); err != nil {
		return fmt.Errorf("write the group keyring: %w", err)
	}
	doc := ring.PublicDocument()
	if err := doc.Sign(gs.priv); err != nil {
		return err
	}
	raw, _ = json.MarshalIndent(doc, "", "  ")
	if err := writeSysFile(ctx, gs.relayURL, gs.groupID, gs.priv, idpkg.GroupPublicKeyDoc, raw); err != nil {
		return fmt.Errorf("publish the group key: %w", err)
	}
	return nil
}

// fetchGroupDoc reads GET /groups/{group}{suffix} from the group's relay as
// actor (challenge-signed), for a member or admin.
func fetchGroupDoc(ctx context.Context, relayURL, group, suffix, actor string, actorPriv ed25519.PrivateKey) ([]byte, error) {
	challenge, err := FetchChallenge(ctx, relayURL, actor)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/groups/"+group+suffix, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Poweur-Identity", actor)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(actorPriv, []byte(challenge.Challenge))))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s is not a group you are in (or it has no group key yet)", group)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse("group request failed", resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
}

// groupAccess is what a member needs to open a drive shared with a group:
// the epoch keys they can reach, and the signed roster to present to a relay
// that does not host the group.
type groupAccess struct {
	Keys   [][]byte
	Roster string // base64url of the signed group.json
}

func loadGroupAccess(ctx context.Context, cfg config.Config, group, actor string, actorPriv ed25519.PrivateKey, actorEnc []byte) (groupAccess, error) {
	group = strings.ToLower(strings.TrimSpace(group))
	relay, err := identityRelayURL(ctx, cfg, group)
	if err != nil {
		return groupAccess{}, err
	}
	resolved, err := identity.ResolveIdentity(ctx, group)
	if err != nil {
		return groupAccess{}, err
	}
	groupSigning, err := idpkg.ParseEd25519PublicKey(resolved.Document.PublicKey)
	if err != nil {
		return groupAccess{}, err
	}
	rawRoster, err := fetchGroupDoc(ctx, relay, group, "", actor, actorPriv)
	if err != nil {
		return groupAccess{}, err
	}
	roster, err := idpkg.ParseShareGroup(rawRoster)
	if err != nil || !strings.EqualFold(roster.Group, group) || roster.VerifySignature(groupSigning) != nil {
		return groupAccess{}, fmt.Errorf("the roster of %s does not verify", group)
	}
	rawRing, err := fetchGroupDoc(ctx, relay, group, "/keys", actor, actorPriv)
	if err != nil {
		return groupAccess{}, err
	}
	ring, err := idpkg.ParseGroupKeyring(rawRing)
	if err != nil {
		return groupAccess{}, err
	}
	if err := ring.VerifySignature(groupSigning); err != nil || !strings.EqualFold(ring.Group, group) || ring.Epoch != roster.Epoch {
		return groupAccess{}, fmt.Errorf("the keyring of %s does not verify against its roster", group)
	}
	opened, err := ring.Open(actor, actorEnc)
	if err != nil {
		return groupAccess{}, err
	}
	access := groupAccess{Roster: base64.RawURLEncoding.EncodeToString(rawRoster)}
	for _, k := range opened {
		access.Keys = append(access.Keys, k.Private)
	}
	return access, nil
}

// shareRecipientKey is the key a share to member seals to: a group's
// current public key when member is a group, else the member's encryption
// key.
func shareRecipientKey(ctx context.Context, member string) ([]byte, error) {
	if doc, err := identity.FetchGroupPublicKey(ctx, member); err == nil {
		return base64.RawURLEncoding.DecodeString(doc.Public)
	}
	key, err := identity.LookupEncryptionKey(ctx, member)
	if err == nil && len(key) != 32 {
		err = fmt.Errorf("no encryption key for %s", member)
	}
	return key, err
}

// joinGroups lets files open drives shared with the named groups: it loads
// each group's keys for the caller and presents the group's signed roster to
// a relay that does not host the group (`--group`).
func joinGroups(files *driveclient.Files, use string, groups []string, stderr io.Writer) bool {
	if len(groups) == 0 {
		return true
	}
	cfg, name, key, ok := loadIdentityKey(use, stderr)
	if !ok {
		return false
	}
	enc, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, name))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	if files.GroupKeys == nil {
		files.GroupKeys = map[string][][]byte{}
	}
	if files.Client.Headers == nil {
		files.Client.Headers = map[string]string{}
	}
	for _, group := range groups {
		access, err := loadGroupAccess(context.Background(), cfg, group, name, key, enc)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return false
		}
		files.GroupKeys[strings.ToLower(group)] = access.Keys
		// The relay verifies one roster per request; name one group at a time.
		files.Client.Headers["X-Poweur-Group-Roster"] = access.Roster
	}
	return true
}
