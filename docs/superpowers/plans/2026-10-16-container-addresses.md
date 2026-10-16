# Container addresses — plan

Spec: `docs/superpowers/specs/2026-10-16-container-addresses-design.md`.
Tests first in every task; `go test ./...` and `go test -tags ee ./...` green
before each commit.

1. **Compose `http:`** (`ee/containers/compose.go`): parse `http:` into
   `Service.Ports`, refuse `domains:` with the hint, `Spec.Endpoints()` in file
   order; drop `Domain`, `CustomDomains`, the `*` bookkeeping. Tests in
   `compose_test.go`.
2. **Seam** (`internal/ext`): `ContainerService.Ports` replaces `Domains`;
   `SyncContainerEndpoints(viewID, endpoints)` replaces
   `SyncContainerDomains`. Runtime (`manager.go`) calls it after a good parse;
   `describe` reports ports. Fakes in tests follow.
3. **Store model** (`internal/store`): `Address`, `Meta.Addresses`,
   `Meta.DomainTargets`, `ContainerService.Ports`, `address-index`,
   `ByAddress`, `SyncAddresses` (labels, links, target fill), delete and
   dangling-link cleanup, `ClearAddresses` for leaving the mode,
   `ContainerMeta.Upstream(endpoint)`.
4. **Store rules** (`redirects.go`): container-aware `serves`/`followable`,
   `AddressRule`, `ViewRule` for container sites, `DomainEndpoint`,
   `SetDomainTarget`, `SetAddressRule`, container `checkTarget`,
   `redirectSource` and `dropAddressRules` covering address rules and targets.
5. **httpapi**: `siteByHost` via the address index; `addressRule` and `admit`
   by endpoint; lift the container refusals (`claimDomain` with target,
   `removeDomain`, `changeDomainRedirect`, `changeViewAddress` with
   endpoint); `PUT …/domains/{domain}/target`; payload (`addresses`,
   `domain_targets`, container `ports`); `SyncContainerEndpoints`;
   `leaveContainerMode` clears addresses; provenance `domain-target`.
6. **MCP**: `add_domain` target, `set_domain_target`, `set_view_address`
   endpoint, `SiteResult.Addresses` / `DomainTargets`; catalog entry.
7. **Edit page**: domain card for container sites (address rows, port
   targets, port select on add), container card shows ports.
8. **e2e**: fixture `http:`, `containers.ps1` covers own address, domain to a
   second port, random address of a second service, redirect, off, vanished
   port.
9. **Docs**: README, `CLAUDE.md`, redirects spec Corrections; website
   `/docs/containers/`, `/docs/custom-domains/`, `/docs/mcp/`.
10. **Ship**: push, deploy app.sitebin.io (prune build cache), verify live with
    a throwaway container site, publish the website.
