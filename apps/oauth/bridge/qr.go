package bridge

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// qrSVG renders text as an inline SVG QR code: one path, no scripts, no
// external resources, so it passes the page's CSP and prints cleanly.
func qrSVG(text string) string {
	if text == "" {
		return ""
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 4
	size := code.Size + 2*quiet
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	// #nosec — every value above is a number we produced.
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="QR code of the sign-in request" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		size, size, size, size, path.String())
}
