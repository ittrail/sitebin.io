# Security policy

Sitebin serves content strangers upload, on domains the operator owns, with a
certificate the operator's Caddy issued. That makes every content-safety and
authorization bug a serious one, and we would rather hear about it from you
than from a victim.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting on this repository
(*Security → Report a vulnerability*). It reaches the maintainers only, and
lets us talk in a thread nobody else can read. If that is unavailable to you,
the contact details in the imprint at [sitebin.io](https://sitebin.io) reach
the same people.

Tell us what you found, how to reproduce it, and which version or commit you
looked at. A proof of concept against your **own** instance or your own site
on the hosted one is welcome; please do not test against other people's sites
or accounts.

## What to expect

- An acknowledgement within **3 business days**.
- A fix, or a clear reason there will be none, within **30 days** for anything
  we agree is High or Critical, and a coordinated disclosure date agreed with
  you before anything is published.
- Credit in the release notes if you want it.

## Scope

In scope: this repository (both the MIT core and the ELv2 `ee/` tree), the
published container images, and the hosted service at `app.sitebin.io`
(please stay within your own account and sites there).

Out of scope: the IT-Trail SaaS Stack (its own repository has its own
policy), denial of service by volume against the hosted service, and findings
that require an operator to have set `SITEBIN_DOMAIN_VERIFICATION=off` or
`SITEBIN_HTTP_ONLY=true` on an instance exposed to strangers — both are
documented as unsafe for that.

## Supported versions

The current `main` and the latest published image tag. Fixes are not
backported: Sitebin is deployed from `latest`.
