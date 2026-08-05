// Package web embeds podkop-server's static assets (the decoy site) into the
// binary so it ships as a single self-contained executable.
package web

import "embed"

// DecoyFS holds the built-in decoy site. It can be overridden at runtime with a
// custom directory via PODKOP_SERVER_DECOY_DIR.
//
//go:embed decoy
var DecoyFS embed.FS
