//go:build !ee

package main

// The community build registers no extension provider; Sitebin runs fully open
// (no accounts), exactly as its MIT core specifies.
const edition = "community"
