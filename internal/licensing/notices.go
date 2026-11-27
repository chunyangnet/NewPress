package licensing

import (
	_ "embed"
	"io"
)

// ThirdPartyLicenses is generated from locked dependencies and bundled notices.
//
//go:embed THIRD_PARTY_LICENSES.txt
var ThirdPartyLicenses string

// FrontendLicenses follows the actual bundled npm graph, including transitive code.
//
//go:embed FRONTEND_LICENSES.txt
var FrontendLicenses string

// WriteNotices makes the licenses accessible even in a standalone binary or an
// update installed by an older updater that cannot extract a licenses directory.
func WriteNotices(w io.Writer) error {
	_, err := io.WriteString(w, "NewPress — Apache-2.0\n\n"+Apache20+"\n\n"+ThirdPartyLicenses+"\n\n"+FrontendLicenses)
	return err
}
