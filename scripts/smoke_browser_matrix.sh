#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BROWSERS=${KIGO_BROWSER_MATRIX:-chromium,firefox,webkit}
CUSTOM_FILTER=${KIGO_BROWSER_MATRIX_FILTER:-}
NATIVE_INTERFACE=${KIGO_BROWSER_MATRIX_NATIVE_INTERFACE:-${KIGO_SMOKE_NATIVE_INTERFACE:-}}
ALL_FILTER="web protocol guards,native->web file,native->web text,web->native file,web->native text,web->web file,web->web text,web->web note,web error handling,web cancel handling"
NATIVE_FILTER="web protocol guards,native->web file,native->web text,web->native file,web->native text"
WEB_FILTER="web->web file,web->web text,web->web note,web error handling,web cancel handling"

IFS=',' read -r -a browser_list <<<"$BROWSERS"
if [[ ${#browser_list[@]} -eq 0 ]]; then
  echo "KIGO_BROWSER_MATRIX must name at least one browser" >&2
  exit 2
fi

failed=0

run_profile() {
  local browser=$1
  local profile=$2
  local filter=$3
  local turn_enabled=$4
  local native_interface=$5
  local channel=""
  if [[ "$browser" == "chromium" ]]; then
    channel=${KIGO_BROWSER_MATRIX_CHROMIUM_CHANNEL-chrome}
  fi
  echo "browser matrix: $browser profile=$profile"
  if PLAYWRIGHT_BROWSER="$browser" \
    PLAYWRIGHT_CHANNEL="$channel" \
    KIGO_SMOKE_FILTER="$filter" \
    KIGO_SMOKE_TURN_ENABLED="$turn_enabled" \
    KIGO_SMOKE_NATIVE_INTERFACE="$native_interface" \
    "$ROOT/scripts/smoke_native_web.sh"; then
    echo "browser matrix passed: $browser profile=$profile"
  else
    echo "browser matrix failed: $browser profile=$profile" >&2
    failed=1
  fi
}

for raw_browser in "${browser_list[@]}"; do
  browser=${raw_browser//[[:space:]]/}
  case "$browser" in
    chromium|firefox|webkit) ;;
    *)
      echo "unsupported browser in KIGO_BROWSER_MATRIX: $raw_browser" >&2
      exit 2
      ;;
  esac
  if [[ -n "$CUSTOM_FILTER" ]]; then
    run_profile "$browser" custom "$CUSTOM_FILTER" "${KIGO_SMOKE_TURN_ENABLED:-1}" "$NATIVE_INTERFACE"
  elif [[ -n "$NATIVE_INTERFACE" ]]; then
    run_profile "$browser" native-web "$NATIVE_FILTER" 0 "$NATIVE_INTERFACE"
    run_profile "$browser" web-web "$WEB_FILTER" 1 ""
  else
    run_profile "$browser" combined "$ALL_FILTER" "${KIGO_SMOKE_TURN_ENABLED:-1}" "${KIGO_SMOKE_NATIVE_INTERFACE:-}"
  fi
done

if [[ "$failed" != "0" ]]; then
  exit 1
fi
echo "all browser matrix smoke checks passed"
