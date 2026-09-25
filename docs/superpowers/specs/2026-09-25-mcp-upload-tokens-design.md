# Upload tokens: large files outside the MCP call

**Date:** 2026-09-25
**Status:** draft — awaiting review

## Problem

An MCP tool receives file content as a JSON argument, and a model produces
that argument the only way it produces anything: as output tokens. A 1.6 MB
file is roughly 400,000–500,000 tokens — far beyond what a model can emit in
one response, and not something it could copy verbatim even if it could.
Sitebin's own limit (`mcp.MaxContentBytes`, 8 MiB per call) is never reached;
the agent gives up long before. In the case that prompted this, it improvised
instead: turn WebDAV on and generate "a random password" for it — but WebDAV
authenticates with the *edit* password, and MCP can only set a *view*
password, which would have locked visitors out rather than let the agent in.

`write_files` also cannot be chunked: it writes whole files, so splitting a
large file across calls is not possible.

What the agent usually *does* have is a way to make HTTP requests of its own —
a shell with `curl`, or code execution with network access. The content never
needs to pass through the model; only a credential does.

## What changes

1. **A new MCP tool, `open_upload`,** hands the agent a short-lived **upload
   token** for one site, the URLs to use it on, and ready-to-run `curl`
   examples.
2. **The token is accepted in exactly two places**: the site's WebDAV tree
   (`/dav/{editID}/`) and the JSON API's file upload for that site
   (`POST /api/sites/{editID}/files`). Everywhere else it is refused.
3. **Nothing is taken away.** `create_site` and `write_files` keep carrying
   file content, and remain the right tool for small files and for agents that
   cannot make HTTP requests. This is purely additive: no saved connector
   configuration breaks.

## The tool

`open_upload` takes the usual site address — `edit_id`, and `edit_password`
unless the connection's account token owns the site — and nothing else. It is
authorized by `mcpOps.openSite`, exactly like `write_files`, and needs the
`sitebin:sites:write` scope. Whoever may write files through MCP may open an
upload, and nobody else.

It deliberately takes no `replace` flag: clearing the site belongs to the
request that carries the new content (`?replace=true` on the upload), so a
failed or abandoned upload can never leave a site emptied by an earlier call.

Result (`mcp.UploadResult`):

```json
{
  "edit_id": "…",
  "view_url": "https://….sitebin.app",
  "token": "sbu_…",
  "upload_url": "https://app.sitebin.io/api/sites/<edit_id>/files",
  "webdav_url": "https://app.sitebin.io/dav/<edit_id>/",
  "idle_timeout_seconds": 300,
  "expires_at": "2026-09-25T13:00:00Z",
  "examples": [
    "curl -H 'Authorization: Bearer sbu_…' -F 'zip=@dist.zip' 'https://app.sitebin.io/api/sites/<edit_id>/files?replace=true'",
    "curl -H 'Authorization: Bearer sbu_…' -F 'files=@video.mp4;filename=media/video.mp4' 'https://app.sitebin.io/api/sites/<edit_id>/files'",
    "curl -H 'Authorization: Bearer sbu_…' -T big.bin 'https://app.sitebin.io/dav/<edit_id>/big.bin'"
  ]
}
```

- `webdav_url` and the WebDAV example are **omitted when WebDAV is off
  instance-wide** (`SITEBIN_WEBDAV_ENABLED=false`). The upload URL alone covers
  every case; WebDAV adds listing, moving and deleting.
- The examples carry the real token and real URLs. They are the most useful
  part of the result: an agent copies a working command more reliably than it
  assembles one from a schema. The token appearing twice adds no exposure — it
  is in the result either way.
- The multipart example matters: `files` parts may carry a path in their
  filename and the store creates the folders, so a nested file needs no
  WebDAV `MKCOL` first. A whole directory goes as one zip.

**Tool description** (what the agent reads when choosing): *"Get a
short-lived token and URLs to upload files with your own HTTP client, for
files too large to pass to write_files (anything beyond a few hundred KB).
Only useful if you can run curl or make HTTP requests; otherwise use
write_files. The token expires 5 minutes after its last use."*

Three existing texts point at it, because that is where a stuck agent looks:

- the server `Instructions` gain one sentence: large files go through
  `open_upload`;
- `write_files`'s description names `open_upload` for large files;
- `DecodeFiles`' over-size error says "call open_upload" instead of listing
  WebDAV, FTP and the zip upload, which an agent cannot use without a
  credential it does not have.

## The upload token

**Format.** `sbu_` + 40 base62 characters (~238 bits), from
`ids.NewUploadToken`, the same generator and length as account API tokens
(`sbp_`). The distinct prefix lets secret scanners recognise it, and lets
every credential check tell it apart before doing anything else.

**Storage: memory only, hash only.** An `uploadTokens` registry in
`internal/httpapi` maps `sha256(secret)` to a record:

```
viewID, editID      the one site it opens
issuedAt            for the absolute cap
lastSeen            for the idle timeout
inflight            requests currently using it
```

The secret itself is never stored or logged. Nothing touches disk: a token
that lives minutes does not belong in the filesystem database, and a restart
simply invalidates every token — the agent calls `open_upload` again.

**Lifetime.**

- **Idle timeout: 5 minutes, measured from the *end* of the last request.**
  A request marks the token in use when it starts and stamps `lastSeen` when
  it finishes, and a token with a request in flight never idles out. A
  10-minute upload of a large file therefore cannot lose its token halfway,
  and the 5 minutes start once it is done.
- **First use:** `lastSeen` starts at issuance, so an unused token dies after
  5 minutes.
- **Absolute cap: 60 minutes from issuance.** Without one, a token that leaked
  (the result sits in the agent's transcript and the model provider's logs)
  could be kept alive indefinitely by touching it every few minutes. A request
  already running at the cap completes; the next is refused.
- Both durations are named constants, not configuration.

**Validity** is checked once, when a request starts:
`now < issuedAt + 60m` and (`inflight > 0` or `now < lastSeen + 5m`).

**Revocation.** A site's tokens are dropped when

- the edit password is rotated (`siteService.RotateEditPassword`, next to the
  existing `verifyCache.Drop`) — rotating is how an owner cuts off whoever held
  access, and a token issued under the old password must not survive it;
- the site is deleted (the delete paths that already call
  `verifyCache.Drop`: the JSON API, MCP and the extension's site service) —
  belt and braces, since the cleanup sweep deletes through the store directly
  and a lookup of a vanished site fails anyway;
- the process restarts.

Revoking the *account* API token that opened an upload does not revoke the
upload token; it expires on its own within the limits above. There is no hook
for it, and 5 minutes idle / 60 minutes absolute bounds the gap.

**Caps.** At most **5 live tokens per site**; opening a sixth evicts the
oldest. A global backstop of **10,000 live tokens** refuses new ones with
"too many open uploads, try again later". Expired records are pruned lazily
on every issue and lookup.

**Logging.** Issuance is logged with the site's view id and the token's first
10 characters (`sbu_` plus six, as the dashboard shows `sbp_` tokens). The
secret never appears in a log line.

## Where the token is accepted

A credential starting with `sbu_` is answered **only** as an upload token. It
never falls back to being tried as an edit password or an account token: a
wrong or expired upload token is a `401`, not a slower path to something
weaker. It is read from `Authorization: Bearer`, or as the password of HTTP
Basic auth (username ignored) — the only thing many WebDAV clients can send.
Never from the URL: URLs end up in logs, headers do not.

### WebDAV — `/dav/{editID}/…`

The handler checks for an upload token before the edit password. With a valid
token for *this* site:

- **The per-site toggle (`webdav_enabled`) and the plan's WebDAV flag
  (`quota_webdav`) are ignored.** This is an upload channel for the agent, not
  the site's network drive: it grants nothing `write_files` does not already
  grant, and the toggle stays exactly as the owner set it.
- **The instance switch is respected.** With `SITEBIN_WEBDAV_ENABLED=false`
  the route stays a `404`, and `open_upload` does not advertise it.
- `gatedAnonymous` is re-checked, although a token can only be issued for a
  site MCP could open: the rule is cheap and belongs on every entry.
- Everything after authentication is the existing handler unchanged: quota
  and file-count checks, `siteFS` path validation through `os.Root`, per-site
  locking, expiry renewal, viewer regeneration, container restarts.

A token for another site is a `401`, the same answer as a wrong token: a
token is bound to one site, not to "whatever site the URL names".

### JSON API — `POST /api/sites/{editID}/files` only

That route gets its own wrapper, `withUploadAuth`: an `sbu_` credential is
checked as an upload token for this site, and anything else goes to
`withEditAuth` unchanged. `uploadFiles` itself does not change — `zip` parts
are extracted, `files` parts stored at their path, `?replace=true` clears
first.

On every other `withEditAuth` route an `sbu_` credential is refused with
`403 "an upload token can only upload files — use the edit password or an
account API token for anything else"`, before any Argon2 verification, so it
neither burns the password rate limit nor reads as a wrong password.

Refused therefore: reading or changing settings, deleting the site, domains,
forms, containers, downloading the site as a zip, and creating sites.

## Error handling

| Situation | Answer |
|---|---|
| `open_upload` without write authority | the same errors `write_files` gives |
| too many live tokens instance-wide | tool error "too many open uploads, try again later" |
| upload with unknown, expired or foreign-site token | `401` |
| upload token on any other API route | `403` with the sentence above |
| WebDAV off instance-wide | `404` on `/dav/`, and no `webdav_url` in the result |
| over the site's size or file quota | unchanged: `507` on WebDAV, the store's error on the API |
| site deleted meanwhile | `404` |

## What stays where

All of it is core (MIT): WebDAV, the JSON API and MCP already are, and the
token authorizes nothing an edit password does not. There is no `ext` seam
change. In the community build the tool works with the edit password, like
every other tool; in the enterprise build with an account token as well.

## Alternatives considered

- **A pre-signed URL with the token in the path** (S3-style). Easiest for an
  agent — no header — but the secret lands in every access log and proxy on
  the way. The header costs the agent one flag.
- **Chunked append through MCP** (`append_file`). The content still travels
  as model output, so a 1.6 MB file is still 400k+ tokens, just spread across
  calls. It solves nothing.
- **Sitebin fetches from a URL the agent names** (`import_from_url`). Needs
  the file hosted somewhere reachable first, and makes Sitebin an SSRF
  vector that needs its own egress rules. Not now.
- **Handing the agent the edit password for WebDAV.** It already has it after
  `create_site` — permanently. A token that dies after five idle minutes is
  strictly less to lose.

## Testing

- **Registry unit tests** with an injected clock: issue and look up; idle
  expiry at 5 minutes; a request in flight keeps the token alive past 5
  minutes, and the idle timer restarts at its end; the 60-minute cap refuses
  new requests but not one in flight; the sixth token evicts the oldest; the
  global backstop; revocation by site; only the hash is held.
- **httpapi tests:** WebDAV PUT with a token on a site whose `webdav_enabled`
  is false succeeds; the instance switch off gives `404`; Basic auth with the
  token works; a token for site A on site B is `401`; an expired token is
  `401` and does not fall back; `POST …/files` with `zip` and `replace=true`
  works; the token on `PUT /api/sites/{edit}`, `DELETE`, download and domains
  is `403` without touching the auth limiter; rotating the edit password kills
  the token.
- **MCP server tests:** `open_upload` needs the write scope; a read-only
  session is refused; the result omits `webdav_url` when WebDAV is off.
- **Enterprise:** an account token that owns the site opens an upload without
  an edit password; one that does not own it is refused.
- **E2E** (`e2e/mcp.ps1`, community image, ASCII only): `open_upload`, then
  `curl` a 9 MiB file — above the MCP content cap — through the upload URL,
  fetch it back from the view URL, and PUT one through WebDAV on a site whose
  WebDAV toggle is off.

## Docs and rollout

- Product `README.md`, MCP section: the tool and the token's rules.
- Website `public/docs/mcp/index.html`: a row for `open_upload` in the tool
  table, and the "One call carries up to 8 MiB" paragraph pointing at it.
- Ship order as always: product merged and deployed, verified live on
  app.sitebin.io, then the website pushed.
- MCP clients that cache the tool list see the new tool only after
  reconnecting.

## Out of scope

- Resumable or chunked HTTP uploads (tus, ranged PUT).
- Letting a token create a site: `create_site` with no files, then
  `open_upload`, covers it.
- Making the durations configurable.
- Revoking upload tokens when the account token that issued them is revoked.
