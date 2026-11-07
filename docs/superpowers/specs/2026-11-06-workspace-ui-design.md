# The workspace UI: a second account interface, chosen per account

Date: 2026-11-06. Status: approved by the operator through the clickable
mockup ("v2", `Sitebin-Project/mockups/2026-11-06-site-management/index.html`,
first version kept as `index-v1.html`). The operator's words: build *exactly
this* UI, with *all* the functions the current one has, as an alternative to
it, switchable per account at any time, and remembered for the account.

## Why

The account dashboard grew one card at a time: a 720 px column in which every
site carries up to seven buttons and three inline forms, followed by tokens,
zones, billing, plan and the danger zone. The edit page is a second long
column. The admin console's register puts expiry, lock and delete into every
row. With dozens of sites, finding and changing one is slow.

## What the operator decided

- **Two interfaces, one product.** "Classic" (today's pages, unchanged) and
  "Workspace" (the mockup). The account chooses; the choice is stored on the
  account (`account.json`, field `ui`), so it follows the person to every
  device and every sign-in, and a toggle switches it at any time, both ways.
  Default: classic, so nobody's interface changes under them.
- **Look and behaviour of the mockup v2**: sidebar (sites, shared, inbox, tag
  groups as filters, domains & zones, tokens & agents, plan & usage, account;
  the instance section for the operator), a dense site table with status
  chips, grouping, sorting, filter and bulk actions, a "needs you" strip,
  Ctrl+K search; a click on a site opens a **full-width site page**: the claim
  ticket across the top (stamps, name, address, tags | facts, actions) and
  tabs Overview, Files, Addresses, Forms, Analytics, Git, Containers, People,
  Activity, Settings; J/K steps through the filtered list, Esc goes back. The
  operator's overview is a triage list with the health of the instance; the
  register opens a side ticket with the operator's actions.
- **Every function the classic UI has works in the workspace** (list below).

## Architecture

The rule that shapes it: **no authorization or business rule is written a
second time.** The workspace is a different *presentation* of the same
server routes and the same site client.

1. **Preference.** `account.Account.UI` (`""` classic, `"workspace"`),
   `POST /account/ui` (CSRF, `ui=classic|workspace`) stores it and returns to
   `/account`. Classic pages show "Try the new interface"; the workspace has
   the way back in its sidebar and on the Account view.
2. **The account shell** (`ee/ui/`, ELv2, embedded): `GET /account` serves
   `app.html` to a workspace account instead of the dashboard template; the
   assets are served at `/account/ui/{file}` (versioned like the core's
   `?v=`). The page boots from `GET /account/api/state` — an explicit JSON
   view of what the dashboard renders (never `ext.SiteInfo` wholesale: the
   owner must not see who placed a lock, the abuse findings or the CSP
   report data). The CSP is the dashboard's with `script-src 'self'`.
3. **Actions reuse the existing POST routes.** The shell posts the same forms
   with `fetch` plus the header `X-Sitebin-UI: json` (a custom header, so a
   cross-site form cannot send it). A middleware on every `/account` route
   wraps the response writer for such a request; `p.redirect`, `renderMessage`
   (a secret shown once — new token, new edit password — rides in `detail`)
   and `leaveTo` answer JSON instead of HTML; `http.Error` stays text with
   its status. Nothing else in the handlers changes.
4. **Server-rendered flows in the workspace frame.** Multi-step pages that
   stay server-rendered (transfer wizard, its confirmations, tag merge steps,
   account deletion confirm, adopt, messages) are rendered with a workspace
   head when the account prefers it: every ee template is parsed twice
   (classic head and workspace head, same body), and the middleware marks
   the writer. The workspace head carries the sidebar and the workspace CSS,
   which restyles the classic markup.
5. **The site page.** The core's `/e/{editID}` asks the optional seam
   `ext.EditPages` first; the extension serves `ee/ui/site.html` to a
   signed-in workspace account. That page contains every element id
   `edit.html` has, arranged in the workspace tabs, and loads the core's own
   `edit.js`, `edit-git.js` and `edit-analytics.js` unchanged in their rules;
   `site.js` (ee) renders the ticket header, the tab bar, Overview, People and
   Activity, and maps the tabs onto edit.js's ("live" holds the file, address,
   form, container and settings tabs; "git" and "analytics" are edit.js's).
   Anyone without a workspace account (password visitors, community build)
   gets `edit.html` exactly as before.
6. **New reads the mockup needs**, all behind the existing standing rules:
   - `GET /account/api/sites/{id}/activity`: the site's provenance for its
     owner or admin, without addresses or user agents (like the GDPR export's
     redaction of foreign entries).
   - `ext.SiteInfo` gains `UpdatedAt`, `ContainerEnabled` and
     `ContainerStatus` (core, additive) for the list's Changed and Status
     columns.
   - Operator JSON (`/account/api/admin/…`: register, reports, containers,
     trail, account) built by the same functions the HTML console uses, plus
     the instance health (licence, data volume free space, containers,
     mailers).
   - Visitors in the list come from the site's own analytics API, fetched per
     visible row by the browser (session standing), never computed for every
     site on the server.

## Every function, and where it lives in the workspace

| Function today | Workspace |
|---|---|
| Sign out, Manage account (IdP console), Instance register link | Account view; sidebar |
| Licence notice | banner above every view |
| Takeovers (accept all / decline), invitations (accept / decline) | Inbox |
| Site list, tag filter (all, untagged, group, tag), counts | Sites table, sidebar tags, status chips |
| Rename, add/remove tag (new tag with group) | Settings tab; row menu; bulk Tags |
| Manage (edit page) | the site page |
| Leave a shared site | People tab; row menu |
| Share with admin, end share, cancel invitation | People tab |
| Transfer (wizard, bundles, dates, cancel) | People tab / bulk → transfer pages in the workspace frame |
| Reset edit password (shown once) | People tab; row menu → secret dialog |
| Delete site (with Git note) | Settings danger zone; row menu; bulk |
| Adopt an anonymous drop | Inbox |
| API tokens: create (shown once), revoke, MCP endpoint | API tokens & agents |
| Zones: add, check now, remove, records | Domains & zones |
| Billing portal, cancel contracts link, plans (upgrade, switch) | Plan & usage |
| Delete account (local confirm / stack) | Account view |
| Tag management (groups, colours, rename, merge, delete, order) | Tags view (sidebar "Manage") |
| Edit page: files (browse, upload, ZIP, folder, replace all, delete, editor, download ZIP), usage | Files tab |
| Containers (start/restart/stop, services, logs, stats) | Containers tab |
| Name, tags, behaviour (mode, viewer entry, SPA fallback, not-found page, caching), start-page note | Settings tab |
| Access (view password, WebDAV, FTP, expiry) | Settings tab (WebDAV/FTP also under Files "other ways in") |
| Custom domains (add, proof, check now, remove, redirects, targets, generated address, per-port addresses) | Addresses tab |
| Forms (add, edit, remove, resend confirmation, snippet) | Forms tab |
| Git tab, Analytics tab (all sub-tabs) | Git, Analytics tabs (the same modules) |
| Admin banner for a shared site | stamps + People tab |
| Locked site (held page), password unlock | unchanged: workspace only for signed-in accounts; held page shown in the frame |
| Register: search (text, IP/CIDR), filters, figures, lock (reason), keep, unlock, hold, release, expiry, delete, dismiss findings, provenance, trail, account page | Instance → Register (side ticket), Overview |
| Reports (lock from a report) | Instance → Reports |
| Containers console (defaults, per-service limits, usage) | Instance → Containers |

## Non-goals

- No change to any rule, quota, permission or API shape.
- The classic UI stays the default and stays fully supported.
- No mobile app; the workspace is responsive like the mockup.
- Password-only (anonymous) site management stays classic.

## Testing

- Go (both tags): preference round trip; `/account` serves the shell only to a
  workspace account; JSON mode for redirect/message/leaveTo and its absence
  without the header; the state JSON hides lock holder and findings; the edit
  page seam (community path unchanged); activity redacts IP/UA and refuses a
  stranger; SiteInfo's new fields; operator JSON refuses a non-admin.
- Browser: every row of the table above exercised against a local enterprise
  instance (local auth), both interfaces.
- The existing e2e suites (`accounts.ps1`, `ownership.ps1`, `tiers.ps1`) stay
  green: classic is the default.

## Corrections (post-implementation)

What building it changed or settled, 2026-11-06:

- **The console's JSON reuses the HTML handlers whole.** Instead of
  refactoring each console handler, `uiCapture` runs it with a writer the twin
  template recognises, which keeps the view the page would have rendered. The
  JSON is therefore exactly what the HTML console shows (same filters, same
  refusals); its field names are the Go field names.
- **Refusal pages read as text.** Some routes answer a refusal with a page of
  their own (share and transfer plan checks). In JSON mode the shell parses
  such an answer's heading and text with `DOMParser` and shows them in a
  dialog; nothing in the page is executed or inserted as HTML.
- **The site page's delete is a dialog.** edit.js deletes on a second click
  and replaces the page; the workspace catches the button first, asks in a
  dialog, calls the same `DELETE /api/sites/{editID}` and returns to the list.
- **Containers are shown only where the runtime is** (`account.containers` in
  the state), as the classic console hides its tab.
- **The transfer page preselects every `site` parameter**, so the bulk
  "Transfer" opens one offer for the whole selection (a single `site` is
  unchanged).
- **`TestWorkspaceSitePageCarriesEveryEditPageID`** fails when edit.html gains
  an id that ee/ui/site.html lacks: such a control would silently do nothing
  in the workspace.
- **Verified after the rollout (same day):** a phone-size pass (390 px,
  headless Chromium) found the sliding sidebar see-through; a scripted run of
  the workspace against a local instance with a real Docker runtime and a
  stand-in PayGate covered forms (mail confirmation), analytics with counted
  visits, a Git push with a token made in the workspace, a transfer between
  two accounts taken over in the workspace, deletes from the row menu and in
  bulk, filters, grouping, the container tab and console, upgrade and portal.
  It found that a reload lost the site page's tab (edit.js rewrites the hash
  to the folder) — the workspace now keeps its tab there, the folder's hash
  only on Files. The live check found the analytics switch read from the
  wrong field (`settings.enabled`). Fixed in 0b92196 and b4944c0.
- **Parity run (1e81a52):** a second scripted run drove every remaining
  classic function through the workspace — 26 checks, all passing on a
  freshly seeded instance: rotate, share/withdraw/end/leave, invitation and
  offer declined, tags and tag groups, zones, token revoke, plan switch,
  adopt, files with upload/editor/delete, rename/tags/view password/expiry/
  caching, File viewer mode, domain add/remove, WebDAV, download, register
  address search/lock/hold/release/expiry/trail/account page, a lock from a
  report, container defaults, a local account's deletion. It found the
  header's tags stale after a save in Settings and the state not refreshed
  on a view switch; both fixed.
- **A fresh SSO sign-in** is covered by
  `TestSSOSignInOfAWorkspaceAccountLandsInTheWorkspace`; in a browser it
  needs the person's own credentials at the identity provider.
- **Side-by-side with Mockup v2 (2026-11-07):** the same five views of the
  mockup and the workspace at 1440×900 (sites, a site, its Settings, the
  operator's overview, the register with its side ticket). Lists, overview
  and register matched; the site page did not, and now does:
  - the ticket's fact is **Visitors 7d** (the list's figure: unique
    visitors over the last seven days) when analytics are on, else Views;
    edit.js's meta line is hidden (At a glance has it), and the address has
    an open-in-new-tab button beside Copy;
  - **Settings** is arranged as the mockup: "Name and tags", **Site type as
    four cards** (Static, Single-page app, File viewer, Container — the
    mode plus the SPA switch, written in one settings PUT; edit.js's radios
    and switch stay in the page, hidden), "Missing pages" (shown where
    edit.js shows the not-found field), Expiry (read-only for an admin),
    View password, Browser caching, and a one-line Danger zone;
  - the tag editor's chips wear the account's tag colours;
  - At a glance names the caching policy (Automatic, not `auto`), who set
    the expiry, and the Git repository.
  Elsewhere: types read Static / SPA / Viewer / Container in lists and the
  ticket (`ext.SiteInfo.SPA`, `wsSite.spa`), long names in grouping; "By
  <tag group>" is offered only for groups the sites use, and past two they
  share one button with a menu; a row shows two tags and "+N"; the
  register's filter chips carry counts (`adminView.Counts`, over the whole
  instance) and its trail reads "Created in the browser, anonymously"
  instead of `ui · none`; the operator overview names the host.
- **Container sites, in depth (same day):** a scripted run against real
  Docker switched a site to Container with the type card, saw it run with
  one generated address per `http:` port (each reaching its own port),
  restarted it from the Containers tab, changed the compose file in the
  editor and saw both ports answer with the change, stopped it and switched
  back to Static — 15 checks. With it, the e2e (19) and parity (26) runs
  pass on a fresh seed.
- **The last three differences (2026-11-07, after 1e64c04):**
  - **Health** has the mockup's five rows — Licence, Disk, Backups,
    Containers, Mail. Disk is used of total with a percentage
    (`analytics.DiskSize` beside `DiskFree`; warn from 70 %, bad from 90 %,
    and a "Disk N % full" line under Needs a decision). **Backups** reads the
    note a complete `sitebin backup` now leaves (`store.BackupMark`,
    `<data>/backup-last.json`: time, entries, skipped, archive bytes; never
    archived itself; written only after the tar and gzip are closed, which
    the backup now checks instead of deferring); older than 26 h is a warning,
    none is "None". Mail states both mailers (`ext.FormsMailHost`, optional on
    Host). Lock retention left the panel; each lock's ticket states it.
  - **Expiry** is a date field, as in the mockup: the workspace saves the
    picked day at the time of day the expiry already had (a plan's term keeps
    its hour), else at 23:59; edit.js's datetime field stays in the page,
    hidden, and does the saving.
  - **Register:** dates as the workspace writes them ("17 Dec"), size only,
    "never"; a lock's ticket reads Since / Reason / Retention; unnamed sites
    are "Untitled drop" everywhere, as in the mockup. Register dates are the
    server's calendar days (an operator's expiry means through that day in
    the server's zone), so they are shown as that day, never converted to
    the browser's zone — converting showed 17 Dec for an expiry set to 16 Dec.
  Verified: e2e 19, parity 26 (now also a date-only expiry and the register's
  date), containers 15, both Go suites.
- **Final pass (same night):** the site page's Overview lost what the mockup
  does not have — the totals line above the chart and At a glance's Storage
  and Views (the ticket states size and visitors) — and analytics that are
  off get the mockup's **Turn on** button (Overview card and Needs
  attention): it sends the stored settings with only `enabled` changed,
  since the settings PUT replaces them whole (what the Analytics tab's own
  switch does). The e2e checks that a saved exclude list survives it, reads
  the chart's bars and the ticket's Visitors 7d (20 checks).
