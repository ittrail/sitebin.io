# Importing a site's Umami history

How to bring one site's history from Umami into Sitebin Analytics. The
design is "Umami import, per site" in
[`superpowers/specs/2026-11-04-site-analytics-design.md`](superpowers/specs/2026-11-04-site-analytics-design.md).

What the import does:

- **The running server imports.** `sitebin analytics import-umami` only
  streams the files to `POST /internal/analytics/import` on the internal
  listener (loopback, never proxied) and prints the server's report. The
  recorder appends under a lock only the server process holds, so the
  server must be running; with it down, the command fails and says so.
- **It never counts twice.** Events at or after the site's **first own
  record** (the earliest line Sitebin recorded itself) are skipped and
  counted, because from then on Sitebin was measuring. `--until <RFC3339>`
  moves that cut earlier, never later.
- **It can be run again.** Every day it touches is rewritten with the
  earlier import's lines replaced by the new ones; Sitebin's own lines stay
  as they are. The same export imported twice gives the same files.
- **Retention applies, the daily cap does not.** Days older than
  `SITEBIN_ANALYTICS_RETENTION_DAYS` are skipped and counted.
- **Funnels and goals** from Umami's reports are added to the site's
  settings when no funnel (or goal) of the same name exists and the limits
  (20 funnels, 30 goals) allow; everything else is listed as skipped.
- Imported visitors are counted per day, like Sitebin's own: an Umami
  session becomes one visitor per local day. Umami's visit ids are kept, so
  visits are Umami's.
- Imported referrers are stored without `www.`, as Umami stores them
  (`google.com/`), while Sitebin's own records keep it (`www.google.com/`).
  The referrer table can list both for the same source.
- It is refused (409) for a locked site and when analytics is off on the
  instance (`SITEBIN_ANALYTICS=false`). A site whose plan lacks analytics
  may be imported into: what is stored stays readable.

The export holds per-visit data (session and visit ids, city, referrers).
Keep the copies only as long as these steps need them, and delete them at
the end (step 6).

## Before you start

1. **Switch the site over first.** Turn analytics on for the site (the
   edit page's Analytics tab) and replace Umami's script with Sitebin's in
   the same deploy. The import then ends exactly where Sitebin's own
   records begin. If Umami kept counting for a while after that (a page
   with the old script, a cache), pass `--until` with the moment it stopped.
2. Note the site's **view id** (`docker exec sitebin sitebin list` on the
   Sitebin host, or the view address `https://<view-id>.sitebin.app`), and
   the **switch-over day**: the local date its analytics was turned on.
3. Work from a machine that can SSH to the Proxmox host srv03 and to the
   Sitebin host. The commands below are POSIX shell (Git Bash works) and
   need nothing else locally: the decoding runs on srv03.

Set the two hosts once. Their real addresses are in the operator's private
notes, not in this public repository; give each as an SSH destination your
SSH config resolves (an alias carrying user, host and port, or
`user@host`).

```sh
PVE='<srv03>'         # the Proxmox host srv03, where Umami runs as VM 103
SB='<sitebin-host>'   # the host running the sitebin container
```

Every query goes to Umami's Postgres (container `0007-1-umami-db-1` in VM
103, user and database `umami`) through the guest agent. `UMAMI` is the
remote command that runs the SQL on its standard input and hands back what
psql printed. The output is compressed on the way, because the guest agent
returns at most 16 MiB, and the command fails rather than hand back a cut
or failed answer:

```sh
UMAMI='qm guest exec 103 --timeout 600 --pass-stdin 1 -- sh -c "docker exec -i 0007-1-umami-db-1 psql -X -q -v ON_ERROR_STOP=1 -U umami -d umami | gzip | base64 -w0" | python3 -c "import sys, json, base64, gzip; d = json.load(sys.stdin); assert d.get(\"exitcode\") == 0 and not d.get(\"out-truncated\") and not d.get(\"err-data\"), d; sys.stdout.buffer.write(gzip.decompress(base64.b64decode(d[\"out-data\"])))"'
```

## 1. Find the website id

```sh
echo "select website_id, name, domain from website where deleted_at is null and domain ilike '%morgenfertig%';" | ssh "$PVE" "$UMAMI"
```

## 2. Export the events and the reports

Save the two files below, with the website id from step 1 in their first
line in place of `<website-id>`.

`umami-events.sql`: one JSON object per event, the event joined with its
session and its event data, oldest first:

```sql
\set site '<website-id>'
set timezone = 'UTC';
copy (
  select json_build_object(
    'created_at', e.created_at,
    'event_type', e.event_type,
    'event_name', e.event_name,
    'hostname', e.hostname,
    'url_path', e.url_path,
    'url_query', e.url_query,
    'referrer_domain', e.referrer_domain,
    'referrer_path', e.referrer_path,
    'page_title', e.page_title,
    'tag', e.tag,
    'visit_id', e.visit_id,
    'session_id', e.session_id,
    'utm_source', e.utm_source,
    'utm_medium', e.utm_medium,
    'utm_campaign', e.utm_campaign,
    'utm_content', e.utm_content,
    'utm_term', e.utm_term,
    'gclid', e.gclid,
    'fbclid', e.fbclid,
    'msclkid', e.msclkid,
    'ttclid', e.ttclid,
    'li_fat_id', e.li_fat_id,
    'twclid', e.twclid,
    'browser', s.browser,
    'os', s.os,
    'device', s.device,
    'screen', s.screen,
    'language', s.language,
    'country', s.country,
    'region', s.region,
    'city', s.city,
    'event_data', (
      select json_object_agg(d.data_key, case d.data_type
               when 2 then to_json(d.number_value)
               when 3 then to_json(d.string_value = 'true')
               when 4 then to_json(d.date_value)
               else to_json(d.string_value)
             end)
      from event_data d
      where d.website_id = e.website_id and d.website_event_id = e.event_id
    )
  )
  from website_event e
  left join session s on s.session_id = e.session_id
  where e.website_id = :'site'
    and e.event_type in (1, 2)
  order by e.created_at, e.event_id
) to stdout with (format csv, quote e'\x01', delimiter e'\x02');
```

`umami-reports.sql`: the site's reports as one JSON array:

```sql
\set site '<website-id>'
copy (
  select coalesce(json_agg(json_build_object('type', r.type, 'name', r.name, 'parameters', r.parameters) order by r.created_at), '[]')
  from report r
  where r.website_id = :'site'
) to stdout with (format csv, quote e'\x01', delimiter e'\x02');
```

Why the SQL looks like this:

- **`copy … to stdout` in CSV format with quote and delimiter bytes that
  never occur in JSON.** The text format (what a plain
  `\copy (…) to stdout` writes) doubles every backslash, which breaks any
  JSON string holding a quote (`\"`) or a newline (`\n`). CSV format writes
  the value as it is: JSON escapes every control character, so the two
  bytes never appear and nothing is quoted. `copy … to stdout` rather than
  `\copy` because a psql backslash command must fit on one line; psql
  relays the server's COPY to its standard output either way.
- **Only page views (1) and custom events (2)** of the one website, oldest
  first: the importer holds one day at a time when the export is sorted.
- **Event data is typed** by `data_type`: numbers stay numbers, booleans
  (stored as `"true"`/`"false"`) become booleans, dates become their
  RFC 3339 text, strings and arrays stay strings.
- **No `referrer_query`** and no click-id values leave Umami as more than
  "present": Sitebin never stores either.

Run both:

```sh
ssh "$PVE" "$UMAMI" < umami-events.sql > umami-events.jsonl
ssh "$PVE" "$UMAMI" < umami-reports.sql > umami-reports.json
wc -l umami-events.jsonl
head -c 600 umami-events.jsonl; echo
cat umami-reports.json
```

`wc -l` is the number of events; it should match Umami's count for the
website (types 1 and 2). An `AssertionError` means the export failed or was
cut: the guest agent's answer is printed with it.

## 3. Copy the files into the Sitebin container

The files go to the SSH user's home on the Sitebin host, and from there
into the container:

```sh
scp umami-events.jsonl umami-reports.json "$SB":
ssh "$SB" 'docker cp umami-events.jsonl sitebin:/tmp/umami-events.jsonl && docker cp umami-reports.json sitebin:/tmp/umami-reports.json'
```

## 4. Import

```sh
ssh "$SB" 'docker exec sitebin sitebin analytics import-umami <view-id> /tmp/umami-events.jsonl --reports /tmp/umami-reports.json'
```

Add `--until 2026-11-05T10:00:00+01:00` to end the import earlier (see
"Before you start"). The command prints the server's report and exits
non-zero when the import failed:

```json
{
  "days": 18,
  "records": 224,
  "skipped_after_cut": 0,
  "skipped_retention": 0,
  "from": "2026-10-17",
  "to": "2026-11-03",
  "funnels": ["Landingpage → Anfrage", "Formular begonnen → gesendet"],
  "goals": ["Anfrage gesendet", "Formular begonnen"],
  "skipped_reports": []
}
```

- `days`, `records`, `from`, `to`: what was written, in the site's local
  dates. The range is recorded in `analytics/imported.json` and shown in
  the tab's Settings as "Imported from Umami".
- `skipped_after_cut`: events at or after the site's first own record (or
  `--until`). After a clean switch-over this is 0, or the few events Umami
  still counted.
- `skipped_retention`: events on days older than the retention.
- `skipped_reports`: `name (type): why` for each report not added.

What a failure answers:

| Status | Meaning |
|---|---|
| "could not reach the running Sitebin server" | the server is not running in the container, or `SITEBIN_INTERNAL_ADDR` is not what the command reads |
| 400 | a line of the export is not usable (the message names it), or a part of the request is missing or malformed |
| 404 | no site with that view id, or it was deleted during the import |
| 409, `reason: locked` | the site is locked: an evidence hold, nothing is imported into it. A lock that came during the import leaves the records written (the report says so) but adds no funnel or goal |
| 409, `reason: instance` | analytics is off on the instance (`SITEBIN_ANALYTICS=false`) |
| 409, no reason | another import into the site is running |
| 500 | nothing imported: the site's day files could not be read to find its first own record (the message says which); or a write failed |

An import that stops part way keeps the days it wrote (the report counts
them) and records their range; fix the cause and run it again.

## 5. Verify

Compare the dashboard with Umami for a few **single days before the
switch-over day**. From the switch-over on, Sitebin measured itself and the
import skipped Umami's events by design (`skipped_after_cut`), so that day
and the ones after it differ. Compare single days, because Sitebin counts
visitors per day (a range's visitors are a sum of days) while Umami counts
distinct sessions over the range. Views and events must match exactly, and
visitors per day too.

The query lists Umami's numbers for the 14 days before the switch-over,
oldest first. Before running it, replace three things in its first lines:

- `<website-id>`;
- `Europe/Vienna` with the site's zone (the tab's Settings show the zone in
  effect);
- `<switch-over-day>` with that date (YYYY-MM-DD).

```sh
cat > umami-days.sql <<'EOF'
\set site '<website-id>'
\set zone 'Europe/Vienna'
\set switch '<switch-over-day>'
select (e.created_at at time zone :'zone')::date as day,
       count(*) filter (where e.event_type = 1) as views,
       count(distinct e.session_id) as visitors,
       count(*) filter (where e.event_type = 2) as events
from website_event e
where e.website_id = :'site' and e.event_type in (1, 2)
  and (e.created_at at time zone :'zone')::date between :'switch'::date - 14 and :'switch'::date - 1
group by 1 order by 1;
EOF
ssh "$PVE" "$UMAMI" < umami-days.sql
```

In the Analytics tab, pick each of those days as the range and compare
views, visitors and the Events sub-tab. The funnels and goals sub-tabs
answer with Umami's semantics, so the imported funnels can be checked
against Umami's reports for the same day.

## 6. Delete the copies

```sh
ssh "$SB" 'docker exec -u 0 sitebin rm -f /tmp/umami-events.jsonl /tmp/umami-reports.json && rm -f umami-events.jsonl umami-reports.json'
rm -f umami-events.jsonl umami-reports.json umami-events.sql umami-reports.sql umami-days.sql
```

(`-u 0`: `docker cp` leaves the files to root, and the container runs as
`sitebin`.) Umami itself is unchanged; retire it once every site moved.
