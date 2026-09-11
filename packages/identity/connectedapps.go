package identity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConnectedAppsPath is the user-editable relay policy file for third-party
// applications authorized through Sign in with Poweur ID.
const ConnectedAppsPath = "poweur-sys/relay/connected-apps.json"

const ConnectedAppsVersion = 1

type ConnectedApp struct {
	AppID     string   `json:"app_id"`
	Audience  string   `json:"audience"`
	Name      string   `json:"name,omitempty"`
	Scopes    []string `json:"scopes"`
	GrantedAt string   `json:"granted_at"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	RevokedAt string   `json:"revoked_at,omitempty"`
}

type ConnectedApps struct {
	Version int            `json:"version"`
	Apps    []ConnectedApp `json:"apps"`
}

func ParseConnectedApps(raw []byte) (ConnectedApps, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return ConnectedApps{Version: ConnectedAppsVersion, Apps: []ConnectedApp{}}, nil
	}
	var doc ConnectedApps
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return ConnectedApps{}, fmt.Errorf("connected apps: %w", err)
	}
	if doc.Version != ConnectedAppsVersion {
		return ConnectedApps{}, fmt.Errorf("connected apps: unsupported version %d", doc.Version)
	}
	seen := map[string]bool{}
	for i := range doc.Apps {
		a := &doc.Apps[i]
		a.AppID = strings.ToLower(strings.TrimSpace(a.AppID))
		origin, err := NormalizeOrigin(a.Audience)
		if err != nil {
			return ConnectedApps{}, fmt.Errorf("connected app %d: %w", i, err)
		}
		a.Audience = origin
		want, err := SignInAppID(origin)
		if err != nil || want != a.AppID {
			return ConnectedApps{}, fmt.Errorf("connected app %d: app_id does not match audience", i)
		}
		if seen[a.AppID] {
			return ConnectedApps{}, fmt.Errorf("connected apps: duplicate app_id %q", a.AppID)
		}
		seen[a.AppID] = true
		a.Scopes, err = NormalizeSignInScopes(a.Scopes)
		if err != nil {
			return ConnectedApps{}, fmt.Errorf("connected app %d: %w", i, err)
		}
		for _, scope := range a.Scopes {
			if err := CheckSignInScopeNamespace(scope, a.AppID); err != nil {
				return ConnectedApps{}, err
			}
		}
		if _, err := time.Parse(time.RFC3339, a.GrantedAt); err != nil {
			return ConnectedApps{}, fmt.Errorf("connected app %d: granted_at must be RFC3339", i)
		}
		for _, stamp := range []string{a.ExpiresAt, a.RevokedAt} {
			if stamp != "" {
				if _, err := time.Parse(time.RFC3339, stamp); err != nil {
					return ConnectedApps{}, fmt.Errorf("connected app %d: timestamp must be RFC3339", i)
				}
			}
		}
	}
	sort.Slice(doc.Apps, func(i, j int) bool { return doc.Apps[i].AppID < doc.Apps[j].AppID })
	if doc.Apps == nil {
		doc.Apps = []ConnectedApp{}
	}
	return doc, nil
}

func (d ConnectedApps) Active(appID string, now time.Time) (ConnectedApp, bool) {
	appID = strings.ToLower(strings.TrimSpace(appID))
	for _, app := range d.Apps {
		if app.AppID != appID || app.RevokedAt != "" {
			continue
		}
		if app.ExpiresAt != "" {
			expires, err := time.Parse(time.RFC3339, app.ExpiresAt)
			if err != nil || !now.Before(expires) {
				return ConnectedApp{}, false
			}
		}
		return app, true
	}
	return ConnectedApp{}, false
}

func (d ConnectedApps) Upsert(app ConnectedApp) ConnectedApps {
	d.Version = ConnectedAppsVersion
	replaced := false
	for i := range d.Apps {
		if strings.EqualFold(d.Apps[i].AppID, app.AppID) {
			d.Apps[i] = app
			replaced = true
			break
		}
	}
	if !replaced {
		d.Apps = append(d.Apps, app)
	}
	sort.Slice(d.Apps, func(i, j int) bool { return d.Apps[i].AppID < d.Apps[j].AppID })
	return d
}
