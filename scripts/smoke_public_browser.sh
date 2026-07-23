#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ENGINE=${KIGO_PUBLIC_BROWSER_ENGINE:-chromium}
case "$ENGINE" in
  chromium|firefox|webkit) ;;
  *)
    echo "KIGO_PUBLIC_BROWSER_ENGINE must be chromium, firefox, or webkit" >&2
    exit 2
    ;;
esac

if [[ -n "${KIGO_PUBLIC_BROWSER_CHANNEL+x}" ]]; then
  CHANNEL=$KIGO_PUBLIC_BROWSER_CHANNEL
elif [[ "$ENGINE" == "chromium" ]]; then
  CHANNEL=chrome
else
  CHANNEL=""
fi

if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  shift
else
  DRY_RUN=0
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--dry-run]" >&2
  exit 2
fi

if [[ "$DRY_RUN" == "0" ]] && ! node -e "require.resolve('playwright')" >/dev/null 2>&1; then
  if [[ -d /Users/sui/Code/openclaw/node_modules ]]; then
    export NODE_PATH=/Users/sui/Code/openclaw/node_modules${NODE_PATH:+:$NODE_PATH}
  fi
fi
if [[ "$DRY_RUN" == "0" ]] && ! node -e "require.resolve('playwright')" >/dev/null 2>&1; then
  echo "Playwright is required for the public browser smoke test." >&2
  exit 2
fi

args=(
  --url "${KIGO_PUBLIC_BROWSER_URL:-}" \
  --engine "$ENGINE" \
  --channel "$CHANNEL" \
  --force-turn "${KIGO_PUBLIC_BROWSER_FORCE_TURN:-1}" \
  --ignore-tls-errors "${KIGO_PUBLIC_BROWSER_IGNORE_TLS_ERRORS:-0}" \
  --scenarios "${KIGO_PUBLIC_BROWSER_SCENARIOS:-text,file}" \
  --timeout-seconds "${KIGO_PUBLIC_BROWSER_TIMEOUT_SECONDS:-60}" \
  --artifact-dir "${KIGO_ARTIFACT_DIR:-$ROOT/artifacts/public-browser-matrix}"
)
if [[ "$DRY_RUN" == "1" ]]; then
  args+=(--dry-run)
fi
exec node "$ROOT/scripts/smoke_public_browser.js" "${args[@]}"
