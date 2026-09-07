//go:build ee

// Package ee is the Sitebin Enterprise Edition: the premium accounts, tiers,
// SMTP, OAuth, and billing features.
//
// This package and everything under ee/ is NOT covered by the repository's MIT
// license. It is licensed under ee/LICENSE. It compiles into the binary only
// with the `ee` build tag; the community build excludes it entirely.
package ee

import "github.com/ittrail/sitebin.io/internal/ext"

// Version of the enterprise extension. It is the binary's own version: the
// extension ships inside it and has no release cycle of its own.
var Version = "dev"

func init() {
	ext.Register(newProvider())
}
