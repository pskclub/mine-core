package devtools

import (
	"bytes"
	_ "embed"
)

// uiHTML is the whole panel: markup, style and script in one file, embedded in
// the binary.
//
// No build step and no CDN, deliberately. A Go library that needs node to be
// installed before it compiles is a library nobody upgrades, and a debug page
// that fetches a framework from the internet does not load on the network where
// it is most needed — a locked-down staging box with no egress.
//
//go:embed ui.html
var uiHTML []byte

// renderPage bakes the mount prefix into the page, so the same asset works
// wherever it was mounted without the script having to guess its own URL.
func renderPage(prefix string) []byte {
	return bytes.ReplaceAll(uiHTML, []byte("{{base}}"), []byte(prefix))
}
