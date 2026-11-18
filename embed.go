// Package iphoneuse embeds the on-device helper source so a single binary can
// build, sign and install it on any iPhone with nothing else to download.
package iphoneuse

import "embed"

//go:embed all:helper
var HelperFS embed.FS
