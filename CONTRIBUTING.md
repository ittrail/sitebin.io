# Contributing

Thank you for looking. A few things that make a change easy to take.

## Two licences, by directory

- Everything outside `ee/` is **MIT**.
- Everything under `ee/` is the enterprise extension, licensed under the
  [Elastic License 2.0](ee/LICENSE). It is source-available, not open source.

A contribution to `ee/` is accepted under ELv2 and one to the rest under MIT;
by opening a pull request you agree to that for the files it touches. Please
do not move logic from `ee/` into `internal/` or the other way round — the
seam between them is `internal/ext`, and [`CLAUDE.md`](CLAUDE.md) explains
why it is the only one.

## Before you open a pull request

```bash
go vet ./... && go test ./...                 # community
go vet -tags ee ./... && go test -tags ee ./...   # enterprise — run BOTH
gofmt -l .
```

Both suites, because the `ee` suite covers paths the core suite cannot even
compile. Tests first: a fix comes with the test that failed without it.

If your change touches tiers, quotas, licensing, custom domains or the MCP
server, read the matching section of `CLAUDE.md` and the design document it
points at in `docs/superpowers/specs/` before writing code — those areas have
rules that were learned the hard way, and the documents say which.

## Commit messages

Lowercase conventional prefixes — `feat:`, `fix:`, `docs:`, `test:`,
`chore:` — with a subject that describes the **behaviour**, not the file:

    fix: sliding renewal never pulls a tier expiry closer

## Security problems

Not in an issue or a pull request: see [SECURITY.md](SECURITY.md).
