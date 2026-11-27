// Package licensing keeps the license notices needed by generated host-plugin
// descriptors available when NewPress runs without its source checkout.
package licensing

import _ "embed"

// Apache20 is the project's complete Apache-2.0 license.
//
//go:embed LICENSE
var Apache20 string

// HostPluginNotices explains the boundary between a descriptor and host assets.
//
//go:embed HOST_PLUGIN_NOTICES.md
var HostPluginNotices string

// FlagIconsMIT is the upstream notice for the comment-origin host artwork.
//
//go:embed flag-icons-MIT.txt
var FlagIconsMIT string
