# Account storage cap — plan

Design: `docs/superpowers/specs/2026-11-11-account-storage-cap-design.md`.
Branch `feat/account-storage` (worktree `.claude/worktrees/account-storage`).
Every task: test first, then code, then `go test ./...` and `go test -tags ee ./...`.

1. **Tier field and stamp.** `eeconfig.Tier.MaxAccountBytes`
   (`max_account_bytes`), `ext.CreateGrant.MaxAccountBytes`, `grantFromTier`,
   `Meta.QuotaAccountBytes`, `store.Quota.AccountBytes` in `ApplyQuota`,
   `stampCaps`, `quotaFromGrant`, `cleanup.reconcile`. Tests: a grant stamps it
   at creation, a restamp follows a tier change, `ApplyQuota` keeps it.
2. **Sweep backfill.** `store.SetQuotaAccountBytes`, `cleanup.reconcileAccountBytes`
   beside `reconcileGit`. Tests: backfills, follows a change, an error or an
   unknown tier changes nothing.
3. **Account index.** `internal/store/accountusage.go`: note from `SiteUsage`
   (anonymous/locked dropped), drop on `Delete`/`ForceDelete`/`PurgeLocked`, re-own
   on transfer/adopt, `MeasureAccounts`, `AccountUsage(site)` with the 30 s
   re-measure near the cap. Tests for each.
4. **`ByteCap` and `ErrAccountFull`.** Tests: per-site binds; account binds; over
   is negative; no stamp = per-site.
5. **Store funnels.** `SaveFile`, `ExtractZip`, `BeginReplace` use `ByteCap` and
   return `ErrAccountFull` when the account bound. `accountChecks` census table
   in `usage_test.go`.
6. **HTTP funnels.** WebDAV PUT, FTP `UsedOutside`, Git allowance; `storeError`
   and MCP `mapErr` words. Tests per surface (join `accountChecks`).
7. **Containers.** `ext.ContainerSite.AccountCap/AccountOthers` in
   `containerSiteOf`; `enforceStorage` stops over the account cap. Tests in
   `ee/containers`.
8. **Startup and sweep measure.** `cmd/sitebin` starts `MeasureAccounts` in the
   background; `Sweep` calls it first.
9. **Ownership.** `limitsCheck` account line. Tests: adopt and accept refused
   when the bundle does not fit, pass when it does.
10. **Reports.** API settings read `account_bytes`/`account_max_bytes`; edit page
    line; workspace caps + meter + lede; classic dashboard line; `tierFeatures`.
11. **Docs in repo.** README tier table, CLAUDE.md "Tiers, quotas and lifetimes"
    rule, e2e `tiers.ps1` case for the account cap.
12. **Ship.** Merge, push, deploy (pre-deploy Borg snapshot, `tiers.json`
    `max_account_bytes` on free/pro/studio), live check, then the website.
