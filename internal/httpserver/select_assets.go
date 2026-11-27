package httpserver

import (
	"bytes"
	"regexp"
)

var (
	selectStyles = regexp.MustCompile(`(?i)<link\s+(?:[^>]*\s)?href=["'][^"']*/chunnel-selects\.css(?:[?#][^"']*)?["']`)
	selectScript = regexp.MustCompile(`(?i)<script\s+(?:[^>]*\s)?src=["'][^"']*/chunnel-selects\.js(?:[?#][^"']*)?["']`)
)

// Installed themes may provide the shared assets themselves. A homepage with
// no initial selects can defer them until its registration dialog opens.
func withSelectAssets(output []byte, page string, bases ...string) []byte {
	if page == "home" && !bytes.Contains(output, []byte("<select")) {
		return output
	}
	headEnd := bytes.Index(output, []byte("</head>"))
	if headEnd < 0 {
		return output
	}
	head := output[:headEnd]
	if !bytes.Contains(head, []byte("data-choice-controls")) {
		output = bytes.Replace(output, []byte("<html "), []byte(`<html data-choice-controls `), 1)
		output = bytes.Replace(output, []byte("<html>"), []byte(`<html data-choice-controls>`), 1)
	}
	if !selectStyles.Match(head) {
		css := `<link rel="stylesheet" href="` + publicCoreAsset("css/chunnel-selects.css", bases...) + `"></head>`
		output = bytes.Replace(output, []byte("</head>"), []byte(css), 1)
	}
	if !selectScript.Match(output) {
		js := `<script src="` + publicCoreAsset("js/chunnel-selects.js", bases...) + `" defer></script></body>`
		output = bytes.Replace(output, []byte("</body>"), []byte(js), 1)
	}
	return output
}
