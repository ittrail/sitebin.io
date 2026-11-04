// The analytics opt-out page's choice, mirrored into the flag Sitebin's
// script and Umami's both read: localStorage["umami.disabled"]. The page
// loads this file (data-state="out" or "in") rather than an inline script,
// which a site's own origin may refuse.
(() => {
  'use strict';
  const me = document.currentScript;
  const state = me && me.getAttribute('data-state');
  try {
    if (state === 'out') localStorage.setItem('umami.disabled', '1');
    else if (state === 'in') localStorage.removeItem('umami.disabled');
  } catch (e) {
    // No storage here: the cookie the page just set still keeps server-side
    // counting out.
  }
})();
