#!/usr/bin/env sh
# Deploy a folder to Sitebin via the HTTP API. Needs `zip` and `curl`.
#
# Create a new site:
#   SITEBIN_BASE=https://sitebin.example.com scripts/deploy.sh ./dist
#   SITEBIN_BASE=https://sitebin.example.com SITEBIN_MODE=viewer scripts/deploy.sh ./report.pdf-dir
#
# Update an existing site (replaces all files):
#   SITEBIN_EDIT_URL=https://sitebin.example.com/e/<edit-id> \
#   SITEBIN_EDIT_PASSWORD=... \
#   scripts/deploy.sh ./dist
#
# On create it prints the JSON response (view_url, edit_url, edit_password).
set -eu

FOLDER="${1:-}"
if [ -z "$FOLDER" ] || [ ! -d "$FOLDER" ]; then
  echo "usage: deploy.sh <folder>  (see header for env vars)" >&2
  exit 2
fi
command -v zip >/dev/null 2>&1 || { echo "error: 'zip' is required" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "error: 'curl' is required" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
( cd "$FOLDER" && zip -qr "$TMP/site.zip" . )

if [ -n "${SITEBIN_EDIT_URL:-}" ]; then
  # ---- update mode ----
  : "${SITEBIN_EDIT_PASSWORD:?set SITEBIN_EDIT_PASSWORD to update}"
  EDIT_ID="${SITEBIN_EDIT_URL##*/e/}"
  BASE="${SITEBIN_EDIT_URL%%/e/*}"
  echo "Updating $EDIT_ID …" >&2
  curl -fsS -X POST \
    -H "X-Edit-Password: $SITEBIN_EDIT_PASSWORD" \
    -F "zip=@$TMP/site.zip" \
    "$BASE/api/sites/$EDIT_ID/files?replace=true"
  echo >&2
  echo "Updated $SITEBIN_EDIT_URL" >&2
else
  # ---- create mode ----
  : "${SITEBIN_BASE:?set SITEBIN_BASE (create) or SITEBIN_EDIT_URL (update)}"
  ARGS="-F zip=@$TMP/site.zip"
  [ -n "${SITEBIN_MODE:-}" ] && ARGS="$ARGS -F mode=$SITEBIN_MODE"
  [ -n "${SITEBIN_VIEW_PASSWORD:-}" ] && ARGS="$ARGS -F view_password=$SITEBIN_VIEW_PASSWORD"
  echo "Creating a new site on $SITEBIN_BASE …" >&2
  # shellcheck disable=SC2086
  curl -fsS -X POST $ARGS "$SITEBIN_BASE/api/sites"
  echo
fi
