package bridge

import (
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// Inbound HTML is hostile. The bridge sanitizes it with an allowlist before it
// is encrypted to the user, and the client sanitizes it again and renders it
// in a sandboxed frame with no scripts and no network: neither layer trusts
// the other (EPIC-023 "Safe rendering of HTML email").
//
// The allowlist keeps what mail layouts need — tables, inline styles for
// colour, type and spacing, images — and drops everything that runs or
// fetches: scripts, frames, objects, forms, event handlers, `<base>`,
// `<meta>`, `<style>` blocks, and any URL scheme but http(s), mailto, cid and
// data:image. Remote images survive as https URLs so the client can offer
// "Load images"; the client blocks them by default.

var (
	policyOnce sync.Once
	policy     *bluemonday.Policy
)

// safeStyles are the CSS properties kept in `style` attributes. None of them
// takes a url(), which is the property that matters: background-image,
// list-style-image, content, cursor and friends are left out.
var safeStyles = []string{
	"color", "background-color", "font", "font-family", "font-size", "font-style", "font-weight",
	"font-variant", "line-height", "letter-spacing", "word-spacing", "text-align", "text-decoration",
	"text-indent", "text-transform", "white-space", "vertical-align", "direction",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left", "border-color",
	"border-style", "border-width", "border-radius", "border-collapse", "border-spacing",
	"width", "max-width", "min-width", "height", "max-height", "min-height", "display",
	"table-layout", "list-style-type", "float", "clear", "overflow",
}

var dataImage = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=\s]+$`)

func htmlPolicy() *bluemonday.Policy {
	policyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		p.AllowElements("font", "center", "u", "s", "small", "big", "span", "div", "section", "header", "footer", "article")
		p.AllowAttrs("color", "face", "size").OnElements("font")
		p.AllowAttrs("align", "valign", "bgcolor", "width", "height", "cellpadding", "cellspacing", "border").
			OnElements("table", "tr", "td", "th", "tbody", "thead", "tfoot", "col", "colgroup", "div", "p", "img", "center")
		p.AllowAttrs("dir", "lang", "title").Globally()
		p.AllowStyles(safeStyles...).Globally()
		p.AllowURLSchemes("http", "https", "mailto", "cid")
		p.AllowURLSchemeWithCustomPolicy("data", func(u *url.URL) bool { return strings.HasPrefix(u.Opaque, "image/") })
		p.AllowImages()
		p.AllowAttrs("alt", "width", "height").OnElements("img")
		p.RequireNoReferrerOnLinks(true)
		p.RequireNoFollowOnLinks(true)
		p.AddTargetBlankToFullyQualifiedLinks(true)
		p.RequireParseableURLs(true)
		policy = p
	})
	return policy
}

// SanitizeHTML returns safe HTML. data: URLs are kept only for inline
// base64 images; everything else the policy refuses is dropped.
func SanitizeHTML(html string) string {
	out := htmlPolicy().Sanitize(html)
	// bluemonday's scheme check cannot look inside a data: URL; drop any
	// data: src that is not a plain base64 image.
	return dataSrc.ReplaceAllStringFunc(out, func(m string) string {
		sub := dataSrc.FindStringSubmatch(m)
		if len(sub) == 2 && dataImage.MatchString(strings.ReplaceAll(sub[1], "&#43;", "+")) {
			return m
		}
		return `src=""`
	})
}

var dataSrc = regexp.MustCompile(`src="(data:[^"]*)"`)
