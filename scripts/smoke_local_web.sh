#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
BIN=${KIGO_BIN:-"$WORK/kigo"}
export KIGO_NOTE_DRAFT_PATH="$WORK/note-drafts"
export KIGO_NOTE_RECENTS_PATH="$WORK/note-recents.json"
export KIGO_CONFIG_PATH="$WORK/config.json"

find_free_addr() {
  local start=$1
  local end=$2
  local port
  for port in $(seq "$start" "$end"); do
    if ! nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      printf '127.0.0.1:%s\n' "$port"
      return 0
    fi
  done
  return 1
}

if [[ -n "${KIGO_LOCAL_WEB_SERVICE_ADDR:-}" ]]; then
  SERVICE_ADDR=$KIGO_LOCAL_WEB_SERVICE_ADDR
elif ! SERVICE_ADDR=$(find_free_addr 18140 18179); then
  echo "No free signaling port found in 18140..18179." >&2
  exit 2
fi

if [[ -n "${KIGO_LOCAL_WEB_ADDR:-}" ]]; then
  LOCAL_ADDR=$KIGO_LOCAL_WEB_ADDR
elif ! LOCAL_ADDR=$(find_free_addr 18180 18219); then
  echo "No free local web port found in 18180..18219." >&2
  exit 2
fi

SERVICE_URL="http://$SERVICE_ADDR"
LOCAL_URL="http://$LOCAL_ADDR"
SERVICE_PID=""
LOCAL_PID=""

cleanup() {
  if [[ -n "$LOCAL_PID" ]]; then
    kill "$LOCAL_PID" 2>/dev/null || true
    wait "$LOCAL_PID" 2>/dev/null || true
  fi
  if [[ -n "$SERVICE_PID" ]]; then
    kill "$SERVICE_PID" 2>/dev/null || true
    wait "$SERVICE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if [[ -z "${KIGO_BIN:-}" ]]; then
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kigo)
fi

"$BIN" serve --listen "$SERVICE_ADDR" >"$WORK/service.log" 2>"$WORK/service.err" &
SERVICE_PID=$!
for _ in $(seq 1 80); do
  if curl -fsS "$SERVICE_URL/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$SERVICE_URL/api/health" >/dev/null

"$BIN" config set service "$SERVICE_URL" >"$WORK/config-set.log"
"$BIN" config show --json >"$WORK/config-show.json"
grep -q "\"signal\": \"$SERVICE_URL\"" "$WORK/config-show.json"
grep -q "\"web_url\": \"$SERVICE_URL\"" "$WORK/config-show.json"

"$BIN" \
  --route-history "$WORK/route-history.json" \
  web --listen "$LOCAL_ADDR" --no-open \
  >"$WORK/local.log" 2>"$WORK/local.err" &
LOCAL_PID=$!
for _ in $(seq 1 80); do
  if grep -q '^Local web:' "$WORK/local.log" 2>/dev/null; then
    break
  fi
  sleep 0.1
done
token=$(sed -n 's|^Local web: .*#token=||p' "$WORK/local.log" | head -1)
if [[ -z "$token" ]]; then
  echo "local web did not print a tokenized URL" >&2
  cat "$WORK/local.log" "$WORK/local.err" >&2
  exit 1
fi

curl -fsS "$LOCAL_URL/" >"$WORK/index.html"
grep -q 'Kigo Local' "$WORK/index.html"
grep -q 'id="pathBrowserDialog"' "$WORK/index.html"
curl -fsS "$LOCAL_URL/local.js" >"$WORK/local.js"
node --check "$WORK/local.js"

status=$(curl -sS -o "$WORK/unauthorized.json" -w '%{http_code}' "$LOCAL_URL/api/config")
if [[ "$status" != "401" ]]; then
  echo "local web API accepted a request without its token" >&2
  exit 1
fi
curl -fsS -H "X-Kigo-Token: $token" "$LOCAL_URL/api/config" >"$WORK/config.json"
grep -q "\"signal\":\"$SERVICE_URL\"" "$WORK/config.json"

curl -fsS -X POST \
  -H "X-Kigo-Token: $token" \
  -H 'Content-Type: application/json' \
  -d '{"timeout":"2s"}' \
  "$LOCAL_URL/api/doctor" >/dev/null
for _ in $(seq 1 100); do
  curl -fsS -H "X-Kigo-Token: $token" "$LOCAL_URL/api/job" >"$WORK/job.json"
  if grep -q '"running":false' "$WORK/job.json"; then
    break
  fi
  sleep 0.1
done
grep -q '"kind":"doctor"' "$WORK/job.json"
grep -q '"failed":false' "$WORK/job.json"

printf 'local web smoke payload\n' >"$WORK/payload.txt"
curl -fsS -G \
  -H "X-Kigo-Token: $token" \
  --data-urlencode "path=$WORK" \
  --data-urlencode 'mode=send' \
  --data-urlencode 'sort=name' \
  "$LOCAL_URL/api/browse" >"$WORK/browse-send.json"
grep -q "\"current\":\"$WORK\"" "$WORK/browse-send.json"
grep -q '"name":"payload.txt"' "$WORK/browse-send.json"
curl -fsS -G \
  -H "X-Kigo-Token: $token" \
  --data-urlencode "path=$WORK" \
  --data-urlencode 'mode=directory' \
  "$LOCAL_URL/api/browse" >"$WORK/browse-directory.json"
if grep -q '"name":"payload.txt"' "$WORK/browse-directory.json"; then
  echo "directory-only browser listed a file" >&2
  exit 1
fi

curl -fsS -X POST \
  -H "X-Kigo-Token: $token" \
  -H 'Content-Type: application/json' \
  -d "{\"path\":\"$WORK/payload.txt\",\"code\":\"local-web-2026\",\"symlinks\":\"follow\"}" \
  "$LOCAL_URL/api/send" >/dev/null
for _ in $(seq 1 100); do
  curl -fsS -H "X-Kigo-Token: $token" "$LOCAL_URL/api/job" >"$WORK/job.json"
  if grep -q '"code":"' "$WORK/job.json"; then
    break
  fi
  sleep 0.1
done
grep -q '"code":"LOCAL-WEB-2026"' "$WORK/job.json"
grep -q "\"link\":\"$SERVICE_URL/#c=" "$WORK/job.json"

curl -fsS -X POST \
  -H "X-Kigo-Token: $token" \
  -H 'Content-Type: application/json' \
  -d '{}' \
  "$LOCAL_URL/api/job/cancel" >/dev/null
for _ in $(seq 1 100); do
  curl -fsS -H "X-Kigo-Token: $token" "$LOCAL_URL/api/job" >"$WORK/job.json"
  if grep -q '"running":false' "$WORK/job.json"; then
    break
  fi
  sleep 0.1
done
grep -q '"canceled":true' "$WORK/job.json"

python3 "$ROOT/scripts/smoke_local_web_note.py" \
  "$BIN" "$SERVICE_URL" "$LOCAL_URL" "$token"

echo "all local web smoke checks passed"
