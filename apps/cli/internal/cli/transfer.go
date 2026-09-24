package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	syncclient "github.com/poweur/cli/internal/sync"
)

// runTransfer is the CLI-first E05-T7 transfer surface. A transfer is a
// short-lived, read-only public-link share rooted below shared/.transfers.
// Uploads always use the resumable endpoint, even for small files, so this
// path exercises the same transport multi-gigabyte sends require.
func runTransfer(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "usage: poweur transfer create <file> [--expires <rfc3339>] [--password ... | --password-stdin] [--max-downloads N] [--json]")
		return 1
	}
	fs := flag.NewFlagSet("transfer create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity sending the transfer")
	expires := fs.String("expires", "", "expiry (RFC3339; default seven days)")
	password := fs.String("password", "", "password for the download link")
	passwordStdin := fs.Bool("password-stdin", false, "read the link password from stdin")
	maxDownloads := fs.Int("max-downloads", 0, "cap on successful downloads (0 = unlimited)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true, "--password-stdin": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur transfer create <file> [--expires <rfc3339>] [--password ... | --password-stdin] [--max-downloads N] [--json]")
		return 1
	}
	if *maxDownloads < 0 {
		fmt.Fprintln(stderr, "--max-downloads cannot be negative")
		return 1
	}
	if *expires == "" {
		*expires = time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	} else if expiry, err := time.Parse(time.RFC3339, *expires); err != nil || !expiry.After(time.Now().UTC()) {
		fmt.Fprintln(stderr, "--expires must be a future RFC3339 timestamp")
		return 1
	}
	if *passwordStdin && *password != "" {
		fmt.Fprintln(stderr, "use either --password or --password-stdin, not both")
		return 1
	}

	localPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := os.Open(localPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "transfer source must be a regular file")
		return 1
	}
	if info.Size() == 0 {
		fmt.Fprintln(stderr, "transfer source must not be empty")
		return 1
	}

	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	token, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	transferID := "tr_" + hex.EncodeToString(random)
	remoteDir := "shared/.transfers/" + transferID
	remotePath := remoteDir + "/" + filepath.Base(localPath)
	remote := &syncclient.HTTPRemote{
		RelayURL: cfg.RelayURL, Identity: identityValue, Token: token.Token,
		DeviceHeaders: deviceHeaders(), ChunkThreshold: 1,
	}
	for _, dir := range []string{"shared/.transfers", remoteDir} {
		if err := remote.Mkdir(ctx, dir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := remote.Put(ctx, remotePath, file, info.Size()); err != nil {
		_ = remote.Delete(ctx, remoteDir)
		fmt.Fprintln(stderr, err)
		return 1
	}

	linkArgs := []string{remoteDir, "--use-identity=" + identityValue, "--expires=" + *expires,
		"--max-downloads=" + strconv.Itoa(*maxDownloads), "--json"}
	if *passwordStdin {
		linkArgs = append(linkArgs, "--password-stdin")
	} else if *password != "" {
		linkArgs = append(linkArgs, "--password="+*password)
	}
	var linkOut, linkErr bytes.Buffer
	if code := runShareLinkAdd(linkArgs, &linkOut, &linkErr); code != 0 {
		_ = remote.Delete(ctx, remoteDir)
		fmt.Fprint(stderr, linkErr.String())
		return code
	}
	var link linkShareResult
	if err := json.Unmarshal(linkOut.Bytes(), &link); err != nil {
		_ = remote.Delete(ctx, remoteDir)
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := map[string]any{
		"transfer_id": transferID, "share_id": link.ShareID, "path": remotePath,
		"name": info.Name(), "size": info.Size(), "url": link.URL,
		"expires_at": *expires, "max_downloads": *maxDownloads, "password": *password != "" || *passwordStdin,
	}
	human := fmt.Sprintf("transfer %s uploaded (%s bytes)\n  %s\n  expires: %s\nRevoke it with: poweur share revoke %s\n",
		transferID, strings.TrimSpace(strconv.FormatInt(info.Size(), 10)), link.URL, *expires, link.ShareID)
	return writeOutput(stdout, *jsonOut, result, human)
}
