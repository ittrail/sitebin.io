// Sitebin analytics: Umami's tracker API, sending only to this site's own
// origin.
//
//   <script defer src="/_sitebin/analytics.js"></script>
//
// Under a custom domain with a path, load it from below that path
// (<path>/_sitebin/analytics.js): it sends to the same prefix. Where the
// site's analytics is not in effect, the same URL serves a stub whose
// umami.track and umami.identify do nothing.
//
// Attributes on its own <script> tag, as Umami reads them:
//   data-auto-track="false"     no automatic page views, SPA route changes or
//                               data-umami-event clicks; call umami.track()
//   data-domains="a.com,b.com"  send only on these hostnames
//   data-tag="x"                sent with every record as its tag
//   data-exclude-search="true"  the page URL without its query
//   data-exclude-hash="true"    the page URL without its fragment
//   data-do-not-track="true"    send nothing when the browser asks not to be
//                               tracked
//   data-before-send="fn"       window.fn(type, payload) returns the payload to
//                               send (or a promise of it); falsy drops it
//   data-website-id, data-host-url: accepted and ignored.
//
// umami.track() sends a page view; track(name), track(name, data),
// track({...payload}) and track(fn), fn(defaults) returning the payload,
// send what Umami's would. umami.identify() does nothing.
//
// Nothing is sent while localStorage["umami.disabled"] is set (a visitor's
// opt-out), nor from an automated browser (navigator.webdriver). The opt-out
// is mirrored into the site's opt-out cookie, so server-side counting skips
// this browser too.
//
// See docs/superpowers/specs/2026-11-04-site-analytics-design.md, "The script".
(() => {
  'use strict';

  const SELF = '/_sitebin/analytics.js';
  const script = document.currentScript || document.querySelector('script[src*="' + SELF + '"]');
  const attr = (name) => (script && script.getAttribute('data-' + name)) || '';

  // The site's prefix: whatever comes before /_sitebin/analytics.js in this
  // script's own URL, when it was loaded from this origin.
  let mount = '';
  try {
    const src = new URL(script.src, location.href);
    if (src.origin === location.origin && src.pathname.endsWith(SELF)) {
      mount = src.pathname.slice(0, -SELF.length);
    }
  } catch (e) {
    // No script element to read: the origin's root.
  }
  const sendURL = mount + '/_sitebin/analytics/send';
  const optOutURL = mount + '/_sitebin/analytics/opt-out';

  const autoTrack = attr('auto-track') !== 'false';
  const domains = attr('domains').split(',').map((d) => d.trim().toLowerCase()).filter(Boolean);
  const tag = attr('tag') || undefined;
  const excludeSearch = attr('exclude-search') === 'true';
  const excludeHash = attr('exclude-hash') === 'true';
  const honourDNT = attr('do-not-track') === 'true';
  const beforeSend = attr('before-send');

  // Storage may be missing, or throw on access (privacy modes, sandboxed
  // frames): then it holds nothing.
  const storage = (kind, use) => {
    try {
      const s = window[kind];
      return s ? use(s) : null;
    } catch (e) {
      return null;
    }
  };
  const flagged = () => !!storage('localStorage', (s) => s.getItem('umami.disabled'));

  const doNotTrack = () => {
    const v = window.doNotTrack || navigator.doNotTrack || navigator.msDoNotTrack;
    return v === 1 || v === '1' || v === 'yes';
  };
  const disabled = () =>
    flagged() ||
    navigator.webdriver === true ||
    (honourDNT && doNotTrack()) ||
    (domains.length > 0 && !domains.includes(location.hostname.toLowerCase()));

  const normalize = (raw) => {
    try {
      const u = new URL(raw, location.href);
      if (excludeSearch) u.search = '';
      if (excludeHash) u.hash = '';
      return u.toString();
    } catch (e) {
      return raw;
    }
  };
  let currentURL = normalize(location.href);
  let currentRef = document.referrer;

  const defaults = () => ({
    hostname: location.hostname,
    language: navigator.language,
    referrer: currentRef,
    screen: `${screen.width}x${screen.height}`,
    title: document.title,
    url: currentURL,
    tag,
  });

  // post never throws and never rejects: a page's own code goes on whatever
  // the network does. keepalive lets a request outlive the page.
  const post = (url, body, type) => {
    try {
      return fetch(url, { method: 'POST', keepalive: true, body, headers: { 'Content-Type': type } })
        .then(() => undefined, () => undefined);
    } catch (e) {
      return Promise.resolve();
    }
  };

  const send = (payload, type = 'event') => {
    if (disabled()) return Promise.resolve();
    const hook = beforeSend && window[beforeSend];
    return new Promise((resolve) => resolve(typeof hook === 'function' ? hook(type, payload) : payload))
      .then((p) => {
        // text/plain: never a preflight.
        if (p) return post(sendURL, JSON.stringify({ type, payload: p }), 'text/plain');
        return undefined;
      }, () => undefined);
  };

  const track = (name, data) => {
    if (typeof name === 'string') return send({ ...defaults(), name, data });
    if (typeof name === 'object' && name) return send({ ...name });
    if (typeof name === 'function') return send(name(defaults()));
    return send(defaults());
  };
  const identify = () => Promise.resolve();

  if (!window.umami) window.umami = { track, identify };

  // The visitor's opt-out, mirrored into the site's opt-out cookie, which is
  // HttpOnly, so markers stand in for reading it: while the flag is set the
  // cookie is set once per tab session (it may have expired or been
  // cleared); the first load after the flag is gone clears it, from any tab.
  // Per prefix: the cookie is the prefix's, the flag the whole origin's.
  const MARK = 'sitebin.optout.mirrored' + (mount ? ':' + mount : '');
  const marked = (kind) => !!storage(kind, (s) => s.getItem(MARK));
  const mark = (kind, on) => storage(kind, (s) => (on ? s.setItem(MARK, '1') : s.removeItem(MARK)));
  const mirrorOptOut = () => {
    const out = flagged();
    if (out ? marked('sessionStorage') : !marked('sessionStorage') && !marked('localStorage')) return;
    post(optOutURL, 'action=' + (out ? 'out' : 'in'), 'application/x-www-form-urlencoded');
    mark('sessionStorage', out);
    mark('localStorage', out);
  };
  mirrorOptOut();

  if (!autoTrack) return;

  // Clicks on [data-umami-event]: the event is sent as the click starts
  // (capture), so a handler that stops the click cannot lose it. Whether a
  // link's navigation waits for it is decided once the page's own handlers
  // have run (bubble): a router that handled the click is left alone. A
  // same-tab link waits for the send, never longer than 300 ms.
  let pending = null;
  const onClickStart = (e) => {
    pending = null;
    const el = e.target && e.target.closest ? e.target.closest('[data-umami-event]') : null;
    const name = el && el.getAttribute('data-umami-event');
    if (!name || disabled()) return;
    const data = {};
    for (const attrName of el.getAttributeNames()) {
      if (attrName.startsWith('data-umami-event-')) {
        data[attrName.slice('data-umami-event-'.length)] = el.getAttribute(attrName);
      }
    }
    pending = { event: e, sent: track(name, data) };
  };
  const sameTab = (e, link) =>
    e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey &&
    (!link.target || link.target === '_self') && !link.hasAttribute('download') &&
    /^https?:$/.test(link.protocol);
  const onClickEnd = (e) => {
    const p = pending;
    pending = null;
    if (!p || p.event !== e || e.defaultPrevented) return;
    const link = e.target.closest('a[href]');
    if (!link || !sameTab(e, link)) return;
    e.preventDefault();
    const href = link.href;
    let gone = false;
    const go = () => {
      if (!gone) {
        gone = true;
        location.href = href;
      }
    };
    p.sent.then(go);
    setTimeout(go, 300);
  };
  window.addEventListener('click', onClickStart, true);
  window.addEventListener('click', onClickEnd);

  // SPA route changes. A history entry that only moves to an anchor on the
  // same page (popstate fires for those too) is no page view. The view is
  // sent a moment later, once the page has set its new title.
  const route = (popped) => {
    const next = normalize(location.href);
    if (next === currentURL) return;
    const anchorOnly = popped && next.split('#')[0] === currentURL.split('#')[0];
    if (!anchorOnly) {
      const view = { url: next, referrer: currentURL };
      setTimeout(() => send({ ...defaults(), ...view }), 300);
      currentRef = currentURL;
    }
    currentURL = next;
  };
  const wrap = (method) => {
    const orig = history[method];
    if (typeof orig !== 'function') return;
    history[method] = function (...args) {
      const result = orig.apply(this, args);
      route(false);
      return result;
    };
  };

  // The first page view once the page has loaded, as Umami waits; route
  // tracking from then on.
  let started = false;
  const start = () => {
    if (started || document.readyState !== 'complete') return;
    started = true;
    document.removeEventListener('readystatechange', start);
    currentURL = normalize(location.href);
    track();
    wrap('pushState');
    wrap('replaceState');
    window.addEventListener('popstate', () => route(true));
  };
  document.addEventListener('readystatechange', start);
  start();
})();
