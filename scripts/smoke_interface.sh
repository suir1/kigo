#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
export KIGO_CONFIG_PATH="$WORK/config.json"
BIN=${KIGO_BIN:-"$WORK/kigo"}
SERVICE_PID=""
RELAY_PID=""

cleanup() {
  if [[ -n "$RELAY_PID" ]]; then
    kill "$RELAY_PID" 2>/dev/null || true
    wait "$RELAY_PID" 2>/dev/null || true
  fi
  if [[ -n "$SERVICE_PID" ]]; then
    kill "$SERVICE_PID" 2>/dev/null || true
    wait "$SERVICE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

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

if [[ -n "${KIGO_TEST_INTERFACE:-}" ]]; then
  INTERFACE=$KIGO_TEST_INTERFACE
elif command -v ifconfig >/dev/null 2>&1 && ifconfig lo0 >/dev/null 2>&1; then
  INTERFACE=lo0
elif [[ -d /sys/class/net/lo ]]; then
  INTERFACE=lo
elif command -v ip >/dev/null 2>&1 && ip link show lo >/dev/null 2>&1; then
  INTERFACE=lo
elif command -v ifconfig >/dev/null 2>&1 && ifconfig lo >/dev/null 2>&1; then
  INTERFACE=lo
else
  echo "No loopback interface found; set KIGO_TEST_INTERFACE." >&2
  exit 2
fi

SERVICE_ADDR=$(find_free_addr 18220 18249)
RELAY_ADDR=$(find_free_addr 18250 18279)
SERVICE_URL="http://$SERVICE_ADDR"

if [[ -z "${KIGO_BIN:-}" ]]; then
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kigo)
fi

"$BIN" serve \
  --listen "$SERVICE_ADDR" \
  --turn-listen 127.0.0.1:0 \
  --turn-public-ip 127.0.0.1 \
  >"$WORK/service.log" 2>"$WORK/service.err" &
SERVICE_PID=$!
for _ in $(seq 1 80); do
  if curl -fsS "$SERVICE_URL/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$SERVICE_URL/api/health" >/dev/null

"$BIN" --interface "$INTERFACE" relay \
  --listen "$RELAY_ADDR" \
  --no-lan-announce \
  >"$WORK/relay.log" 2>"$WORK/relay.err" &
RELAY_PID=$!
for _ in $(seq 1 80); do
  if nc -z 127.0.0.1 "${RELAY_ADDR##*:}" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
nc -z 127.0.0.1 "${RELAY_ADDR##*:}"

COMMON=(
  --interface "$INTERFACE"
  --signal "$SERVICE_URL"
  --web-url "$SERVICE_URL"
  --no-lan
  --route-history "$WORK/route-history.json"
  --pair-timeout 15s
)

"$BIN" "${COMMON[@]}" --relay "$RELAY_ADDR" doctor --json >"$WORK/doctor.json"
python3 - "$WORK/doctor.json" "$INTERFACE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    report = json.load(handle)
assert report["network"]["policy"] == "interface", report["network"]
assert report["network"]["interface"] == sys.argv[2], report["network"]
assert report["network"]["addresses"], report["network"]
assert report["signal"]["ok"], report["signal"]
assert report["relay"]["ok"], report["relay"]
assert report["direct"]["ok"], report["direct"]
assert report["history"]["scope"]["source"] == "selected-interface", report["history"]
PY
echo "ok interface doctor"

run_text_pair() {
  local label=$1
  local code=$2
  shift 2
  local payload="interface smoke $label"
  "$BIN" "${COMMON[@]}" "$@" text send "$payload" --code "$code" \
    >"$WORK/$label-send.log" 2>"$WORK/$label-send.err" &
  local sender_pid=$!
  for _ in $(seq 1 100); do
    if grep -q "^Code: $code" "$WORK/$label-send.log" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  grep -q "^Code: $code" "$WORK/$label-send.log"
  "$BIN" "${COMMON[@]}" "$@" text recv "$code" \
    >"$WORK/$label-recv.log" 2>"$WORK/$label-recv.err"
  wait "$sender_pid"
  grep -qx "$payload" "$WORK/$label-recv.log"
  echo "ok interface $label"
}

run_text_pair relay INTERFACE-RELAY-2026 \
  --transport native --relay "$RELAY_ADDR" --no-direct
run_text_pair direct INTERFACE-DIRECT-2026 \
  --transport native --connections 2
run_text_pair webrtc INTERFACE-WEBRTC-2026 \
  --transport webrtc

if "$BIN" --interface kigo-interface-that-does-not-exist version >"$WORK/invalid.out" 2>"$WORK/invalid.err"; then
  echo "unknown network interface was accepted" >&2
  exit 1
fi
grep -q 'network interface' "$WORK/invalid.err"

if "$BIN" --interface "$INTERFACE" --direct-listen 192.0.2.20:0 version \
  >"$WORK/conflict.out" 2>"$WORK/conflict.err"; then
  echo "conflicting direct listen address was accepted" >&2
  exit 1
fi
grep -q 'does not belong to interface' "$WORK/conflict.err"

echo "all interface policy smoke checks passed"
