# authz lookup errors — plan

Spec: `docs/superpowers/specs/2026-11-14-authz-lookup-errors-design.md`.

1. **store, test first** (`internal/store/pathindex_test.go`): add
   `TestResolveAddressUnreadableSiteIsAnError`,
   `TestResolveAddressUnreadablePathDoesNotFallBack` and
   `TestResolveAddressNotFoundSaysWhy`. Run them and watch them fail.
2. **store, implement** (`internal/store/pathindex.go`): `ResolveAddress`
   propagates non-NotFound `load` errors, both on a path link that exists and on
   the host, and wraps `ErrNotFound` with its reason. Run the store tests.
3. **httpapi tally, test first** (`internal/httpapi/authzreport_test.go`): miss
   counting, the bound, silence for generated misses only, and suppressed
   errors, with an injected clock.
4. **httpapi tally, implement** (`internal/httpapi/authzreport.go`): the
   `authzReport` type, `API.RunAuthzReport(ctx)`, and the field in `API`
   initialised in `New`.
5. **authz, test first** (`internal/httpapi/httpapi_test.go`):
   `TestAuthzLookupErrorIsTemporary` (custom domain, corrupt `meta.json` → 503
   with `Retry-After` and no-store) and the miss being counted.
6. **authz, implement** (`internal/httpapi/internalh.go`): the 503 branch and
   the tally calls.
7. **wire** `go api.RunAuthzReport(ctx)` in `cmd/sitebin/main.go`.
8. `go vet ./...`, `go test ./...` and `go test -tags ee ./...`, then
   `e2e/e2e.ps1` and `e2e/paths.ps1` if Docker is available. CLAUDE.md gets a
   short note under the custom-domain section.
9. Commit, push `main`, deploy to app.sitebin.io: Borg predeploy, a rollback
   image, and LICENSE_ROOTS from the running binary. Then verify on the live
   instance: 404 for an unknown custom host stays, nothing else changes, and the
   report line appears for a test miss.
