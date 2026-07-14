// Package eeconfig parses Sitebin Enterprise Edition configuration — account
// mode, tiers, and quota caps — from environment variables set at container
// startup.
//
// This file (doc.go) carries no build tag so the community build compiles an
// empty package here; the real implementation is in eeconfig.go under the `ee`
// build tag. Enterprise code and its dependencies never enter the community
// binary. Licensed under ee/LICENSE (not MIT).
package eeconfig
