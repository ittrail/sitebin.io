# Account zones: implementation plan

Design: `docs/superpowers/specs/2026-09-23-account-zones-design.md`. Approved
in chat on 2026-09-23 ("ja genau so umsetzen"). Executed the same day.

1. **Core store: zone registry** (`internal/store/zones.go`).
   - `Zone`, verified at `data/zones/<zone>.json` and pending at
     `data/zones/pending/<zone>~<account>.json`.
   - `ClaimZone` checks overlaps (reserved, operator, verified zones of either
     account), then the plan count, then verifies at once.
   - `checkZone` combines the TXT proof with the conflict scan of the domain
     index.
   - Promotion re-checks overlaps under the lock.
   - `ReconcileZones` runs the pending TTL, drops losing claimants, re-checks
     daily and revokes after 72h. A lookup error changes nothing.
   - `ReleaseZone`, `ReleaseZones`, `Zones`, `ZoneOf` and `ZoneDomains` round
     out the API.
   - `DNSVerifier.VerifyZone` does a TXT lookup; `TrustingVerifier.VerifyZone`
     always proves.
2. **Wire into the domain path.**
   - `AddDomain` calls `refuseForeignZone`, which runs the operator gate, then
     the account-zone gate, the plan check and the per-hour throttle.
   - The per-site cap skips zone names (`countedClaims`).
   - `store.verify` answers account-zone names by owner.
   - `ReconcileDomains` drops foreign pending claims inside a verified zone.
   - `cleanup.Sweep` runs `ReconcileZones` before the per-site pass.
3. **Config and seam.**
   - `SITEBIN_ZONE_NAMES_PER_HOUR` defaults to 50.
   - `ext.ZoneAccounts` is added.
   - `ext.SiteService` gains `ClaimZone`, `Zones`, `ReleaseZone` and
     `ReleaseZones` (`internal/httpapi/zones.go`).
   - The site payload gains `zone_domains`.
   - `main.go` installs `SetZoneCheck` and `SetZoneNamesPerHour`.
4. **EE.**
   - The tier gets `max_zones`.
   - `provider.ZonesAllowed` uses the strict tier lookup.
   - The account page gets a Zones section with claim, check-now and remove
     (`POST /account/zones` and `/account/zones/{zone}/delete`, session and
     CSRF only).
   - Both deletion paths (dashboard and GDPR) release the account's zones.
5. **Edit page.** Names held through a zone show "via zone …".
6. **Tests.**
   - Store: attach, closed to others, overlaps, plan, two claimants,
     conflicts, pending drop, same-sweep attach, revocation and fallback, cap
     exemption, throttle, downgrade, TTL and release, no zone verifier,
     trusting verifier.
   - httpapi: the seam.
   - ee: tier, section visibility, session/CSRF/token, deletion.
   - config: the throttle.
7. **Docs.** README (custom domains, env table, tier zeros), CLAUDE.md, and a
   corrections block in the design.
8. **Ship.**
   - Push the product.
   - Deploy app.sitebin.io, then add `max_zones` to `tiers.json` (studio 3,
     unlimited/admin 1000).
   - Verify with a real zone.
   - Then the website: pricing, `/docs/custom-domains/` and `/docs/enterprise/`.
