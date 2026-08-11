# Tier Lifetimes & API Gating — Design

Date: 2026-08-11 · Status: Approved for implementation

Reshape the hosted plan boundaries so that an account — not a payment — is the
line that separates a throwaway drop from a working tool:

| | Drop (anonymous) | Free (account) | Pro | Studio |
|---|---|---|---|---|
| Lifetime | **24 h, fixed** | **7 days, sliding from the last change** | unlimited | unlimited |
| Claim ticket | ✓ | ✓ | ✓ | ✓ |
| API | **✗** | ✓ | ✓ | ✓ |

The rationale is abuse control as much as pricing: anonymous content lives one
day and cannot be automated, while anything that survives longer is tied to an
identity. Paid tiers keep sites until the owner deletes them.

This is a greenfield change. No migration, no grandfathering, no
compatibility shims — any sites that exist when it ships are deleted.

## 1. Lifetimes

`max_expiry_days` already expresses all three cases, so the tier schema is
unchanged. The hosted `tiers.json` becomes:

- `drop` — `"max_expiry_days": 1`
- `free` — `"max_expiry_days": 7`
- `pro`, `studio` — `"max_expiry_days": 0` (unlimited)

A tier cap already doubles as the default lifetime at creation
(`internal/httpapi/sites.go`, the `QuotaExpiryDays > 0 && ExpiresAt == nil`
branch), so a drop is stamped `now + 24 h` and a free site `now + 7 d` without
further work.

### 1.1 Sliding renewal

A site whose meta carries **an owner account**, a positive `QuotaExpiryDays`
and a non-nil `ExpiresAt` has its expiry pushed to `now + QuotaExpiryDays·24 h`
whenever its content changes. Anonymous drops are deliberately excluded: a
phishing page must not be able to keep itself alive by rewriting a file every
23 hours.

Implementation: `Store.RenewExpiry(site *Site) error` in `internal/store`,
called from the write paths — `saveFileLocked`, `DeleteFile`, `ClearFiles`,
`ExtractZip` — and from `applySettings` in `internal/httpapi/sites.go`. Placing
it in the store means WebDAV and FTP writes are covered by construction, since
both go through the same file operations.

To avoid rewriting `meta.json` once per file during a 500-file upload,
`RenewExpiry` is a no-op when the newly computed expiry is within one minute of
the stored one.

### 1.2 Clearing an expiry

`PUT {"expires_at": null}` currently clears the expiry unconditionally
(`internal/httpapi/sites.go`, the `raw == "null"` branch), which lets an
anonymous drop make itself permanent from its own edit page. Clearing is
allowed only when the effective cap (per-site `QuotaExpiryDays`, else
`SITEBIN_MAX_EXPIRY_DAYS`) is `0`; otherwise the request fails with `400` and
the same wording the cap violation already uses.

## 2. API gating

The rule in code is: **a site with no owning account has no API**, enforced only
where a provider gates creation at all. The community build has no provider,
so self-hosted instances stay fully open — no new environment variable, no new
tier flag, no new meta field. `Meta.OwnerAccountID == ""` already carries the
distinction.

Two enforcement points:

- `POST /api/sites` — when creation is gated and the resolved grant has no
  owner, the request must carry browser fetch metadata: a `Sec-Fetch-Site`
  header, plus an `Origin` that is either the instance itself or an allowlisted
  embed origin. Otherwise `401`, "sign in to use the API".
- `GET|PUT|POST|DELETE /api/sites/{editID}/…` — when accounts are enabled and
  the site has no owner, the same requirement applies, otherwise `403` carrying
  an upgrade hint. The edit page keeps working because a browser always sends
  both headers; `curl` does not.

**This is a plan boundary, not a security boundary.** `Sec-Fetch-Site` and
`Origin` are forgeable with `curl -H`. The gate stops CI pipelines, agents and
bulk uploaders — the honest majority — and is worth exactly that much. A real
boundary would require withholding the edit password from anonymous sites,
which was considered and rejected: the claim ticket stays.

Consequence to accept: an account holder's API credential remains the per-site
edit password from the claim ticket. Creating a *new* site from a script
without a browser session is therefore not possible for account holders either.
Account-level API tokens are out of scope here.

## 3. Product copy

- Claim ticket (`web/static/app.js`, `web/static/embed.js`) states the site's
  lifetime — "expires in 24 hours" / "expires in 7 days, renewed on every
  change" / no expiry line when unlimited.
- Edit page (`web/static/edit.js`) shows the cap next to the expiry field and
  hides "Clear" when a cap applies.
- Start page (`web/static/index.html`) marks the `curl` hint as needing an
  account.
- `README.md`: tier-cap semantics (default lifetime, sliding renewal, clearing)
  and the API-requires-an-account rule.
- `deploy/docker-compose.example.yml`: tier example updated to the new numbers.

## 4. Website (`C:\Projects\Sitebin-Website`)

- `/pricing/` — Drop card: "7 days" → "24 hours", "full API + claim ticket" →
  claim ticket only, API listed as excluded. Free card: "never expire" → "7
  days, renewed on every change", API listed. Comparison table rows *Site
  lifetime* and *API + CI deploy*. Two FAQ answers ("What happens when a Drop
  site expires?", "Do anonymous sites need an account later?") plus the
  paragraph under the plan cards claiming anonymous sites never lock you out.
- Sweep `index.html`, `/docs/api/`, `/docs/using/`, `/docs/embed/` for the same
  claims, which are repeated in several places.

## 5. Tests

Go tests in `internal/httpapi` and `internal/store`:

- anonymous site + `X-Edit-Password` without fetch metadata → `403`
- same request with `Sec-Fetch-Site` + instance `Origin` → `200`
- owned site via plain `curl`-shaped request → `200`
- anonymous `POST /api/sites` without fetch metadata → `401`; from an
  allowlisted embed origin → `201`
- write to an owned, capped site pushes `ExpiresAt` forward
- write to an anonymous capped site leaves `ExpiresAt` untouched
- `RenewExpiry` does not rewrite meta when the delta is under a minute
- `PUT {"expires_at": null}` on a capped site → `400`; on an uncapped site →
  cleared

## 6. Deployment

`tiers.json` on the hosted instance (`/opt/sitebin/`) must be updated to the
numbers in §1 and the container restarted. Website and product ship
independently; the website copy must not go live before the instance carries
the new caps.
