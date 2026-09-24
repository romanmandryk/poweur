package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	gopath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	identitycli "github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Share management (EPIC-005 E05-T3, CLI slice): grants are signed
// documents the owner writes into their own tree at
// poweur-sys/relay/shares/<share-id>.json; groups at
// poweur-sys/relay/groups/<name>.json. The relay enforces them; revocation
// is deleting the file. Direct recipients are notified with encrypted
// sys.share.* lifecycle messages; accepting materializes a credential-free
// mount pointer in the recipient's tree.

const (
	sharesTreeDir = "poweur-sys/relay/shares"
	groupsTreeDir = "poweur-sys/relay/groups"
)

func runShare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur share <add|accept|claim|ls|revoke|group|link|request>")
		return 1
	}
	switch args[0] {
	case "add":
		return runShareAdd(args[1:], stdout, stderr)
	case "accept":
		return runShareAccept(args[1:], stdout, stderr)
	case "claim":
		return runShareClaim(args[1:], stdout, stderr)
	case "ls":
		return runShareLs(args[1:], stdout, stderr)
	case "revoke":
		return runShareRevoke(args[1:], stdout, stderr)
	case "group":
		return runShareGroup(args[1:], stdout, stderr)
	case "link":
		return runShareLink(args[1:], stdout, stderr)
	case "request":
		return runShareRequest(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown share subcommand (want add, accept, claim, ls, revoke, group, link, request)")
		return 1
	}
}

// runShareRequest creates an upload-only capability URL. It deliberately
// has its own command instead of overloading `link add`: a download link and
// a drop box have opposite authority and should never be confused in a
// shell history or script.
func runShareRequest(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stderr, "usage: poweur share request add <folder> [--password ... | --password-stdin] [--expires ...] [--max-uploads N] [--max-bytes N] [--max-object-bytes N] [--allow-type type] [--notify] [--json]")
		return 1
	}
	fs := flag.NewFlagSet("share request add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to request files as")
	password := fs.String("password", "", "password for the request")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	expires := fs.String("expires", "", "expiry (RFC3339), empty = never")
	maxUploads := fs.Int("max-uploads", 0, "maximum accepted files (0 = unlimited)")
	maxBytes := fs.Int64("max-bytes", 0, "maximum aggregate bytes (0 = unlimited)")
	maxObjectBytes := fs.Int64("max-object-bytes", 0, "maximum bytes per file (0 = relay default)")
	var allowedTypes stringList
	fs.Var(&allowedTypes, "allow-type", "accepted media type, e.g. image/* (repeatable)")
	notify := fs.Bool("notify", false, "record that owner notification is requested")
	jsonOut := fs.Bool("json", false, "output json")
	bools := map[string]bool{"--json": true, "--password-stdin": true, "--notify": true}
	if err := fs.Parse(normalizeArgs(args[1:], bools)); err != nil {
		return 1
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(stderr, "share request add requires one destination folder")
		return 1
	}
	path, err := idpkg.NormalizeGrantPath(fs.Args()[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *expires != "" {
		if _, err := time.Parse(time.RFC3339, *expires); err != nil {
			fmt.Fprintf(stderr, "invalid --expires: %v\n", err)
			return 1
		}
	}
	if *passwordStdin {
		if *password != "" {
			fmt.Fprintln(stderr, "use either --password or --password-stdin, not both")
			return 1
		}
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*password = strings.TrimRight(string(raw), "\r\n")
		if *password == "" {
			fmt.Fprintln(stderr, "--password-stdin got an empty password")
			return 1
		}
	}
	request := &idpkg.ShareFileRequest{
		MaxUploads: *maxUploads, MaxBytes: *maxBytes, MaxObjectBytes: *maxObjectBytes,
		AllowedTypes: []string(allowedTypes), Notify: *notify,
	}
	link := &idpkg.ShareLink{FileRequest: request}
	if *password != "" {
		link.Password, err = idpkg.HashLinkPassword(*password)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	token, err := idpkg.GenerateLinkToken()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	shareID, err := newShareID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	grant := idpkg.ShareGrant{
		ShareID: shareID, Owner: identityValue, Path: path,
		Audience: []idpkg.ShareAudience{{Link: token}}, Permissions: []string{idpkg.PermCreate},
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: *expires, Link: link,
	}
	if err := grant.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, _ := json.MarshalIndent(grant, "", "  ")
	ctx := context.Background()
	davToken, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := davPutBytes(ctx, cfg.RelayURL, identityValue, davToken.Token, sharesTreeDir+"/"+shareID+".json", raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := map[string]any{
		"share_id": shareID, "path": path, "url": shareLinkURL(identityValue, token),
		"token": token, "expires_at": *expires, "max_uploads": *maxUploads,
		"max_bytes": *maxBytes, "max_object_bytes": *maxObjectBytes,
	}
	text := fmt.Sprintf("file request %s created\n  %s\n  destination: /%s\nGuests can upload new files but cannot list, read, replace, or delete files.\nRevoke it with: poweur share revoke %s\n",
		shareID, shareLinkURL(identityValue, token), path, shareID)
	return writeOutput(stdout, *jsonOut, result, text)
}

// runShareClaim covers both sides of the guest-to-ID handoff. Request sends
// the encrypted proof-of-continuity gesture; approve verifies the still-live
// capability and creates a fresh direct grant only after owner consent.
func runShareClaim(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur share claim <request|approve>")
		return 1
	}
	switch args[0] {
	case "request":
		return runShareClaimRequest(args[1:], stdout, stderr)
	case "approve":
		return runShareClaimApprove(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown share claim subcommand (want request, approve)")
		return 1
	}
}

func runShareClaimRequest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share claim request", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity claiming the share")
	shareID := fs.String("share-id", "", "public request share id")
	token := fs.String("token", "", "public request capability token")
	tokenFile := fs.String("token-file", "", "read the capability token from a file ('-' reads stdin)")
	action := fs.String("action", "viewed", "completed action: viewed | downloaded | uploaded")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur share claim request <owner> --share-id <id> (--token <token> | --token-file <file|->) [--action viewed|downloaded|uploaded]")
		return 1
	}
	if *token != "" && *tokenFile != "" {
		fmt.Fprintln(stderr, "use either --token or --token-file, not both")
		return 1
	}
	tokenValue := strings.TrimSpace(*token)
	if *tokenFile != "" {
		raw, err := readShareLifecycleFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tokenValue = strings.TrimSpace(string(raw))
	}
	// Reject malformed public inputs before touching local identity state.
	probe := idpkg.ShareClaim{
		Version: idpkg.ShareLifecycleVersion, ShareID: strings.TrimSpace(*shareID),
		Owner: strings.ToLower(strings.TrimSpace(fs.Arg(0))), Token: strings.ToLower(tokenValue),
		Claimant: "claimant.example.org", Action: strings.ToLower(strings.TrimSpace(*action)),
		ClaimedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := probe.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, claimant, _, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	claim := idpkg.ShareClaim{
		Version: idpkg.ShareLifecycleVersion, ShareID: strings.TrimSpace(*shareID),
		Owner: strings.ToLower(strings.TrimSpace(fs.Arg(0))), Token: strings.ToLower(tokenValue),
		Claimant: claimant, Action: strings.ToLower(strings.TrimSpace(*action)),
		ClaimedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := claim.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	payload, _ := json.Marshal(claim)
	if code := sendShareLifecycle(claimant, claim.Owner, idpkg.MsgTypeShareClaim, claim.ShareID,
		time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339), payload, stderr); code != 0 {
		return code
	}
	return writeOutput(stdout, *jsonOut, claim,
		fmt.Sprintf("share claim sent to %s for %s; the owner must approve it\n", claim.Owner, claim.ShareID))
}

func runShareClaimApprove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share claim approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity owning the request")
	claimFile := fs.String("claim-file", "", "decrypted ShareClaim JSON file ('-' reads stdin)")
	perm := fs.String("perm", "rw", "new direct permissions: read | rw")
	consume := fs.Bool("consume-link", false, "revoke the public capability after the offer is delivered")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--consume-link": true})); err != nil {
		return 1
	}
	if fs.NArg() != 0 || strings.TrimSpace(*claimFile) == "" {
		fmt.Fprintln(stderr, "usage: poweur share claim approve --claim-file <file|-> [--perm read|rw] [--consume-link]")
		return 1
	}
	raw, err := readShareLifecycleFile(*claimFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	claim, err := idpkg.ParseShareClaim(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, owner, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	if !strings.EqualFold(claim.Owner, owner) {
		fmt.Fprintf(stderr, "claim owner %s does not match %s\n", claim.Owner, owner)
		return 1
	}
	permissions, ok := directSharePermissions(*perm)
	if !ok {
		fmt.Fprintln(stderr, "--perm must be read or rw")
		return 1
	}
	ctx := context.Background()
	davToken, err := MintDAVToken(ctx, cfg.RelayURL, owner, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sourceRaw, status, err := davGetBytes(ctx, cfg.RelayURL, owner, davToken.Token, sharesTreeDir+"/"+claim.ShareID+".json")
	if err != nil || status != http.StatusOK {
		fmt.Fprintln(stderr, "source capability is missing or revoked")
		return 1
	}
	source, err := idpkg.ParseShareGrant(sourceRaw)
	if err != nil || !source.IsFileRequest() || !source.MatchesLinkToken(claim.Token) || source.Expired(time.Now()) {
		fmt.Fprintln(stderr, "source capability is invalid, expired, or does not match the claim")
		return 1
	}
	if err := source.VerifySignature(priv.Public().(ed25519.PublicKey)); err != nil {
		fmt.Fprintln(stderr, "source capability signature is invalid")
		return 1
	}
	newID, err := newShareID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	grant := idpkg.ShareGrant{
		ShareID: newID, SourceShareID: source.ShareID, Owner: owner, Path: source.Path,
		Audience: []idpkg.ShareAudience{{ID: claim.Claimant}}, Permissions: permissions,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: source.ExpiresAt,
	}
	if err := grant.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	grantRaw, _ := json.MarshalIndent(grant, "", "  ")
	if err := davPutBytes(ctx, cfg.RelayURL, owner, davToken.Token, sharesTreeDir+"/"+newID+".json", grantRaw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	offer := idpkg.ShareOffer{Version: idpkg.ShareLifecycleVersion, Grant: grant, OfferedAt: time.Now().UTC().Format(time.RFC3339)}
	offerRaw, _ := json.Marshal(offer)
	offerExpiry := time.Now().UTC().Add(7 * 24 * time.Hour)
	if grant.ExpiresAt != "" {
		if expiry, parseErr := time.Parse(time.RFC3339, grant.ExpiresAt); parseErr == nil && expiry.Before(offerExpiry) {
			offerExpiry = expiry
		}
	}
	if code := sendShareLifecycle(owner, claim.Claimant, idpkg.MsgTypeShareOffer, newID,
		offerExpiry.Format(time.RFC3339), offerRaw, stderr); code != 0 {
		fmt.Fprintf(stderr, "direct grant %s was created, but its offer was not delivered; public link retained\n", newID)
		return code
	}
	linkRevoked := false
	if *consume {
		status, err = davDeleteFile(ctx, cfg.RelayURL, owner, davToken.Token, sharesTreeDir+"/"+source.ShareID+".json")
		if err != nil || (status != http.StatusNoContent && status != http.StatusOK) {
			fmt.Fprintf(stderr, "direct grant was offered, but public link revocation failed (HTTP %d)\n", status)
			return 1
		}
		linkRevoked = true
	}
	result := map[string]any{"grant": grant, "offer": offer, "link_revoked": linkRevoked}
	return writeOutput(stdout, *jsonOut, result,
		fmt.Sprintf("claim approved for %s; direct share %s offered (public link revoked: %t)\n", claim.Claimant, newID, linkRevoked))
}

// runShareAccept verifies an owner-signed offer, writes a credential-free
// mount pointer into the recipient tree, then best-effort acknowledges it.
func runShareAccept(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share accept", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity accepting the offer")
	offerFile := fs.String("offer-file", "", "decrypted ShareOffer JSON file ('-' reads stdin)")
	name := fs.String("name", "", "local mount name (default: source basename)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() != 0 || strings.TrimSpace(*offerFile) == "" {
		fmt.Fprintln(stderr, "usage: poweur share accept --offer-file <file|-> [--name <mount-name>]")
		return 1
	}
	raw, err := readShareLifecycleFile(*offerFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, recipient, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	offer, err := idpkg.ParseShareOffer(raw, recipient)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resolved, err := identitycli.ResolveIdentity(context.Background(), offer.Grant.Owner)
	if err != nil {
		fmt.Fprintf(stderr, "cannot resolve share owner: %v\n", err)
		return 1
	}
	ownerKey, err := idpkg.ParseEd25519PublicKey(resolved.Document.PublicKey)
	if err != nil || offer.Grant.VerifySignature(ownerKey) != nil {
		fmt.Fprintln(stderr, "offered grant signature is invalid")
		return 1
	}
	mountName := strings.TrimSpace(*name)
	if mountName == "" {
		mountName = gopath.Base(offer.Grant.Path)
	}
	mountPath, err := idpkg.NormalizeShareMountPath("shared/"+offer.Grant.Owner+"/"+mountName, offer.Grant.Owner)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	acceptedAt := time.Now().UTC().Format(time.RFC3339)
	mount := idpkg.ShareMount{
		Version: idpkg.ShareLifecycleVersion, ShareID: offer.Grant.ShareID,
		Owner: offer.Grant.Owner, SourcePath: offer.Grant.Path,
		Permissions: offer.Grant.Permissions, AcceptedAt: acceptedAt, ExpiresAt: offer.Grant.ExpiresAt,
	}
	if err := mount.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	davToken, err := MintDAVToken(context.Background(), cfg.RelayURL, recipient, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, dir := range []string{"shared/" + offer.Grant.Owner, mountPath} {
		if err := shareMKCOL(context.Background(), cfg.RelayURL, recipient, davToken.Token, dir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	documentPath := mountPath + "/" + idpkg.ShareMountFile
	if existingRaw, status, readErr := davGetBytes(context.Background(), cfg.RelayURL, recipient, davToken.Token, documentPath); readErr != nil {
		fmt.Fprintln(stderr, readErr)
		return 1
	} else if status == http.StatusOK {
		existing, parseErr := idpkg.ParseShareMount(existingRaw)
		if parseErr != nil || existing.ShareID != mount.ShareID {
			fmt.Fprintf(stderr, "mount path /%s already belongs to another or invalid share\n", mountPath)
			return 1
		}
	} else if status != http.StatusNotFound {
		fmt.Fprintf(stderr, "check mount path failed: HTTP %d\n", status)
		return 1
	}
	mountRaw, _ := json.MarshalIndent(mount, "", "  ")
	if err := davPutBytes(context.Background(), cfg.RelayURL, recipient, davToken.Token,
		documentPath, mountRaw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	acceptance := idpkg.ShareAccept{
		Version: idpkg.ShareLifecycleVersion, ShareID: offer.Grant.ShareID,
		Owner: offer.Grant.Owner, Recipient: recipient, MountPath: mountPath, AcceptedAt: acceptedAt,
	}
	acceptRaw, _ := json.Marshal(acceptance)
	acceptMetadata := map[string]string{"share_id": offer.Grant.ShareID}
	if offer.Grant.SourceShareID != "" {
		acceptMetadata["source_share_id"] = offer.Grant.SourceShareID
	}
	notified := sendShareLifecycleWithMetadata(recipient, offer.Grant.Owner, idpkg.MsgTypeShareAccept,
		acceptMetadata, time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339), acceptRaw, stderr) == 0
	result := map[string]any{"mount": mount, "mount_path": mountPath, "owner_notified": notified}
	return writeOutput(stdout, *jsonOut, result,
		fmt.Sprintf("share %s mounted at /%s (owner notified: %t)\n", offer.Grant.ShareID, mountPath, notified))
}

func readShareLifecycleFile(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(name)
}

func directSharePermissions(raw string) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "read":
		return []string{idpkg.PermRead}, true
	case "rw", "write", "read-write":
		return []string{idpkg.PermRead, idpkg.PermWrite}, true
	default:
		return nil, false
	}
}

func shareMKCOL(ctx context.Context, relayURL, owner, token, treePath string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", davFileURL(relayURL, owner, treePath), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed {
		return parseErrorResponse("create share mount directory failed", resp)
	}
	return nil
}

// --- generic DAV file helpers (bearer token) ---

func davGetBytes(ctx context.Context, relayURL, identity, token, treePath string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, davFileURL(relayURL, identity, treePath), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, err
}

func davPutBytes(ctx context.Context, relayURL, identity, token, treePath string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, davFileURL(relayURL, identity, treePath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return parseErrorResponse("store "+treePath+" failed", resp)
	}
	return nil
}

func davDeleteFile(ctx context.Context, relayURL, identity, token, treePath string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, davFileURL(relayURL, identity, treePath), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return resp.StatusCode, nil
}

// listTreeDir returns the file basenames under a tree directory using the
// sync manifest endpoint (EPIC-004) filtered to that prefix.
func listTreeDir(ctx context.Context, relayURL, identity, token, treeDir string) ([]string, error) {
	u := strings.TrimSuffix(relayURL, "/") + "/sync/" + url.PathEscape(identity) + "/manifest?paths=" + url.QueryEscape("/"+treeDir+"/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse("list "+treeDir+" failed", resp)
	}
	var names []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var entry struct {
			Path string `json:"path"`
			Dir  bool   `json:"dir"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.Path == "" || entry.Dir {
			continue
		}
		if strings.HasPrefix(entry.Path, treeDir+"/") && strings.HasSuffix(entry.Path, ".json") {
			names = append(names, strings.TrimPrefix(entry.Path, treeDir+"/"))
		}
	}
	return names, scanner.Err()
}

// loadShareSession loads the configured identity and mints a dav:full
// token for managing its share/group files.
func loadShareSession(useIdentity string, stderr io.Writer) (relayURL, identityValue, token string, ok bool) {
	cfg, identityValue, priv, okLoad := loadIdentityForDAV(useIdentity, stderr)
	if !okLoad {
		return "", "", "", false
	}
	tok, err := MintDAVToken(context.Background(), cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return "", "", "", false
	}
	return cfg.RelayURL, identityValue, tok.Token, true
}

func newShareID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "shr_" + hex.EncodeToString(buf), nil
}

func runShareAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to share from")
	var with, withGroups stringList
	fs.Var(&with, "with", "recipient Poweur ID (repeatable)")
	fs.Var(&withGroups, "with-group", "recipient group name (repeatable)")
	perm := fs.String("perm", "read", "permissions: read | rw")
	expires := fs.String("expires", "", "expiry (RFC3339), empty = never")
	noNotify := fs.Bool("no-notify", false, "create the grant without sending encrypted offers")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-notify": true})); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: poweur share add <path> --with <id> [--with-group <name>] [--perm read|rw] [--expires ...]")
		return 1
	}
	path, err := idpkg.NormalizeGrantPath(rest[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var perms []string
	switch *perm {
	case "read":
		perms = []string{idpkg.PermRead}
	case "rw", "read-write", "write":
		perms = []string{idpkg.PermRead, idpkg.PermWrite}
	default:
		fmt.Fprintln(stderr, "--perm must be read or rw")
		return 1
	}
	var audience []idpkg.ShareAudience
	for _, id := range with {
		audience = append(audience, idpkg.ShareAudience{ID: id})
	}
	for _, g := range withGroups {
		audience = append(audience, idpkg.ShareAudience{Group: g})
	}
	if len(audience) == 0 {
		fmt.Fprintln(stderr, "at least one --with or --with-group is required")
		return 1
	}
	if *expires != "" {
		if _, err := time.Parse(time.RFC3339, *expires); err != nil {
			fmt.Fprintf(stderr, "invalid --expires: %v\n", err)
			return 1
		}
	}

	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	shareID, err := newShareID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	grant := idpkg.ShareGrant{
		ShareID:     shareID,
		Owner:       identityValue,
		Path:        path,
		Audience:    audience,
		Permissions: perms,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   *expires,
	}
	if err := grant.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, err := json.MarshalIndent(grant, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := davPutBytes(ctx, cfg.RelayURL, identityValue, tok.Token, sharesTreeDir+"/"+shareID+".json", raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*noNotify {
		offer := idpkg.ShareOffer{Version: idpkg.ShareLifecycleVersion, Grant: grant, OfferedAt: time.Now().UTC().Format(time.RFC3339)}
		payload, _ := json.Marshal(offer)
		offerExpiry := time.Now().UTC().Add(7 * 24 * time.Hour)
		if grant.ExpiresAt != "" {
			if expiry, parseErr := time.Parse(time.RFC3339, grant.ExpiresAt); parseErr == nil && expiry.Before(offerExpiry) {
				offerExpiry = expiry
			}
		}
		for _, recipient := range with {
			if code := sendShareLifecycle(identityValue, recipient, idpkg.MsgTypeShareOffer, shareID, offerExpiry.Format(time.RFC3339), payload, stderr); code != 0 {
				fmt.Fprintf(stderr, "warning: grant created, but offer to %s was not delivered\n", recipient)
			}
		}
	}
	return writeOutput(stdout, *jsonOut, grant, fmt.Sprintf(
		"share %s created\n  path: /%s\n  audience: %s\n  permissions: %s\n",
		shareID, path, describeAudience(audience), strings.Join(perms, ",")))
}

func describeAudience(audience []idpkg.ShareAudience) string {
	var parts []string
	for _, a := range audience {
		switch {
		case a.ID != "":
			parts = append(parts, a.ID)
		case a.Link != "":
			// The token is the credential, so `share ls` shows that this is
			// a link and not what the link is. Anyone who needs the URL
			// itself has it already; anyone reading over a shoulder does not.
			parts = append(parts, "link")
		default:
			parts = append(parts, "group:"+a.Group)
		}
	}
	return strings.Join(parts, ", ")
}

// --- public-link shares (E05-T4) ---

// runShareLink handles `poweur share link <add|ls>`. Revoking a link is
// `poweur share revoke <share-id>` like any other grant — a link share is
// an ordinary grant document with a token for an audience, and giving it a
// second revocation verb would only invite the two to drift apart.
func runShareLink(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur share link <add|ls>  (revoke with: poweur share revoke <share-id>)")
		return 1
	}
	switch args[0] {
	case "add":
		return runShareLinkAdd(args[1:], stdout, stderr)
	case "ls":
		return runShareLinkLs(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown share link subcommand (want add, ls)")
		return 1
	}
}

// linkShareResult is the JSON shape of `share link add`. The URL is
// reported once and never again: the relay stores only the token inside a
// signed grant, and the CLI keeps nothing.
type linkShareResult struct {
	ShareID      string `json:"share_id"`
	Path         string `json:"path"`
	URL          string `json:"url"`
	Token        string `json:"token"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	MaxDownloads int    `json:"max_downloads,omitempty"`
	Password     bool   `json:"password"`
}

func runShareLinkAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share link add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to share from")
	password := fs.String("password", "", "password for the link (hashed with argon2id before it is stored)")
	passwordStdin := fs.Bool("password-stdin", false, "read the link password from stdin instead of the command line")
	expires := fs.String("expires", "", "expiry (RFC3339), empty = never")
	maxDownloads := fs.Int("max-downloads", 0, "cap on successful downloads (0 = unlimited)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--password-stdin": true})); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: poweur share link add <path> [--password ... | --password-stdin] [--expires ...] [--max-downloads N]")
		return 1
	}
	path, err := idpkg.NormalizeGrantPath(rest[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *expires != "" {
		if _, err := time.Parse(time.RFC3339, *expires); err != nil {
			fmt.Fprintf(stderr, "invalid --expires: %v\n", err)
			return 1
		}
	}
	if *passwordStdin {
		if *password != "" {
			fmt.Fprintln(stderr, "use either --password or --password-stdin, not both")
			return 1
		}
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*password = strings.TrimRight(string(raw), "\r\n")
		if *password == "" {
			fmt.Fprintln(stderr, "--password-stdin got an empty password")
			return 1
		}
	}

	// Build the link options first: a validation failure here should cost
	// nothing, and in particular should not leave a half-made grant behind.
	var link *idpkg.ShareLink
	if *password != "" || *maxDownloads > 0 {
		link = &idpkg.ShareLink{MaxDownloads: *maxDownloads}
		if *password != "" {
			hash, err := idpkg.HashLinkPassword(*password)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			link.Password = hash
		}
	}
	token, err := idpkg.GenerateLinkToken()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	shareID, err := newShareID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	grant := idpkg.ShareGrant{
		ShareID:     shareID,
		Owner:       identityValue,
		Path:        path,
		Audience:    []idpkg.ShareAudience{{Link: token}},
		Permissions: []string{idpkg.PermRead}, // link shares are read-only in v1
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   *expires,
		Link:        link,
	}
	if err := grant.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, err := json.MarshalIndent(grant, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := davPutBytes(ctx, cfg.RelayURL, identityValue, tok.Token, sharesTreeDir+"/"+shareID+".json", raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	result := linkShareResult{
		ShareID: shareID, Path: path,
		URL:          shareLinkURL(identityValue, token),
		Token:        token,
		ExpiresAt:    *expires,
		MaxDownloads: *maxDownloads,
		Password:     link != nil && link.Password != "",
	}
	var text strings.Builder
	fmt.Fprintf(&text, "link share %s created\n  %s\n", shareID, result.URL)
	fmt.Fprintf(&text, "  path: /%s\n", path)
	if result.ExpiresAt != "" {
		fmt.Fprintf(&text, "  expires: %s\n", result.ExpiresAt)
	}
	if result.MaxDownloads > 0 {
		fmt.Fprintf(&text, "  downloads: %d max\n", result.MaxDownloads)
	}
	if result.Password {
		fmt.Fprintln(&text, "  password: required")
	}
	// Said plainly, because a capability URL behaves unlike every other
	// share in the system and the moment of creation is when that matters.
	fmt.Fprintln(&text, "\nAnyone with this URL can read the share — it is the whole credential.")
	fmt.Fprintf(&text, "Revoke it with: poweur share revoke %s\n", shareID)
	return writeOutput(stdout, *jsonOut, result, text.String())
}

// shareLinkURL is where a link share lives: the owner's own host.
func shareLinkURL(identity, token string) string {
	return "https://" + identity + "/s/" + token
}

func runShareLinkLs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share link ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	names, err := listTreeDir(ctx, relayURL, identityValue, token, sharesTreeDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var links []linkShareResult
	for _, name := range names {
		raw, status, err := davGetBytes(ctx, relayURL, identityValue, token, sharesTreeDir+"/"+name)
		if err != nil || status != http.StatusOK {
			continue
		}
		g, err := idpkg.ParseShareGrant(raw)
		if err != nil {
			continue
		}
		linkTok, isLink := g.LinkToken()
		if !isLink {
			continue
		}
		links = append(links, linkShareResult{
			ShareID: g.ShareID, Path: g.Path,
			URL:          shareLinkURL(g.Owner, linkTok),
			Token:        linkTok,
			ExpiresAt:    g.ExpiresAt,
			MaxDownloads: g.MaxDownloads(),
			Password:     g.RequiresPassword(),
		})
	}
	if *jsonOut {
		return writeOutput(stdout, true, links, "")
	}
	if len(links) == 0 {
		fmt.Fprintln(stdout, "no link shares")
		return 0
	}
	for _, l := range links {
		expiry := l.ExpiresAt
		if expiry == "" {
			expiry = "never"
		}
		limit := "unlimited"
		if l.MaxDownloads > 0 {
			limit = strconv.Itoa(l.MaxDownloads)
		}
		fmt.Fprintf(stdout, "%s\t/%s\t%s\texpires=%s\tmax=%s\tpassword=%t\n",
			l.ShareID, l.Path, l.URL, expiry, limit, l.Password)
	}
	return 0
}

func runShareLs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	names, err := listTreeDir(ctx, relayURL, identityValue, token, sharesTreeDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var grants []idpkg.ShareGrant
	for _, name := range names {
		raw, status, err := davGetBytes(ctx, relayURL, identityValue, token, sharesTreeDir+"/"+name)
		if err != nil || status != http.StatusOK {
			continue
		}
		if g, err := idpkg.ParseShareGrant(raw); err == nil {
			grants = append(grants, g)
		}
	}
	if *jsonOut {
		return writeOutput(stdout, true, grants, "")
	}
	if len(grants) == 0 {
		fmt.Fprintln(stdout, "no shares")
		return 0
	}
	for _, g := range grants {
		expiry := g.ExpiresAt
		if expiry == "" {
			expiry = "never"
		}
		fmt.Fprintf(stdout, "%s\t/%s\t%s\tperm=%s\texpires=%s\n",
			g.ShareID, g.Path, describeAudience(g.Audience), strings.Join(g.Permissions, ","), expiry)
	}
	return 0
}

func runShareRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("share revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: poweur share revoke <share-id>")
		return 1
	}
	shareID := rest[0]
	if strings.ContainsAny(shareID, "/\\") {
		fmt.Fprintln(stderr, "invalid share id")
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	raw, readStatus, _ := davGetBytes(ctx, relayURL, identityValue, token, sharesTreeDir+"/"+shareID+".json")
	var grant idpkg.ShareGrant
	if readStatus == http.StatusOK {
		_ = json.Unmarshal(raw, &grant)
	}
	status, err := davDeleteFile(ctx, relayURL, identityValue, token, sharesTreeDir+"/"+shareID+".json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if status == http.StatusNotFound {
		fmt.Fprintf(stderr, "share %s not found\n", shareID)
		return 1
	}
	if status >= 300 {
		fmt.Fprintf(stderr, "revoke failed: HTTP %d\n", status)
		return 1
	}
	revoked := idpkg.ShareRevoked{
		Version: idpkg.ShareLifecycleVersion, ShareID: shareID,
		Owner: identityValue, RevokedAt: time.Now().UTC().Format(time.RFC3339),
	}
	payload, _ := json.Marshal(revoked)
	for _, audience := range grant.Audience {
		if audience.ID == "" {
			continue
		}
		if code := sendShareLifecycle(identityValue, audience.ID, idpkg.MsgTypeShareRevoked, shareID, "", payload, stderr); code != 0 {
			fmt.Fprintf(stderr, "warning: access is revoked, but %s could not be notified\n", audience.ID)
		}
	}
	fmt.Fprintf(stdout, "share %s revoked\n", shareID)
	return 0
}

func sendShareLifecycle(useIdentity, recipient, msgType, shareID, expiresAt string, payload []byte, stderr io.Writer) int {
	return sendShareLifecycleWithMetadata(useIdentity, recipient, msgType,
		map[string]string{"share_id": shareID}, expiresAt, payload, stderr)
}

func sendShareLifecycleWithMetadata(useIdentity, recipient, msgType string, metadata map[string]string, expiresAt string, payload []byte, stderr io.Writer) int {
	args := []string{
		"--use-identity=" + useIdentity,
		"--sign-with=identity",
		"--via-home-relay",
		"--type=" + msgType,
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--meta="+key+"="+metadata[key])
	}
	if expiresAt != "" {
		args = append(args, "--expires="+expiresAt)
	}
	args = append(args, recipient, string(payload))
	var discard bytes.Buffer
	return runSend(args, &discard, stderr)
}

func runShareGroup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur share group <set|ls|remove>")
		return 1
	}
	sub := args[0]
	fs := flag.NewFlagSet("share group "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	members := fs.String("members", "", "comma-separated member Poweur IDs")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})); err != nil {
		return 1
	}
	rest := fs.Args()

	switch sub {
	case "set":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "usage: poweur share group set <name> --members bob.example.org,carol.poweur.net")
			return 1
		}
		name := rest[0]
		var memberList []string
		for _, m := range strings.Split(*members, ",") {
			if m = strings.TrimSpace(m); m != "" {
				memberList = append(memberList, m)
			}
		}
		cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
		if !ok {
			return 1
		}
		group := idpkg.ShareGroup{
			Group: name, Owner: identityValue, Members: memberList,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := group.Sign(priv); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		raw, _ := json.MarshalIndent(group, "", "  ")
		ctx := context.Background()
		tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := davPutBytes(ctx, cfg.RelayURL, identityValue, tok.Token, groupsTreeDir+"/"+name+".json", raw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeOutput(stdout, *jsonOut, group, fmt.Sprintf(
			"group %q set (%d member(s))\n", name, len(memberList)))
	case "ls":
		relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
		if !ok {
			return 1
		}
		ctx := context.Background()
		names, err := listTreeDir(ctx, relayURL, identityValue, token, groupsTreeDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var groups []idpkg.ShareGroup
		for _, name := range names {
			raw, status, err := davGetBytes(ctx, relayURL, identityValue, token, groupsTreeDir+"/"+name)
			if err != nil || status != http.StatusOK {
				continue
			}
			if g, err := idpkg.ParseShareGroup(raw); err == nil {
				groups = append(groups, g)
			}
		}
		if *jsonOut {
			return writeOutput(stdout, true, groups, "")
		}
		if len(groups) == 0 {
			fmt.Fprintln(stdout, "no groups")
			return 0
		}
		for _, g := range groups {
			fmt.Fprintf(stdout, "%s\t%s\n", g.Group, strings.Join(g.Members, ", "))
		}
		return 0
	case "remove":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "usage: poweur share group remove <name>")
			return 1
		}
		name := rest[0]
		if strings.ContainsAny(name, "/\\") {
			fmt.Fprintln(stderr, "invalid group name")
			return 1
		}
		relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
		if !ok {
			return 1
		}
		status, err := davDeleteFile(context.Background(), relayURL, identityValue, token, groupsTreeDir+"/"+name+".json")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if status == http.StatusNotFound {
			fmt.Fprintf(stderr, "group %q not found\n", name)
			return 1
		}
		if status >= 300 {
			fmt.Fprintf(stderr, "remove failed: HTTP %d\n", status)
			return 1
		}
		fmt.Fprintf(stdout, "group %q removed\n", name)
		return 0
	default:
		fmt.Fprintln(stderr, "unknown share group subcommand (want set, ls, remove)")
		return 1
	}
}
