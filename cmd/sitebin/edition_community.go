//go:build !ee

package main

// The community build registers no extension provider; Sitebin runs fully open
// (no accounts), exactly as its MIT core specifies.
const edition = "community"

// editionDetail is empty for the community build: it has no licensing.
func editionDetail() string { return "" }
