# Site tags, tag groups and colours

Date: 2026-11-03. Status: design, approved in conversation with the operator
(mockups reviewed in the brainstorming companion).

## Why

The operator wants to tag their projects (sites, container sites included)
and filter the site list by tag. Their own tagging is by **customer /
project name** ("Müller GmbH", "Bäckerei Huber") and by **kind** ("live",
"mockup", "demo"). They asked for:

- filters "All" and "Untagged" ("Alle", "Ohne Tags"), plus each tag;
- **colours they pick themselves**, so they can build their own system
  (blue = customer, green = kind);
- **tag groups** (e.g. "Customer", "Kind"):
  - a group sets a default colour for its tags, which a tag can override;
  - a tag may also have no group;
  - in the filter each group is a block holding its tags, and a whole group
    can be selected at once;
- that it be **community**: tags are not an Enterprise feature.

## Goals

1. **Every site can carry tags.** Tags live in the MIT core, so they can be
   set and read on every edition and every tier, Free included:
   - on the edit page;
   - through the JSON API;
   - through MCP.
2. **The account's site list ("My account") filters by tags**, grouped in
   blocks, with "All", "Untagged", per-tag and whole-group selection.
3. **Groups and colours** per account, managed on a "Manage tags" page. It
   also renames and deletes a tag across all of the account's sites at once.

## Non-goals

- **Tags anywhere visitors see them.** They are a private label, like the
  site name: never served, never in Caddy, authz or the viewer.
- **The operator's admin register** does not show or search tags (the
  operator's decision: tags are the owner's business).
- **Groups or colours in the community build, the API or MCP.** These
  surfaces know tag *names* only. The community build has no accounts, so it
  has no place that knows all of one person's sites, and no site list to
  filter.
- **A filter argument on MCP `list_sites`.** It returns each site's tags, and
  an agent filters itself. That keeps the tool contract small.
- **Nested groups, or a tag in more than one group.**
- **A free colour picker.** There is a fixed palette instead (see Colours).

## Anti-goals

- Selecting two customers yields an empty list. This is why the filter
  semantics below are per group and not a plain AND.
- "Müller GmbH" and "Mueller GmbH" or "müller gmbh" accumulating as separate
  tags because the UI did not offer the existing one.
- A tag rename or delete that half-applies silently.

## Model

### Core: tags on the site (MIT)

- `store.Meta.Tags []string`, `json:"tags,omitempty"`. Rule 1: older sites
  read as untagged, no migration.
- **One validator, `store.CleanTags`**, through which every writer goes (API,
  MCP, edit page, dashboard, the account-wide rename):
  - surrounding space is trimmed and inner runs of space collapse to one;
  - control characters and commas are **refused**, not stripped (the same
    reasoning as `CleanSiteName`). Commas are refused so that a comma-separated
    form field can carry a list;
  - each tag is 1–30 characters (runes), and a site carries at most 10 tags;
  - duplicates that differ only in case are merged, keeping the first
    spelling;
  - the stored order is case-insensitive alphabetical, so `meta.json` and
    every listing are deterministic.

  A violation is a user-safe error naming the rule (`400` on the API).
- **Identity is case-insensitive** (`strings.EqualFold` / a lowercased key).
  "Demo" and "demo" are one tag. The spelling shown is the stored one.
- **Seam** (the site-names pattern, `2026-09-24-site-names-design.md`):
  - `ext.SiteInfo` gains `Tags []string`;
  - `ext.SiteService` gains `SetTags(viewID string, tags []string) error`
    (validates through `CleanTags`, a vanished site is `ErrSiteGone`, a
    locked site is refused like every other owner write).

### Enterprise: the account's tag registry

Per account, beside `account.json`: `accounts/<id>/tags.json`, written
atomically under the same per-account locking discipline as `account.json`.

```json
{
  "groups": [
    {"id": "g_…", "name": "Customer", "color": "blue"},
    {"id": "g_…", "name": "Kind",     "color": "green"}
  ],
  "tags": {
    "müller gmbh": {"name": "Müller GmbH", "group": "g_…"},
    "demo":        {"name": "demo", "group": "g_…", "color": "red"},
    "privat":      {"name": "privat", "color": "amber"}
  }
}
```

The JSON shape is illustrative: the builder may choose another, as long as it
has the same content.

- **The sites are the truth about which tags exist and where; the registry
  holds only the extras**: group membership, colour override, and the
  display spelling for autocomplete.
  - A tag on a site with no registry entry is ungrouped and grey.
  - A registry entry used by no site is kept and shown with "0 sites". It can
    be created ahead of use from the inline "+ Tag" with a group, and deleted
    on the manage page.
- **Colour resolution:** the tag's own colour, else its group's colour, else
  grey.
- **Limits** (the builder's call, chosen so the dashboard stays readable): at
  most 20 groups and 200 registry tags per account. Group names follow the
  tag rules (1–30 characters, no control characters) and are unique
  case-insensitively within the account.
- **GDPR:** the registry is account data. It goes into the account export,
  and it is deleted with the account.

### Colours

A fixed palette of 8, tuned to the dark claim-ticket look so that every tag
stays legible: `blue #5b8cff`, `green #5ecf8a`, `amber #f5b84d`,
`red #f26d6d`, `violet #a98bff`, `teal #3fc6c6`, `pink #ff86c8`,
`grey #97a3b8`. The values are a starting point: tune them against the
dashboard's real background, and keep them in one place (CSS custom
properties).

A tag is drawn as a small rounded chip: a tinted background, a border at
about 50 % and the text lightened. Stored values are palette **ids**, not hex,
so the palette can be retuned without touching data. An unknown id renders
grey.

## Filter semantics (operator-approved)

The filter state lives in the URL query (bookmarkable, server-rendered), for
example `/account?tag=müller%20gmbh&tag=mockup&group=g_…`. The exact
parameter shape is the builder's call.

- **All**: no filter. This is the default.
- **Untagged**: sites with no tags at all. It is exclusive: choosing it
  clears any tag or group selection, and choosing a tag clears it.
- **Within one group, selections are OR'd.** Müller + Huber shows both
  customers' sites.
- **Across groups, they are AND'd.** (Müller or Huber) and mockup shows those
  customers' mockups.
- **"Whole group"** selects "any tag of this group" (sites that carry at least
  one of its tags). Selecting individual tags in that group as well is
  redundant; the UI shows the group as fully selected.
- **Each ungrouped tag is its own facet**, so two ungrouped tags are AND'd.
- Above the list, a one-line summary says what is filtered and how many
  match, in words: "2 of 8 · (Müller GmbH or Bäckerei Huber) and mockup". A
  link clears the filter.
- Tag counts on the chips are **totals over the account**, not over the
  current result.

## Surfaces

### JSON API (core)

- `PUT /api/sites/{edit}` accepts `"tags": ["Müller GmbH", "mockup"]`. It
  replaces the whole set; `[]` clears, and an absent field leaves the tags
  alone.
- A JSON create body accepts `tags`; a multipart create accepts a `tags` form
  field, comma-separated or repeated.
- Every site payload returns `"tags": [...]`, always present (`[]` when
  untagged), the same as `name` always being present.
- Recorded in provenance as a settings change (the route is already wrapped
  in `recorded(ActionSettings, …)`).

### MCP (core)

- `Settings.Tags *[]string`, with the same semantics as the API: absent
  leaves the tags alone, `[]` clears. So `create_site` and `update_site` take
  it.
- `SiteResult.Tags` and `SiteSummary.Tags` mean `get_site` and `list_sites`
  return it. **In MCP results it is `omitempty`**, unlike the JSON API, where
  it is always present.
  - `SiteResult` is the result of ten tools.
  - An MCP client with a cached output schema rejects any result that carries
    an unknown field, after the server has already acted. The site-names
    rollout showed this: a `create_site` whose one-time `edit_password` the
    client never showed.
  - `name` is `omitempty` in MCP (`internal/mcp/ops.go`) for exactly that
    reason. That limits the breakage to sites that actually carry tags,
    until the client reconnects.
- No new tool: tags are a setting (the site-names reasoning, since a new tool
  is a contract change for saved connector configurations). The `jsonschema`
  description should state the rules (30 characters, 10 tags, no commas,
  private).
- Known trap (site names, 2026-09-24): even with `omitempty`, a stale client
  rejects the results for a tagged site until it reconnects. Note it in the
  docs. Nothing more to fix in code.

### Edit page (core)

A "Tags" field in the settings, saved with a button like the name. Chips with
a remove ×, plus an input. No colours here: the core does not know them. With
accounts this is a secondary place; the dashboard is the primary one.
Plain-text chips are enough.

### Account dashboard (enterprise): "Your sites"

The dashboard is **server-rendered and runs no script** except the hashed copy
script (`copyScriptCSP`). Keep it that way: everything below is links and
plain POST forms behind the CSRF token and the same ownership check as rename,
rotate and delete.

- **Filter area** above the list:
  - first row: "All (n)" and "Untagged (n)";
  - then one block per group, alphabetical, with the group's colour dot, its
    name, a "whole group" toggle, and its tags as chips with counts;
  - then a block "No group" for ungrouped tags;
  - every chip and toggle is a link to the next filter state;
  - selected chips are visibly marked (an amber ring in the mockup);
  - blocks without any tag in use are hidden.
- **The summary line** (see Filter semantics).
- **Per site row:**
  - the site's tags as coloured chips, ordered by group then name;
  - each chip has a small remove control: a POST form, so with no script it
    is a tiny button inside the chip;
  - a "+ Tag" `<details>` disclosure holding a form:
    - a text input with an HTML `<datalist>` of the account's existing tags.
      This is the autocomplete, and it needs no script;
    - a group `<select>` (default "no group"), used only when the typed tag is
      new;
    - an "Add" button.

    The server matches the input case-insensitively against existing tags
    and uses the existing spelling. If the tag is new, it creates the
    registry entry with the chosen group.
- **"Manage tags"** link at the top right of "Your sites" →
  `/account/tags`.

### Manage tags (enterprise): `/account/tags`

One page, listing groups (alphabetical) with their tags beneath, then "No
group":

- **Group row:** name, tag count, colour (8 radio dots), rename, delete.
  Deleting a group keeps its tags, which become ungrouped. A tag that
  inherited the group's colour turns grey; an override is kept.
- **Tag row:**
  - name, count of sites;
  - colour state: "group colour", or "own colour" with a "back to group"
    reset;
  - colour (8 dots), group (`<select>`), rename, delete.
- **Rename** applies to every site of the account that carries the tag (via
  `SetTags` per site) and to the registry. Renaming onto an existing tag
  **merges** them.
  - The existing tag's spelling, group and colour win.
  - Gaps are filled from the renamed tag: if the existing one has no group,
    it takes the renamed tag's group, and likewise for an explicit colour.
  - Ask for confirmation first, naming both tags and the site count, and
    saying which settings the merged tag keeps.
- **Delete** removes the tag from every site of the account and from the
  registry. Sites are untouched otherwise. Confirm with the site count.
- **"+ New group".**
- **Account-wide operations must not half-apply silently.**
  - They walk the account's sites (`AccountSiteIDs`) and are idempotent, so a
    retry finishes the job.
  - The result page names any site that could not be updated: a locked site
    is skipped and named, a vanished site is ignored.
  - The registry is updated after the sites, so a crash midway leaves sites
    with the new name, and the next rename or the page simply shows it.
  - The exact ordering and recovery are the builder's choice within that
    rule.
- Provenance: per-site tag changes from the dashboard are recorded like the
  rename (`recordSite`, a new action such as `ActionTags`). An account-wide
  rename or delete records one account-log line plus one site line per
  changed site. Burst merging already bounds that.

## Shared sites (with `2026-11-03-site-ownership-design.md`)

A site's `Meta.Tags` are its **owner's** labels. The ownership design adds
admins of shared sites, and those admins tag a shared site **in their own
account**. The account's registry gains a per-site tag list for the sites the
account is an admin of; it is validated by the same `CleanTags`, and it uses
the same groups and colours. The dashboard's filter runs over owned and
shared sites alike. An admin never sees or changes the owner's `Meta.Tags`,
and their own tags for a site are dropped when the share ends. A transfer
clears `Meta.Tags`, which were the old owner's labels. If tags ship before
sharing, the per-site list is simply empty.

## Tests the builder must include

- `CleanTags`: trimming, collapse, case-merge keeping the first spelling,
  refusals (control characters, comma, 31 characters, 11 tags), ordering.
- Core API/MCP: set, replace, clear, absent-leaves-alone; `tags` always
  present in the JSON API and omitted when empty in MCP results;
  community build round-trip.
- Filter semantics as a pure function (enterprise): All, Untagged exclusivity,
  OR within a group, AND across groups, whole group, ungrouped tags AND'd,
  unknown tag in the URL ignored.
- Registry: colour resolution, group delete (inherit → grey, override kept),
  rename-merge, delete across sites with one locked site, GDPR export
  includes the registry, account deletion removes it.
- E2E: tags through the API in `e2e/e2e.ps1` (community image) and MCP in
  `e2e/mcp.ps1`; the dashboard filter and the manage page in
  `e2e/accounts.ps1`.

## Docs and coupling

- `Sitebin-Website/public/docs/api/`: `tags` in the settings and payloads.
- `Sitebin-Website/public/docs/mcp/`: `tags` in `create_site`, `update_site`,
  `get_site`, `list_sites`. This adds fields and renames no tool. The page's
  tool list itself does not change.
- `Sitebin-Website/public/docs/using/`: tagging, groups, colours, filter
  rules.
- The pricing page needs no change: tags are on every tier and the page does
  not list such details. Do not add an Enterprise marker anywhere.
- Product README: the `tags` setting.

## Ship order

Not a tier or lifetime change, so the standard order is enough: merge and
deploy the product, verify on app.sitebin.io (set tags, filter, manage), then
push the website docs.

## Corrections (planning, 2026-11-03)

- The seam method is
  `UpdateTags(viewID, edit func(current []string) ([]string, error)) error`,
  not `SetTags(viewID, tags)`. The edit runs under the site lock against
  `meta.json`, so an account-wide rename cannot lose an API write that lands
  between its read and its write. See
  `docs/superpowers/plans/2026-11-03-site-tags.md`, Task 3.

## Calls made without asking (for the operator to check)

- Each tag is limited to 30 characters and a site to 10 tags. Commas are
  refused.
- Per account: at most 20 groups and 200 registry tags.
- Tag counts on the chips are totals, not counts within the current result.
- The edit page shows tags without colours. Colours and groups appear only in
  "My account".
- A tag pre-created in the registry but used by no site is kept and listed
  with "0 sites".
- Renaming onto an existing tag merges them, after a confirmation. The
  existing tag's settings win, and gaps are filled from the renamed one.
  Raised by the builder check.
- `tags` is omitted from MCP results when empty, so that stale connectors
  break only on tagged sites. Raised by the builder check, following the
  site-names precedent.
- The dashboard stays script-free. Autocomplete is an HTML `<datalist>`.

## Corrections (post-implementation, final review 2026-11-03)

- **Deleting a group asks first**, like deleting a tag: a confirmation page
  names the group and how many tags it holds, which stay without a group.
  Only its own POST (`confirm=1`) deletes.
- **"Alphabetical" means the reader's order**, not byte order: every list
  "My account" shows (filter blocks and chips, a site's chips, the datalist,
  the group selects, the manage page, the summary's facets) is ordered by the
  Unicode root collation (`golang.org/x/text/collate`, `language.Und`, which
  is German's standard order: Ä with A). The stored order in `meta.json`
  (`CleanTags`) and the filter's URL (`Query`) keep their byte order, so no
  data and no bookmark changes.
- **The 200 limit counts registry entries** — tags with a group or a colour,
  or created ahead of use — not the tags the sites carry. The error says so:
  "an account can give at most 200 tags a group or colour".
- **Only the rules' refusals reach a page** (400, in the rule's words):
  the registry's limits, a taken group name, a colour, a bad group id
  (`ErrBadGroupID`), an unknown group (`ErrUnknownGroup`), the tag rule and
  the forms' "no longer exists". Everything else is a logged 500 that shows
  nothing of the error. One list (`tagRuleText`) serves the manage page and
  the dashboard's add form.
- **An account-wide rename or delete that changed sites is recorded** in the
  account log even when its registry write then fails.
- **The GDPR export carries each site's tags** (`sites[].tags`), beside the
  registry.
