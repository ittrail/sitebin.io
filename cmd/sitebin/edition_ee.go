//go:build ee

package main

// Importing ee for its init() side effect registers the enterprise extension
// provider. This file is compiled only under the `ee` build tag.
import (
	"fmt"

	_ "github.com/ittrail/sitebin.io/ee"
	"github.com/ittrail/sitebin.io/ee/licensing"
)

const edition = "enterprise"

// editionDetail is what `sitebin version` prints after the edition: the number
// of license roots this binary trusts. It is the check a release runbook makes
// on the artifact, because an enterprise image built without any is a 90-day
// trial that no license can ever unlock, and nothing else about it looks
// wrong until the trial ends.
func editionDetail() string {
	roots, err := licensing.TrustedRoots()
	if err != nil {
		return fmt.Sprintf(", trusted license roots: UNUSABLE (%v)", err)
	}
	if len(roots) == 0 {
		return ", trusted license roots: NONE (unlicensable build)"
	}
	return fmt.Sprintf(", trusted license roots: %d", len(roots))
}
