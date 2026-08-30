# ---- backend: test + build (static binary) ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# EDITION=community (default, MIT) or enterprise (adds the ee/ premium tree).
ARG EDITION=community
ARG VERSION=dev
# LICENSE_ROOTS is the comma-separated list of trusted Ed25519 ROOT public keys
# (base64) this build verifies enterprise licences against. It is baked in at
# BUILD time on purpose -- a public key fetched at runtime is one an attacker
# can substitute -- and it is a LIST so a root can be rotated without
# redistributing every binary. Empty (the default) means this build trusts
# nothing, which resolves to the unlicensed trial state and never to a refusal
# to start. The release pipeline passes the stack's root; e2e/license.ps1 passes
# a throwaway one it minted itself.
ARG LICENSE_ROOTS=""
RUN set -eux; \
    if [ "$EDITION" = "enterprise" ]; then TAGS="-tags ee"; else TAGS=""; fi; \
    LDFLAGS="-s -w -X main.version=${VERSION}"; \
    if [ -n "$LICENSE_ROOTS" ]; then LDFLAGS="$LDFLAGS -X github.com/ittrail/sitebin.io/ee/licensing.trustedRootsB64=${LICENSE_ROOTS}"; fi; \
    go vet $TAGS ./... && go test $TAGS ./...; \
    CGO_ENABLED=0 go build $TAGS -trimpath -ldflags "$LDFLAGS" -o /out/sitebin ./cmd/sitebin

# ---- caddy with DNS modules for the wildcard certificate ----
FROM caddy:2-builder AS caddybuild
RUN xcaddy build \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/caddy-dns/hetzner/v2 \
    --with github.com/caddy-dns/duckdns \
    --with github.com/caddy-dns/netcup \
    --with github.com/caddy-dns/porkbun

# ---- final: all-in-one, non-root ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini libcap mailcap \
    && addgroup -S -g 1000 sitebin \
    && adduser -S -u 1000 -G sitebin -h /data sitebin \
    && mkdir -p /data && chown sitebin:sitebin /data
COPY --from=caddybuild /usr/bin/caddy /usr/bin/caddy
COPY --from=build /out/sitebin /usr/bin/sitebin
# let the non-root user bind :80/:443
RUN setcap cap_net_bind_service=+ep /usr/bin/caddy

USER sitebin
ENV SITEBIN_DATA_DIR=/data \
    XDG_DATA_HOME=/data/caddy-home \
    XDG_CONFIG_HOME=/data/caddy-home
VOLUME /data
# 80/443 = HTTP(S); 21 + 21000-21010 = optional FTP control + passive data
# (only used when SITEBIN_FTP_ENABLED=true; map them at `docker run` as needed).
EXPOSE 80 443 443/udp 21 21000-21010
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["sitebin", "healthcheck"]
ENTRYPOINT ["/sbin/tini", "--", "sitebin"]
CMD ["run"]
