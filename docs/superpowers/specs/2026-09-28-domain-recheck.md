# A fresh domain verifies as soon as its record exists — and Check now says what it saw

2026-09-28. Fixes the report "`www.physioprint.org` stays pending although its
TXT record exists; when is it re-checked?" (container site
`iw5ogd6bpx7fjdpno25bdhtf5m`).

## What happened

- `physioprint.org` was **registered at 21:17:40 UTC** (RDAP) and added to the
  site at 21:20:16, the TXT record created a few minutes later. Hetzner's three
  servers answered the TXT authoritatively; `1.1.1.1` saw it.
- The instance's resolver (`192.103.100.1`, the hosting provider's; Docker's
  127.0.0.11 forwards to it) answered **NXDOMAIN for every name under
  `physioprint.org`**, with the `.org` SOA — negative TTL 3600 s. The domain
  had been looked up before `.org` published its delegation: by the add-time
  check and the first sweep, whose nameserver discovery goes through that
  resolver.
- `authDNS.servers()` finds a name's nameservers through the system resolver
  and walks up one label per miss. With the registrable domain itself
  negatively cached it stepped past `physioprint.org`, and its loop
  (`strings.Contains(cand, ".")`) stopped before the TLD. `errNoAuthority` —
  and `withFallback` then asked the same resolver, whose cached NXDOMAIN
  counts as a *definitive* "no". Every check for an hour — the sweep's and
  any "Check now" — said "not there". f4eaa5b made the TXT lookup itself
  skip the cache; the nameserver discovery in front of it did not.
- Separately, a container site had **no Check now at all**: the edit page
  hides the domain card in container mode, and the container view printed the
  record without a button. The API already accepted a re-check there. A
  restart re-syncs the compose domains but skips claims it already has, so it
  re-checks nothing. And every "not verified" said only "the record is not
  visible", whatever DNS had shown.

## Rules

1. **Delegations are followed.** The nameserver walk goes up to the TLD, and
   a non-authoritative answer that delegates a zone deeper on the way to the
   name (NS in the authority section, owner a proper descendant of the zone
   asked and an ancestor-or-self of the name) is followed to that zone's
   servers, at most `maxReferrals` times. Glue is used only for a nameserver
   inside the zone of the server that sent it (the bailiwick rule); other
   nameserver names are resolved as before. A referral sideways or upwards is
   treated as "not authoritative". So a domain the resolver believes does not
   exist is still asked of its own servers, via the registry's delegation.
2. **A check says what it found.** `DNSVerifier` also implements
   `DomainExplainer`: for a definitive "no" it returns a sentence the owner
   can act on — no TXT record at the name (and which nameserver said so), a
   TXT record with another value (shown, truncated), a CNAME pointing
   elsewhere. The store records it on the pending claim (`check_result`,
   `checked_at` — every attempt of a pending claim, lookup errors included;
   verified claims keep "ran to completion" semantics because their schedule
   hangs off it). The API returns both in `pending_domains[]`; the edit page
   shows them; the MCP `add_domain` result carries it in `warnings` (no new
   output field: clients with cached output schemas reject unknown ones).
3. **Container sites get Check now**, in the container view beside each
   pending route, posting the same `POST /domains` the domain card uses.
4. **User-asked checks are rate-limited** in one helper both surfaces call
   (`API.claimDomain`): per claim (burst 3, then one a minute) and per site
   (burst 10, then 2 a minute). Past it: `429`, "checked a moment ago; the
   instance also checks every N minutes". The sweep is not limited (it is
   one pass per interval). The container rule (a container site's domains
   come from its compose file; POST only re-checks one it already claims)
   moves into the same helper, so MCP now refuses a new domain on a container
   site exactly as the API does.

## Not changed

- A *stale* nameserver set after a DNS-provider move (the resolver still
  knows the old servers, which still answer authoritatively) is not solved:
  the walk starts from what the resolver knows. The Check now result names
  the server that said "no", which makes that case diagnosable.
- The sweep interval (10 min) and the 7-day pending TTL.
