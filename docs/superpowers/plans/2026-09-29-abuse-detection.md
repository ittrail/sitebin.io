# Abuse detection — plan

Design: [`../specs/2026-09-29-abuse-detection.md`](../specs/2026-09-29-abuse-detection.md).
Tests first; run `go vet ./...`, `go test ./...`, `go vet -tags ee ./...` and
`go test -tags ee ./...`.

1. **`internal/abuse`** — `Rule`, `Destination`, `RuleSet`, `Defaults()`,
   `Parse` (extend / replace / off, validation), `Loader` (mtime, 10 s, last
   good), `Scan` (streaming, chunk overlap, binary skip, 8 MiB cap, SHA-256,
   excerpts, active extensions), `RuleSet.Destination(url)`,
   `RuleSet.References`. Tests per rule, positive and negative.
2. **Store** — `Meta.Abuse` (`Findings`, `Reviewed`), `LockByScanner`,
   `ErrHeld`; `SetScanner`, `SetScanHook`; the verdict inside `writeFileIn`
   (temp → scan → meta → rename) for `SaveFile` / `ExtractZip`; staged
   verdicts applied in `Replacement.Commit`; `StagedFile` + `RenameChecked`;
   `CheckExfil`; `ScanSite` / `ApplyScan`; operator unlock and
   `ClearFindings` move hashes into `Reviewed`. Tests incl. "lock before
   visible", exemptions, reviewed fingerprint, new content.
3. **Surfaces** — `storeError` / `mcpError` map `ErrHeld`; `createSiteWith`
   finishes a held creation (tier expiry, ownership) and answers 403; WebDAV
   `siteFS` stages writes and checks renames, answers 403 on a hold; FTP
   `quotaFs` stages through a guard from `FTPAuth`. Tests per surface and the
   write-path census.
4. **Alerts** — `config.AbuseAlertsTo`, `forms.BuildNotice`, `alerter`
   (per-key hour, global cap, digest, async, defang), store hook → log +
   `lockChanged` + alert. Tests.
5. **Tripwire** — `handleCSPReport` → destination → document URL check →
   limiter/semaphore → `CheckExfil`. Tests incl. forged report.
6. **Reports** — `Report.Contact` / `Via`, `/report` page (GET/POST, ticket,
   honeypot, CSP), API `contact`, mail per stored report, `/v/<id>` targets,
   links from the 410 page and the landing footer. Tests.
7. **Seam + register** — `ext` additions, `siteService.Reports` /
   `ClearFindings` / findings in `infoOf`; ee `AccountEmail`, Reports tab,
   lock from report, Flagged filter + Scanner figure, findings + Dismiss,
   scanner lock text + Keep. Tests.
8. **CLI** — `sitebin scan <id>|--all [--lock]`; `serve()` wires the loader
   and the operator check. Tests.
9. **Docs** — README (env var, rules file, CLI, register), `CLAUDE.md`
   invariant ("every write is scanned before it is visible").
10. **Verify** — full suites both editions + vet; run the rules against the
    quarantined kits and the production sites (read-only, streamed); rebase,
    push, CI.
