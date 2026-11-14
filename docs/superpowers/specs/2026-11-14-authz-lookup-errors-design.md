# authz: a lookup that cannot read is not "no site" — design

Date: 2026-11-14. Status: approved by the operator ("damit wir in zukunft besser
rein sehen").

## Why

On 2026-11-14 between 18:29:04 and 18:30:29 UTC every monitored custom domain on
the hosted instance answered **404** for about 70 seconds. Nothing was in any
log:

- `authz` (Caddy's `forward_auth` on the internal listener) has no request log.
- `ResolveAddress` turns every error `load` returns into `ErrNotFound`, and
  authz answers any error with the "Site not found" page.

The investigation ruled out a restart, the disk state, resources, the network
and the monitor. What made the lookup fail is still unknown, because the one
place that knew the reason threw it away. An unreadable `meta.json` (I/O error,
permissions, a torn write by another writer) looks exactly like a deleted site.
Uptime monitors then report "404", which says "gone", while "temporarily
unavailable" is what actually happened.

## Rules

1. **Not found is only "nothing is there".** `store.ResolveAddress` returns
   `ErrNotFound` only when no address is attached, or when the index link leads
   to no site. Both cases are wrapped with that reason
   (`site not found: nothing is attached at x.at`,
   `site not found: the index link of x.at leads to no site`), and
   `errors.Is(err, ErrNotFound)` still holds. Every other error from `load` is
   returned wrapped with the address: unreadable or corrupt `meta.json`, an I/O
   error, a permission error.
2. **A path address whose site cannot be read is an error, never a fallback.**
   Falling back to a shorter path or to the host would serve another site's
   files at that path. A dangling path link (the site is gone) still falls back,
   as before.
3. **authz answers a lookup error with 503**, never 404:
   - `Retry-After: 10`, and `no-store` through `renderPage`;
   - the page "Temporarily unavailable — This site cannot be loaded right now.
     Try again in a moment.";
   - this holds for every branch of authz: custom domains, generated addresses
     and path views.

   Caddy relays the answer as it is, because a non-2xx from `forward_auth` is
   not an error, so `handle_errors 404` does not apply.
4. **Lookup errors are logged at once, rate-limited.** Each one is logged at
   ERROR: `authz: site lookup failed, answered 503`, with `host`, `err` and
   `suppressed`. At most one line per 10 seconds; the errors in between are
   counted and reported in the next line and in the minute report.
5. **Misses are reported once a minute.** authz counts its "site not found"
   answers, in a mutex-guarded tally that never blocks and never does I/O:
   - **per custom-domain host**, with the reason, at most 1000 distinct hosts
     per window, the rest counted as `others`;
   - **one total for generated addresses and path views.** Bots probe random
     labels of the view domain, so these are counted but never listed.

   `API.RunAuthzReport(ctx)` (started by `sitebin run`) logs one INFO line per
   minute (`authz: answered site not found`, with `since`, `until`, `custom`,
   `distinct` (how many hosts missed, listed or not), `hosts`, the 10 most
   frequent as `host=n (reason)`, `others`, `generated` and `lookup_errors`),
   but only when the window counted a custom-domain miss or a
   lookup error. A custom domain only reaches authz with a certificate that
   on-demand TLS issued for an attached host, so in normal operation the line is
   rare, and every one is worth reading. It flushes once more on shutdown.
6. **Nothing else changes.** 401, 410 and redirects answer as before. So do the
   backend's own resolvers (`servingSiteByAddress`: forms, unlock, analytics),
   which already pass `load`'s errors through and answer them on their own
   routes. No IP address, user agent or path is logged. Hosts are logged
   lowercased, without a port, cut at 253 characters.

## Not done

- A Caddy access log. The volume is high, and Caddy's 4xx handler errors stay
  at debug. The two lines above cover what authz decides.
- Retrying the read inside authz. A retry hides the cause; the 503 plus the log
  line show it, and the client's own retry (Retry-After) is the retry.

## Tests

- store: a custom domain whose `meta.json` is unreadable is an error that is not
  `ErrNotFound`. A path address whose site is unreadable does not fall back to
  the host's site. An unknown host, and a dangling host link, are `ErrNotFound`
  with their reasons. The existing longest-prefix and dangling-path cases are
  unchanged.
- httpapi: authz on an unreadable custom domain answers 503 with `Retry-After`
  and `no-store`; an unknown host still answers 404. The tally counts custom
  hosts with reasons, counts generated misses without listing them, bounds the
  host map, stays silent for a window of generated misses only, and reports
  suppressed lookup errors.
