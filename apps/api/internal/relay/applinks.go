package relay

import (
	"net/http"
)

// App association for hosted identities (EPIC-028 E28-T6).
//
// A sign-in or pairing QR carries https://<identity>/id/approve#…. When the
// store app is installed and these files name it, a phone's camera opens
// that link in the app instead of the browser — no page, no tap before
// Approve. Only /id/approve is claimed: every other link on the identity's
// host stays in the browser. Self-hosted identities are not covered (the
// store app can only list domains it was built with); their links open the
// web app, which offers the app from there.

// approveAppLinkPath is the one path the store app claims.
const approveAppLinkPath = appPrefix + "/approve"

func (s *Server) handleAppleAppSiteAssociation(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.AppLinksIOSAppIDs) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"applinks": map[string]any{
			"details": []map[string]any{{
				"appIDs": s.cfg.AppLinksIOSAppIDs,
				"components": []map[string]string{
					{"/": approveAppLinkPath, "comment": "sign-in and new-device approvals"},
					{"/": approveAppLinkPath + "/", "comment": "the same, with a trailing slash"},
				},
			}},
		},
	})
}

func (s *Server) handleAssetLinks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AppLinksAndroidPackage == "" || len(s.cfg.AppLinksAndroidSHA256) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, []map[string]any{{
		"relation": []string{"delegate_permission/common.handle_all_urls"},
		"target": map[string]any{
			"namespace":                "android_app",
			"package_name":             s.cfg.AppLinksAndroidPackage,
			"sha256_cert_fingerprints": s.cfg.AppLinksAndroidSHA256,
		},
	}})
}
