// Package web embeds podkop-server's static assets (the public site, the
// operator panel and the legacy decoy) into the binary so it ships as a single
// self-contained executable with no external requests at runtime.
package web

import "embed"

// DecoyFS holds the built-in decoy site. It can be overridden at runtime with a
// custom directory via PODKOP_SERVER_DECOY_DIR.
//
//go:embed decoy
var DecoyFS embed.FS

// AdminFS holds the operator area: templates, stylesheet and the DSEG7 font
// used for the panel's numeric readouts.
//
//go:embed admin
var AdminFS embed.FS

// SiteFS holds the public Halogen devlog: templates, stylesheet and post media.
//
//go:embed site
var SiteFS embed.FS
