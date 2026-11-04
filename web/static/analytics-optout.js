// The analytics opt-out page's choice, mirrored into the flag Sitebin's
// script and Umami's both read: localStorage["umami.disabled"]. The page
// loads this file (data-state="out" or "in") rather than an inline script,
// which a site's own origin may refuse.
//
// The page has just set or cleared the cookie itself, so the script's
// markers of what it last mirrored for this prefix are dropped too: the next
// change of the flag (a site's own ?statistik=aus, say) is mirrored again.
(() => {
  'use strict';
  const me = document.currentScript;
  const state = me && me.getAttribute('data-state');
  const ASSET = '/_sitebin/assets/static/analytics-optout.js';
  let mount = '';
  try {
    const path = new URL(me.src, location.href).pathname;
    if (path.endsWith(ASSET)) mount = path.slice(0, -ASSET.length);
  } catch (e) {
    // No script element to read: the origin's root.
  }
  const mark = 'sitebin.optout.mirrored' + (mount ? ':' + mount : '');
  for (const kind of ['localStorage', 'sessionStorage']) {
    try {
      const s = window[kind];
      if (!s) continue;
      if (kind === 'localStorage' && state === 'out') s.setItem('umami.disabled', '1');
      if (kind === 'localStorage' && state === 'in') s.removeItem('umami.disabled');
      s.removeItem(mark);
    } catch (e) {
      // No storage here: the cookie the page just set still keeps
      // server-side counting out.
    }
  }
})();
