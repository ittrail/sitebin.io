//go:build ee

package ee

import (
	"crypto/sha256"
	"encoding/base64"
)

// copyScript backs the one interactive control in the dashboard: the button
// that copies a freshly issued secret. It is the only script the pages carry.
//
// It must stay byte-identical to what the template emits, because the CSP
// admits it by hash — see copyScriptCSP. Editing it here updates the hash
// automatically; editing the copy in the template would break it, which a test
// guards against.
const copyScript = `var b=document.getElementById('copybtn'),v=document.getElementById('secretval');` +
	`b.hidden=!navigator.clipboard;` +
	`b.addEventListener('click',function(){navigator.clipboard.writeText(v.textContent).then(function(){b.textContent='Copied'})});`

// copyScriptCSP is the script-src value that admits exactly copyScript and
// nothing else. A hash is used rather than 'unsafe-inline' so an injected
// script — the thing the rest of this CSP exists to stop — still cannot run.
var copyScriptCSP = func() string {
	sum := sha256.Sum256([]byte(copyScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()
