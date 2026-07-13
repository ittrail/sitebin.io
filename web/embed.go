// Package web holds Sitebin's embedded frontend: the landing/create UI, the
// edit UI, the viewer runtime, and vendored third-party libraries. Everything
// is served from the binary — no CDN, no build step.
package web

import "embed"

//go:embed static viewer vendor
var Assets embed.FS
