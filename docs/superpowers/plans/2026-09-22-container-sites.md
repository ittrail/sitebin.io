# Container sites — implementation plan

Design: `docs/superpowers/specs/2026-09-22-container-sites-design.md`.
Branch `feat/containers`. Tests first in every task; both tags green at the end
of every task (`go test ./...`, `go test -tags ee ./...`).

1. **Symlink confinement (core, all modes).** `os.Root` for store writes,
   deletes and reads; listings and ZIP skip symlinks; WebDAV FileSystem over
   `os.Root`; FTP afero Fs over `os.Root`; backup skips escaping links and
   special files; `store.PurgeSymlinks`. Tests create escaping links and
   prove each surface refuses them.
2. **Store mode + meta.** `ModeContainer`, `ContainerMeta`, compose file name
   constant, no file-count cap in container mode, `SetContainerState`,
   `PrepareVolume`.
3. **Seam.** `ext.ContainerProvider`, `ext.ContainerRuntime`, types, the
   SiteService additions and their httpapi implementation.
4. **API.** Mode validation (community: refused; ee: `Allowed`), mode-switch
   side effects (Kick / Stop + purge), container endpoints, domain endpoints
   refuse container sites, payload fields, listing cap, delete stops first.
5. **authz + Caddyfile.** Upstream header, 503/404 pages, path views refuse;
   caddygen strip + proxy lines.
6. **ee/containers compose + catalog.** Parser, validator, image catalogue.
7. **ee/containers docker client** + `dockertest` integration test.
8. **ee/containers manager.** Reconciler over a fake engine.
9. **ee wiring.** eeconfig variables and `max_containers`; provider
   implements `ContainerProvider`; cleanup sweep stops before delete.
10. **UI.** Edit page mode, container card, mappings, logs, domain card
    hidden.
11. **E2E** `e2e/containers.ps1`, run against the real daemon.
12. **Docs.** README (mode, compose reference, env table, security note),
    CLAUDE.md section, deploy example.
13. **Website.** Pricing (Pro 3 / Studio 20), enterprise page, new
    `/docs/containers/`, docs index, sitemap; runbook note for the instance
    (`tiers.json` `max_containers`, env, socket) — not pushed before the
    product is live.
