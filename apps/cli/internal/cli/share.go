package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	idpkg "github.com/poweur/identity"
)

// Share management (EPIC-005 E05-T3, CLI slice): grants are signed
// documents the owner writes into their own tree at
// poweur-sys/relay/shares/<share-id>.json; groups at
// poweur-sys/relay/groups/<name>.json. The relay enforces them; revocation
// is deleting the file. Offer/accept messaging and recipient-side mounts
// land with EPIC-009 groundwork.

const (
	sharesTreeDir = "poweur-sys/relay/shares"
	groupsTreeDir = "poweur-sys/relay/groups"
)

func runShare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur share <add|ls|revoke|group>")
		return 1
	}
	switch args[0] {
	case "add":
		return runShareAdd(args[1:], stdout, stderr)
	case "ls":
		return runShareLs(args[1:], stdout, stderr)
	case "revoke":
		return runShareRevoke(args[1:], stdout, stderr)
	case "group":
		return runShareGroup(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown share subcommand (want add, ls, revoke, group)")
		return 1
	}
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
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
	return writeOutput(stdout, *jsonOut, grant, fmt.Sprintf(
		"share %s created\n  path: /%s\n  audience: %s\n  permissions: %s\n",
		shareID, path, describeAudience(audience), strings.Join(perms, ",")))
}

func describeAudience(audience []idpkg.ShareAudience) string {
	var parts []string
	for _, a := range audience {
		if a.ID != "" {
			parts = append(parts, a.ID)
		} else {
			parts = append(parts, "group:"+a.Group)
		}
	}
	return strings.Join(parts, ", ")
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
	status, err := davDeleteFile(context.Background(), relayURL, identityValue, token, sharesTreeDir+"/"+shareID+".json")
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
	fmt.Fprintf(stdout, "share %s revoked\n", shareID)
	return 0
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
