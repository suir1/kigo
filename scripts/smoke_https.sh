#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
export KIGO_CONFIG_PATH="$WORK/config.json"
BIN=${KIGO_BIN:-"$WORK/kigo"}
PID=""

cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

find_free_addr() {
  local port
  for port in $(seq 18220 18259); do
    if ! nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      printf '127.0.0.1:%s\n' "$port"
      return 0
    fi
  done
  return 1
}

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required for the HTTPS smoke test" >&2
  exit 2
fi
if [[ -z "${KIGO_BIN:-}" ]]; then
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kigo)
fi
if ! LISTEN=$(find_free_addr); then
  echo "No free HTTPS smoke port found in 18220..18259." >&2
  exit 2
fi

openssl req -x509 -newkey rsa:2048 -sha256 -nodes \
  -keyout "$WORK/key.pem" \
  -out "$WORK/cert.pem" \
  -days 1 \
  -subj "/CN=localhost" \
  >/dev/null 2>&1

"$BIN" serve \
  --listen "$LISTEN" \
  --public-url "https://$LISTEN" \
  --tls-cert "$WORK/cert.pem" \
  --tls-key "$WORK/key.pem" \
  --check-config \
  | grep -q '^configuration valid$'

if "$BIN" serve --listen "$LISTEN" --tls-cert "$WORK/cert.pem" --check-config >"$WORK/invalid.log" 2>&1; then
  echo "serve accepted a TLS certificate without a key" >&2
  exit 1
fi
grep -q 'TLS certificate and key must be configured together' "$WORK/invalid.log"

"$BIN" serve \
  --listen "$LISTEN" \
  --public-url "https://$LISTEN" \
  --tls-cert "$WORK/cert.pem" \
  --tls-key "$WORK/key.pem" \
  >"$WORK/service.log" 2>"$WORK/service.err" &
PID=$!

for _ in $(seq 1 80); do
  if curl -kfsS "https://$LISTEN/api/health" >"$WORK/health.json" 2>/dev/null; then
    break
  fi
  sleep 0.1
done
curl -kfsS "https://$LISTEN/api/health" >"$WORK/health.json"
grep -q '"ok":true' "$WORK/health.json"
grep -q "\"public_url\":\"https://$LISTEN\"" "$WORK/health.json"

curl -kfsSI "https://$LISTEN/" >"$WORK/headers.txt"
grep -qi '^content-security-policy:' "$WORK/headers.txt"
grep -qi '^cross-origin-opener-policy: same-origin' "$WORK/headers.txt"
grep -qi '^permissions-policy: camera=(), microphone=(), geolocation=()' "$WORK/headers.txt"
grep -qi '^referrer-policy: no-referrer' "$WORK/headers.txt"
grep -qi '^strict-transport-security: max-age=31536000' "$WORK/headers.txt"
grep -qi '^x-content-type-options: nosniff' "$WORK/headers.txt"
grep -qi '^x-frame-options: DENY' "$WORK/headers.txt"

curl -kfsSI "https://$LISTEN/api/health" >"$WORK/api-headers.txt"
grep -qi '^cache-control: no-store' "$WORK/api-headers.txt"
grep -q "kigo service listening on https://$LISTEN" "$WORK/service.err"

echo "all HTTPS smoke checks passed"
