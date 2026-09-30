package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
	"golang.org/x/crypto/argon2"
)

var linkPath = regexp.MustCompile(`^/s/([0-9a-f]{32})/?$`)

// linkInfo is GET /drive/{id}/links/{link}: what a holder needs before
// opening the link. It never carries a key.
type linkInfo struct {
	Drive    string `json:"drive"`
	Role     string `json:"role"`
	Password bool   `json:"password"`
	Salt     string `json:"salt"`
}

// runLinkGet opens a key-in-fragment link without a Poweur ID, like the web
// viewer: the relay sees the link ID and, for a password link, the verifier
// half of the argon2id output; the fragment and password stay here. It writes
// the linked file, or a file inside a linked folder (inside), to out.
func runLinkGet(ctx context.Context, rawURL, inside, password, out string, stderr io.Writer) (map[string]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	match := linkPath.FindStringSubmatch(u.Path)
	fragment, fragErr := base64.RawURLEncoding.DecodeString(strings.TrimRight(u.Fragment, "="))
	if match == nil || fragErr != nil || len(fragment) != 32 {
		return nil, errors.New("not a drive link: expected https://<id>/s/<link>#<secret>")
	}
	drive, linkID := strings.ToLower(u.Hostname()), match[1]

	res, err := identity.ResolveIdentity(ctx, drive)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", drive, err)
	}
	relay := strings.TrimRight(strings.TrimSpace(res.Document.Relay), "/")
	if relay == "" {
		return nil, fmt.Errorf("%s names no relay", drive)
	}
	if !strings.HasPrefix(relay, "http://") && !strings.HasPrefix(relay, "https://") {
		relay = u.Scheme + "://" + relay
	}

	client := &driveclient.Client{Relay: relay, Drive: drive, LinkID: linkID}
	var info linkInfo
	if err = client.Get(ctx, "/links/"+linkID, &info); err != nil {
		return nil, err
	}
	secret := fragment
	if info.Password {
		if password == "" {
			return nil, errors.New("this link needs a password (--password)")
		}
		salt, err := base64.RawURLEncoding.DecodeString(info.Salt)
		if err != nil || len(salt) == 0 {
			return nil, errors.New("link has no password salt")
		}
		derived := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 64)
		if secret, err = driveclient.LinkSecret(fragment, derived[:32]); err != nil {
			return nil, err
		}
		client.LinkVerifier = derived[32:]
	}
	if info.Drive != "" {
		client.Drive = info.Drive
	}
	files := &driveclient.Files{Client: client, EncryptionKey: secret}
	files.Authors = func(author string) (ed25519.PublicKey, error) {
		res, err := identity.ResolveIdentity(ctx, author)
		if err != nil {
			return nil, err
		}
		return idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	}
	shared, err := files.Shared(ctx)
	if err != nil {
		var driveErr *driveclient.Error
		if errors.As(err, &driveErr) && driveErr.Status == http.StatusUnauthorized && info.Password {
			return nil, errors.New("wrong password")
		}
		return nil, err
	}
	if len(shared) == 0 {
		return nil, errors.New("the link opens nothing")
	}
	file := shared[0]
	if inside = strings.Trim(inside, "/"); inside != "" {
		if file, err = files.Resolve(ctx, "/"+file.Manifest.Node+"/"+inside); err != nil {
			return nil, err
		}
	}
	output, err := os.CreateTemp(filepath.Dir(out), ".poweur-link-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	if err = files.Read(ctx, file, output); err != nil {
		return nil, err
	}
	if err = output.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(output.Name(), out); err != nil {
		return nil, err
	}
	return map[string]string{"node": file.Manifest.Node, "name": file.Name, "path": out}, nil
}
