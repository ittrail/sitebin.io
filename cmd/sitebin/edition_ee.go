//go:build ee

package main

// Importing ee for its init() side effect registers the enterprise extension
// provider. This file is compiled only under the `ee` build tag.
import _ "github.com/ittrail/sitebin/ee"

const edition = "enterprise"
