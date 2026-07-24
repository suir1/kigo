#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BIN=${KIGO_BIN:-}
LISTEN=${KIGO_RELAY_LISTEN:-127.0.0.1:19090}
PASS=${KIGO_RELAY_PASS:-relay-smoke-secret}
TOKEN_SECRET=${KIGO_RELAY_TOKEN_SECRET:-relay-token-smoke-secret}
DISCOVERY_ADDR=${KIGO_DISCOVERY_ADDR:-127.0.0.1:19091}
SIGNAL_LISTEN=${KIGO_SIGNAL_LISTEN:-127.0.0.1:19092}
BASE_URL=${KIGO_BASE_URL:-http://$SIGNAL_LISTEN}
DIRECT_SIGNAL_LISTEN=${KIGO_DIRECT_SIGNAL_LISTEN:-127.0.0.1:19093}
DIRECT_BASE_URL=${KIGO_DIRECT_BASE_URL:-http://$DIRECT_SIGNAL_LISTEN}
PROXY_LISTEN=${KIGO_PROXY_LISTEN:-127.0.0.1:19094}

WORK=$(mktemp -d)
export KIGO_CONFIG_PATH="$WORK/config.json"
RELAY_PID=""
SERVICE_PID=""
DIRECT_SERVICE_PID=""
PROXY_PID=""
HISTORY="$WORK/route-history.json"
RELAY_FALLBACK_HISTORY="$WORK/relay-fallback-route-history.json"
WEBRTC_FALLBACK_HISTORY="$WORK/webrtc-fallback-route-history.json"

cleanup() {
  if [[ -n "$RELAY_PID" ]]; then
    kill "$RELAY_PID" 2>/dev/null || true
    wait "$RELAY_PID" 2>/dev/null || true
  fi
  if [[ -n "$SERVICE_PID" ]]; then
    kill "$SERVICE_PID" 2>/dev/null || true
    wait "$SERVICE_PID" 2>/dev/null || true
  fi
  if [[ -n "$DIRECT_SERVICE_PID" ]]; then
    kill "$DIRECT_SERVICE_PID" 2>/dev/null || true
    wait "$DIRECT_SERVICE_PID" 2>/dev/null || true
  fi
  if [[ -n "$PROXY_PID" ]]; then
    kill "$PROXY_PID" 2>/dev/null || true
    wait "$PROXY_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if [[ -z "$BIN" ]]; then
  BIN="$WORK/kigo"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kigo)
fi

"$BIN" relay --listen "$LISTEN" --room-ttl 2m --pass "$PASS" --token-secret "$TOKEN_SECRET" --discovery-addr "$DISCOVERY_ADDR" >"$WORK/relay.log" 2>"$WORK/relay.err" &
RELAY_PID=$!
"$BIN" serve --listen "$SIGNAL_LISTEN" --native-relay "$LISTEN" \
  --native-relay-secret "$TOKEN_SECRET" \
  --native-relay-credential-ttl 5m \
  >"$WORK/service.log" 2>"$WORK/service.err" &
SERVICE_PID=$!
"$BIN" serve --listen "$DIRECT_SIGNAL_LISTEN" >"$WORK/direct-service.log" 2>"$WORK/direct-service.err" &
DIRECT_SERVICE_PID=$!
python3 "$ROOT/scripts/http_connect_proxy.py" --listen "$PROXY_LISTEN" >"$WORK/proxy.log" 2>"$WORK/proxy.err" &
PROXY_PID=$!
unset KIGO_RELAY_PASS

for _ in $(seq 1 80); do
  if nc -z "${LISTEN%:*}" "${LISTEN##*:}" >/dev/null 2>&1 &&
     curl -fsS "$BASE_URL/api/health" >/dev/null 2>&1 &&
     curl -fsS "$DIRECT_BASE_URL/api/health" >/dev/null 2>&1 &&
     nc -z "${PROXY_LISTEN%:*}" "${PROXY_LISTEN##*:}" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
nc -z "${LISTEN%:*}" "${LISTEN##*:}" >/dev/null 2>&1
curl -fsS "$BASE_URL/api/health" >"$WORK/health.json"
grep -q '"endpoint":"'"$LISTEN"'"' "$WORK/health.json"
grep -q '"credential_mode":"temporary"' "$WORK/health.json"
curl -fsS "$DIRECT_BASE_URL/api/health" >/dev/null

echo "relay: $LISTEN"
"$BIN" --route-history "$HISTORY" --signal "$BASE_URL" --relay "$LISTEN" --relay-pass "$PASS" doctor --timeout 2s >"$WORK/doctor.log"
grep -q 'relay-observed TCP mapping:' "$WORK/doctor.log"
"$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" route --json --pair native-native --timeout 2s >"$WORK/route.json"
grep -q '"pair": "native-native"' "$WORK/route.json"
grep -q '"scope": {' "$WORK/route.json"
grep -q '"probe": {' "$WORK/route.json"
grep -q '"latency_ms":' "$WORK/route.json"
"$BIN" --route-history "$HISTORY" --signal "$BASE_URL" --relay "$LISTEN" --relay-pass "$PASS" route --json --pair native-native --timeout 2s >"$WORK/signal-route.json"
grep -q '"kind": "signal-direct"' "$WORK/signal-route.json"
grep -q '"primary": true' "$WORK/signal-route.json"
echo "ok doctor relay"
"$BIN" --route-history "$HISTORY" --signal "$BASE_URL" --relay "$LISTEN" --relay-pass "$PASS" \
  --proxy "http://$PROXY_LISTEN" doctor --timeout 2s >"$WORK/doctor-proxy.log"
grep -q 'relay: ok .* via proxy' "$WORK/doctor-proxy.log"
grep -q 'direct: disabled by --proxy' "$WORK/doctor-proxy.log"
echo "ok doctor relay through proxy"

set +e
"$BIN" --relay "$LISTEN" --relay-pass "$PASS" --transport native --no-lan \
  --pair-timeout 150ms text send "pair-timeout-smoke" \
  >"$WORK/pair-timeout.log" 2>"$WORK/pair-timeout.err"
pair_timeout_status=$?
set -e
if [[ $pair_timeout_status -eq 0 ]] ||
   ! grep -q 'pairing timed out after 150ms waiting for peer' "$WORK/pair-timeout.err"; then
  echo "pair-timeout: sender did not stop at the configured pairing deadline" >&2
  sed -n '1,120p' "$WORK/pair-timeout.log" >&2 || true
  sed -n '1,120p' "$WORK/pair-timeout.err" >&2 || true
  exit 1
fi
echo "ok client pairing timeout"

wait_for_code() {
  local log=$1
  local code=""
  for _ in $(seq 1 80); do
    code=$(sed -n 's/^Code: //p' "$log" | head -1)
    if [[ -n "$code" ]]; then
      printf '%s\n' "$code"
      return 0
    fi
    sleep 0.1
  done
  return 1
}

run_file_pair() {
  local label=$1
  shift
  local dir="$WORK/$label"
  local src="$dir/input.txt"
  local out="$dir/out"
  mkdir -p "$out"
  if [[ "$label" == "relay-fallback" || "$label" == "direct-via-relay" || "$label" == "weighted-history" ]]; then
    : >"$src"
    for i in $(seq 1 12000); do
      printf 'striped-line-%05d abcdefghijklmnopqrstuvwxyz\n' "$i" >>"$src"
    done
  else
    printf '%s payload %s\n' "$label" "$(date +%s)" >"$src"
  fi

  "$BIN" --route-history "$HISTORY" "$@" send "$src" >"$dir.send.log" 2>"$dir.send.err" &
  local send_pid=$!
  local code
  if ! code=$(wait_for_code "$dir.send.log"); then
    echo "$label: sender did not emit code" >&2
    sed -n '1,120p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    kill "$send_pid" 2>/dev/null || true
    return 1
  fi
  # The deterministic smoke uses unicast discovery, which permits one listener
  # at a time. Production multicast discovery supports concurrent listeners.
  if [[ "$label" == "lan-discovery" ]]; then
    sleep 0.8
  fi

  set +e
  "$BIN" --route-history "$HISTORY" "$@" recv "$code" --out "$out" >"$dir.recv.log" 2>"$dir.recv.err"
  local recv_status=$?
  if [[ $recv_status -ne 0 ]]; then
    kill "$send_pid" 2>/dev/null || true
  fi
  wait "$send_pid"
  local send_status=$?
  set -e
  if [[ $recv_status -ne 0 || $send_status -ne 0 ]]; then
    echo "$label: send_status=$send_status recv_status=$recv_status" >&2
    sed -n '1,160p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    sed -n '1,160p' "$dir.recv.log" >&2 || true
    sed -n '1,120p' "$dir.recv.err" >&2 || true
    return 1
  fi
  cmp "$src" "$out/input.txt"
  if [[ "$label" == "relay-fallback" ]]; then
    if ! grep -q 'Route: relay, connections: 4' "$dir.send.log" ||
       ! grep -q 'Route: relay, connections: 4' "$dir.recv.log"; then
      echo "$label: four-connection relay route was not negotiated" >&2
      sed -n '1,160p' "$dir.send.log" >&2 || true
      sed -n '1,160p' "$dir.recv.log" >&2 || true
      return 1
    fi
    if ! grep -q 'chunk striping enabled' "$dir.send.log" ||
       ! grep -q 'chunk striping enabled' "$dir.recv.log"; then
      echo "$label: chunk striping capability was not negotiated" >&2
      sed -n '1,180p' "$dir.send.log" >&2 || true
      sed -n '1,180p' "$dir.recv.log" >&2 || true
      return 1
    fi
    local path_count
    path_count=$(grep -Ec ' path [123] sent ' "$dir.send.log" || true)
    if [[ "$path_count" -ne 3 ]]; then
      echo "$label: expected stats from all three data-path workers, got $path_count" >&2
      sed -n '1,220p' "$dir.send.log" >&2 || true
      return 1
    fi
  fi
  echo "ok $label"
}

run_resume_pair() {
  local label="resume-relay"
  local dir="$WORK/$label"
  local src="$dir/input.txt"
  local out="$dir/out"
  local part="$out/input.txt.kigopart"
  mkdir -p "$out"
  : >"$src"
  for i in $(seq 1 12000); do
    printf 'resume-line-%05d abcdefghijklmnopqrstuvwxyz\n' "$i" >>"$src"
  done
  head -c 180000 "$src" >"$part"

  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan send "$src" >"$dir.send.log" 2>"$dir.send.err" &
  local send_pid=$!
  local code
  if ! code=$(wait_for_code "$dir.send.log"); then
    echo "$label: sender did not emit code" >&2
    sed -n '1,120p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    kill "$send_pid" 2>/dev/null || true
    return 1
  fi

  set +e
  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan recv "$code" --out "$out" >"$dir.recv.log" 2>"$dir.recv.err"
  local recv_status=$?
  if [[ $recv_status -ne 0 ]]; then
    kill "$send_pid" 2>/dev/null || true
  fi
  wait "$send_pid"
  local send_status=$?
  set -e
  if [[ $recv_status -ne 0 || $send_status -ne 0 ]]; then
    echo "$label: send_status=$send_status recv_status=$recv_status" >&2
    sed -n '1,180p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    sed -n '1,180p' "$dir.recv.log" >&2 || true
    sed -n '1,120p' "$dir.recv.err" >&2 || true
    return 1
  fi
  cmp "$src" "$out/input.txt"
  if ! grep -q 'resuming input.txt from 180000/' "$dir.send.log"; then
    echo "$label: sender did not log resume offset" >&2
    sed -n '1,180p' "$dir.send.log" >&2
    return 1
  fi
  if ! grep -q 'resuming input.txt from 180000/' "$dir.recv.log"; then
    echo "$label: receiver did not log resume offset" >&2
    sed -n '1,180p' "$dir.recv.log" >&2
    return 1
  fi
  if [[ -e "$part" ]]; then
    echo "$label: partial file still exists after completion" >&2
    return 1
  fi

  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan send "$src" >"$dir.duplicate.send.log" 2>"$dir.duplicate.send.err" &
  local duplicate_send_pid=$!
  local duplicate_code
  if ! duplicate_code=$(wait_for_code "$dir.duplicate.send.log"); then
    echo "$label: duplicate sender did not emit code" >&2
    kill "$duplicate_send_pid" 2>/dev/null || true
    return 1
  fi
  set +e
  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan recv "$duplicate_code" --out "$out" >"$dir.duplicate.recv.log" 2>"$dir.duplicate.recv.err"
  local duplicate_recv_status=$?
  if [[ $duplicate_recv_status -ne 0 ]]; then
    kill "$duplicate_send_pid" 2>/dev/null || true
  fi
  wait "$duplicate_send_pid"
  local duplicate_send_status=$?
  set -e
  if [[ $duplicate_recv_status -ne 0 || $duplicate_send_status -ne 0 ]] ||
     ! grep -q 'skipped already-complete input.txt' "$dir.duplicate.send.log"; then
    echo "$label: completed-file fast skip failed" >&2
    sed -n '1,180p' "$dir.duplicate.send.log" >&2 || true
    sed -n '1,180p' "$dir.duplicate.recv.log" >&2 || true
    return 1
  fi
  cmp "$src" "$out/input.txt"
  if [[ -e "$part" ]]; then
    echo "$label: duplicate skip created a partial file" >&2
    return 1
  fi
  echo "ok $label"
}

run_text_pair() {
  local label=${1:-text-relay}
  local custom_code=${2:-}
  local dir="$WORK/$label"
  local payload="hello from $label $(date +%s)"
  mkdir -p "$dir"

  local code_args=()
  if [[ -n "$custom_code" ]]; then
    code_args=(--code "$custom_code")
  fi
  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan text send "$payload" "${code_args[@]}" >"$dir.send.log" 2>"$dir.send.err" &
  local send_pid=$!
  local code
  if ! code=$(wait_for_code "$dir.send.log"); then
    echo "$label: sender did not emit code" >&2
    sed -n '1,120p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    kill "$send_pid" 2>/dev/null || true
    return 1
  fi
  if [[ -n "$custom_code" && "$code" != "$custom_code" ]]; then
    echo "$label: sender normalized code '$code', expected '$custom_code'" >&2
    return 1
  fi

  set +e
  "$BIN" --route-history "$HISTORY" --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan text recv "$code" >"$dir.recv.log" 2>"$dir.recv.err"
  local recv_status=$?
  if [[ $recv_status -ne 0 ]]; then
    kill "$send_pid" 2>/dev/null || true
  fi
  wait "$send_pid"
  local send_status=$?
  set -e
  if [[ $recv_status -ne 0 || $send_status -ne 0 ]]; then
    echo "$label: send_status=$send_status recv_status=$recv_status" >&2
    sed -n '1,160p' "$dir.send.log" >&2 || true
    sed -n '1,120p' "$dir.send.err" >&2 || true
    sed -n '1,160p' "$dir.recv.log" >&2 || true
    sed -n '1,120p' "$dir.recv.err" >&2 || true
    return 1
  fi
  if ! grep -Fqx "$payload" "$dir.recv.log"; then
    echo "$label: receiver output did not contain exact payload" >&2
    sed -n '1,160p' "$dir.recv.log" >&2 || true
    return 1
  fi
  echo "ok $label"
}

run_file_pair relay-fallback --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan
run_file_pair relay-proxy --transport native --relay "$LISTEN" --relay-pass "$PASS" --proxy "http://$PROXY_LISTEN" --no-lan
if ! grep -q 'Route: relay, connections: 4' "$WORK/relay-proxy.send.log" ||
   ! grep -q 'Route: relay, connections: 4' "$WORK/relay-proxy.recv.log"; then
  echo "relay-proxy: multi-connection relay did not use the proxy route" >&2
  sed -n '1,180p' "$WORK/relay-proxy.send.log" >&2 || true
  sed -n '1,180p' "$WORK/relay-proxy.recv.log" >&2 || true
  exit 1
fi
proxy_connects=$(grep -c "CONNECT $LISTEN" "$WORK/proxy.log" || true)
if [[ "$proxy_connects" -lt 9 ]]; then
  echo "relay-proxy: expected doctor plus primary/auxiliary proxy tunnels, got $proxy_connects" >&2
  sed -n '1,160p' "$WORK/proxy.log" >&2 || true
  exit 1
fi
echo "ok native relay through HTTP CONNECT proxy"
run_file_pair auto-native --signal "$BASE_URL" --no-direct --no-lan
if ! grep -q 'Route negotiation: native' "$WORK/auto-native.send.log" ||
   ! grep -q 'Route negotiation: native' "$WORK/auto-native.recv.log" ||
   ! grep -q "Negotiated relay: $LISTEN" "$WORK/auto-native.send.log" ||
   ! grep -q "Negotiated relay: $LISTEN" "$WORK/auto-native.recv.log" ||
   ! grep -q 'Negotiated relay credential: temporary' "$WORK/auto-native.send.log" ||
   ! grep -q 'Negotiated relay credential: temporary' "$WORK/auto-native.recv.log" ||
   ! grep -q 'Route: relay' "$WORK/auto-native.send.log" ||
   ! grep -q 'Route: relay' "$WORK/auto-native.recv.log"; then
  echo "auto-native: capability negotiation did not select the advertised relay" >&2
  sed -n '1,180p' "$WORK/auto-native.send.log" >&2 || true
  sed -n '1,180p' "$WORK/auto-native.recv.log" >&2 || true
  exit 1
fi
echo "ok automatic native route negotiation"

run_file_pair signal-direct-only --transport native --signal "$BASE_URL" --direct-listen 127.0.0.1:0 --no-lan
if ! grep -q 'Direct mode: synchronized bidirectional TCP' "$WORK/signal-direct-only.send.log" ||
   ! grep -q 'Direct mode: synchronized bidirectional TCP' "$WORK/signal-direct-only.recv.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-only.send.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-only.recv.log"; then
  echo "signal-direct-only: signaling rendezvous did not establish direct TCP" >&2
  sed -n '1,180p' "$WORK/signal-direct-only.send.log" >&2 || true
  sed -n '1,180p' "$WORK/signal-direct-only.recv.log" >&2 || true
  exit 1
fi
echo "ok signaling-only direct route"

run_file_pair signal-direct-public-probe --signal "$BASE_URL" --direct-listen 127.0.0.1:0 --no-lan
if ! grep -q 'TCP public probe:' "$WORK/signal-direct-public-probe.send.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-public-probe.send.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-public-probe.recv.log"; then
  echo "signal-direct-public-probe: relay-observed mapping did not preserve signaling direct" >&2
  sed -n '1,200p' "$WORK/signal-direct-public-probe.send.log" >&2 || true
  sed -n '1,200p' "$WORK/signal-direct-public-probe.recv.log" >&2 || true
  exit 1
fi
echo "ok signaling direct TCP public probe"

run_file_pair signal-direct-udp-probe --transport native --signal "$BASE_URL" --udp-probe --direct-listen 127.0.0.1:0 --no-lan
if ! grep -q 'Direct timeout:' "$WORK/signal-direct-udp-probe.send.log" ||
   ! grep -q 'Direct timeout:' "$WORK/signal-direct-udp-probe.recv.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-udp-probe.send.log" ||
   ! grep -q 'Route: direct via signaling, connections: 4' "$WORK/signal-direct-udp-probe.recv.log"; then
  echo "signal-direct-udp-probe: peers did not exchange NAT probe capability" >&2
  sed -n '1,220p' "$WORK/signal-direct-udp-probe.send.log" >&2 || true
  sed -n '1,220p' "$WORK/signal-direct-udp-probe.recv.log" >&2 || true
  exit 1
fi
echo "ok signaling direct NAT timeout adaptation"

run_file_pair signal-direct-relay-fallback --route-history "$RELAY_FALLBACK_HISTORY" --signal "$BASE_URL" --direct-advertise 127.0.0.1:1 --direct-timeout 200ms --no-lan
if ! grep -q 'Direct fallback:' "$WORK/signal-direct-relay-fallback.send.log" ||
   ! grep -q 'Direct fallback:' "$WORK/signal-direct-relay-fallback.recv.log" ||
   ! grep -q 'Route: relay' "$WORK/signal-direct-relay-fallback.send.log" ||
   ! grep -q 'Route: relay' "$WORK/signal-direct-relay-fallback.recv.log"; then
  echo "signal-direct-relay-fallback: failed direct route did not converge on relay" >&2
  sed -n '1,200p' "$WORK/signal-direct-relay-fallback.send.log" >&2 || true
  sed -n '1,200p' "$WORK/signal-direct-relay-fallback.recv.log" >&2 || true
  exit 1
fi
echo "ok signaling direct to relay fallback"

run_file_pair signal-direct-webrtc-fallback --route-history "$WEBRTC_FALLBACK_HISTORY" --signal "$DIRECT_BASE_URL" --direct-advertise 127.0.0.1:1 --direct-timeout 200ms --no-lan
if ! grep -q 'Route fallback: WebRTC' "$WORK/signal-direct-webrtc-fallback.send.log" ||
   ! grep -q 'Route fallback: WebRTC' "$WORK/signal-direct-webrtc-fallback.recv.log"; then
  echo "signal-direct-webrtc-fallback: failed direct route did not converge on WebRTC" >&2
  sed -n '1,220p' "$WORK/signal-direct-webrtc-fallback.send.log" >&2 || true
  sed -n '1,220p' "$WORK/signal-direct-webrtc-fallback.recv.log" >&2 || true
  exit 1
fi
echo "ok signaling direct to WebRTC fallback"

lan_upgrade_dir="$WORK/lan-upgrade"
mkdir -p "$lan_upgrade_dir/out"
printf 'post relay LAN upgrade payload\n' >"$lan_upgrade_dir/input.txt"
"$BIN" --route-history "$HISTORY" --signal http://127.0.0.1:19103 --relay "$LISTEN" --relay-pass "$PASS" \
  --direct-listen 127.0.0.1:0 --direct-advertise 127.0.0.1:1 --direct-timeout 200ms \
  --discovery-addr 127.0.0.1:19101 --lan-discovery-timeout 100ms \
  send "$lan_upgrade_dir/input.txt" >"$lan_upgrade_dir.send.log" 2>"$lan_upgrade_dir.send.err" &
lan_upgrade_sender=$!
lan_upgrade_code=$(wait_for_code "$lan_upgrade_dir.send.log")
set +e
"$BIN" --route-history "$HISTORY" --signal http://127.0.0.1:19103 --relay "$LISTEN" --relay-pass "$PASS" \
  --direct-listen 127.0.0.1:0 --direct-advertise 127.0.0.1:1 --direct-timeout 200ms \
  --discovery-addr 127.0.0.1:19102 --lan-discovery-timeout 100ms \
  recv "$lan_upgrade_code" --out "$lan_upgrade_dir/out" >"$lan_upgrade_dir.recv.log" 2>"$lan_upgrade_dir.recv.err"
lan_upgrade_recv_status=$?
if [[ $lan_upgrade_recv_status -ne 0 ]]; then
  kill "$lan_upgrade_sender" 2>/dev/null || true
fi
wait "$lan_upgrade_sender"
lan_upgrade_send_status=$?
set -e
if [[ $lan_upgrade_send_status -ne 0 || $lan_upgrade_recv_status -ne 0 ]] ||
   ! cmp "$lan_upgrade_dir/input.txt" "$lan_upgrade_dir/out/input.txt" ||
   ! grep -q 'Route: LAN direct upgrade, connections: 4' "$lan_upgrade_dir.send.log" ||
   ! grep -q 'Route: LAN direct upgrade, connections: 4' "$lan_upgrade_dir.recv.log"; then
  echo "lan-upgrade: external relay did not upgrade to LAN direct" >&2
  sed -n '1,220p' "$lan_upgrade_dir.send.log" >&2 || true
  sed -n '1,160p' "$lan_upgrade_dir.send.err" >&2 || true
  sed -n '1,220p' "$lan_upgrade_dir.recv.log" >&2 || true
  sed -n '1,160p' "$lan_upgrade_dir.recv.err" >&2 || true
  exit 1
fi
echo "ok post-relay LAN direct upgrade"

run_text_pair
run_text_pair text-custom NATIVE-TEXT-CUSTOM-2026
run_resume_pair

run_file_pair direct-via-relay --signal http://127.0.0.1:19103 \
  --relay "$LISTEN" --relay-pass "$PASS" --direct-listen 127.0.0.1:0 --no-lan
if ! grep -q 'Direct mode: synchronized bidirectional TCP' "$WORK/direct-via-relay.send.log" ||
   ! grep -q 'Direct mode: synchronized bidirectional TCP' "$WORK/direct-via-relay.recv.log"; then
  echo "direct-via-relay: peers did not negotiate synchronized bidirectional direct" >&2
  sed -n '1,180p' "$WORK/direct-via-relay.send.log" >&2
  sed -n '1,180p' "$WORK/direct-via-relay.recv.log" >&2
  exit 1
fi
if ! grep -q 'Route: direct, connections: 4' "$WORK/direct-via-relay.send.log"; then
  echo "direct-via-relay: sender did not report direct route" >&2
  sed -n '1,160p' "$WORK/direct-via-relay.send.log" >&2
  exit 1
fi
if ! grep -q 'Route: direct, connections: 4' "$WORK/direct-via-relay.recv.log"; then
  echo "direct-via-relay: receiver did not report direct route" >&2
  sed -n '1,160p' "$WORK/direct-via-relay.recv.log" >&2
  exit 1
fi
direct_path_count=$(grep -Ec ' path [123] sent ' "$WORK/direct-via-relay.send.log" || true)
if [[ "$direct_path_count" -ne 3 ]]; then
  echo "direct-via-relay: expected stats from all three direct data paths, got $direct_path_count" >&2
  sed -n '1,220p' "$WORK/direct-via-relay.send.log" >&2
  exit 1
fi
echo "ok direct route selected"

now_ms=$(($(date +%s) * 1000))
scope_id=$(sed -n 's/.*"id": "\([^"]*\)".*/\1/p' "$WORK/route.json" | head -1)
if [[ -z "$scope_id" ]]; then
  echo "could not read network scope from route report" >&2
  sed -n '1,180p' "$WORK/route.json" >&2
  exit 1
fi
cat >"$HISTORY" <<EOF
{
  "version": 2,
  "profiles": {
    "$scope_id": {
      "last_seen": $now_ms,
      "routes": {
        "relay": {
          "attempts": 3,
          "successes": 3,
          "paths": {
            "1": {"samples": 3, "sent_bytes": 3145728, "send_nanos": 3000000000, "ewma_bytes_per_second": 1048576},
            "2": {"samples": 3, "sent_bytes": 3145728, "send_nanos": 3000000000, "ewma_bytes_per_second": 1048576},
            "3": {"samples": 3, "sent_bytes": 25165824, "send_nanos": 3000000000, "ewma_bytes_per_second": 8388608}
          }
        }
      }
    }
  }
}
EOF
run_file_pair weighted-history --relay "$LISTEN" --relay-pass "$PASS" --no-direct --no-lan
if ! grep -q 'Historical path weights: p1=0.50 p2=0.50 p3=2.00' "$WORK/weighted-history.send.log"; then
  echo "weighted-history: sender did not load historical path weights" >&2
  sed -n '1,220p' "$WORK/weighted-history.send.log" >&2
  exit 1
fi
path1_chunks=$(sed -n 's/.*path 1 sent .* in \([0-9][0-9]*\) chunk.*/\1/p' "$WORK/weighted-history.send.log" | tail -1)
path2_chunks=$(sed -n 's/.*path 2 sent .* in \([0-9][0-9]*\) chunk.*/\1/p' "$WORK/weighted-history.send.log" | tail -1)
path3_chunks=$(sed -n 's/.*path 3 sent .* in \([0-9][0-9]*\) chunk.*/\1/p' "$WORK/weighted-history.send.log" | tail -1)
path1_chunks=${path1_chunks:-0}
path2_chunks=${path2_chunks:-0}
path3_chunks=${path3_chunks:-0}
if (( path3_chunks <= path1_chunks || path3_chunks <= path2_chunks )); then
  echo "weighted-history: high-weight path was not favored (p1=$path1_chunks p2=$path2_chunks p3=$path3_chunks)" >&2
  sed -n '1,240p' "$WORK/weighted-history.send.log" >&2
  exit 1
fi
echo "ok adaptive historical path weights"

printf '{\n  "version": 1,\n  "routes": {\n    "direct": {\n      "attempts": 3,\n      "successes": 0,\n      "failures": 3,\n      "consecutive_failures": 3,\n      "sent_bytes": 0,\n      "received_bytes": 0,\n      "duration_ms": 2700,\n      "ewma_bytes_per_second": 0,\n      "last_failure": %s\n    }\n  }\n}\n' "$now_ms" >"$HISTORY"
run_file_pair history-deferred --signal http://127.0.0.1:19103 \
  --relay "$LISTEN" --relay-pass "$PASS" --direct-listen 127.0.0.1:0 --no-lan
if ! grep -q 'Route preference: relay' "$WORK/history-deferred.send.log" ||
   ! grep -q 'Route preference: relay' "$WORK/history-deferred.recv.log"; then
  echo "history-deferred: peers did not negotiate relay preference" >&2
  sed -n '1,180p' "$WORK/history-deferred.send.log" >&2
  sed -n '1,180p' "$WORK/history-deferred.recv.log" >&2
  exit 1
fi
if ! grep -q 'Route: relay, connections: 4' "$WORK/history-deferred.send.log" ||
   ! grep -q 'Route: relay, connections: 4' "$WORK/history-deferred.recv.log"; then
  echo "history-deferred: negotiated preference did not select relay" >&2
  sed -n '1,180p' "$WORK/history-deferred.send.log" >&2
  sed -n '1,180p' "$WORK/history-deferred.recv.log" >&2
  exit 1
fi
echo "ok historical route negotiation"

run_file_pair lan-discovery --local --relay-pass "$PASS" --discovery-addr "$DISCOVERY_ADDR" --lan-discovery-timeout 600ms
if ! grep -q 'Relay route: embedded' "$WORK/lan-discovery.send.log"; then
  echo "lan-discovery: sender did not select its embedded LAN relay" >&2
  sed -n '1,160p' "$WORK/lan-discovery.send.log" >&2
  exit 1
fi
if ! grep -q 'Relay route: lan' "$WORK/lan-discovery.recv.log"; then
  echo "lan-discovery: receiver did not discover the embedded LAN relay" >&2
  sed -n '1,160p' "$WORK/lan-discovery.recv.log" >&2
  exit 1
fi
echo "ok embedded LAN relay discovery"

if "$BIN" --route-history "$HISTORY" --signal http://127.0.0.1:19103 \
  --relay "$LISTEN" --relay-pass wrong recv ABC123 --out "$WORK/wrong-pass" \
  >"$WORK/wrong-pass.log" 2>"$WORK/wrong-pass.err"; then
  echo "wrong relay password was accepted" >&2
  exit 1
fi
if ! grep -q 'relay password rejected' "$WORK/wrong-pass.err"; then
  echo "wrong password failure did not mention relay password rejection" >&2
  sed -n '1,120p' "$WORK/wrong-pass.log" >&2
  sed -n '1,120p' "$WORK/wrong-pass.err" >&2
  exit 1
fi
echo "ok relay password rejection"

if ! grep -q '"version": 2' "$HISTORY" ||
   ! grep -q '"profiles": {' "$HISTORY" ||
   ! grep -q '"direct": {' "$HISTORY" ||
   ! grep -q '"relay": {' "$HISTORY" ||
   ! grep -q '"paths": {' "$HISTORY"; then
  echo "route history did not record both direct and relay observations" >&2
  sed -n '1,200p' "$HISTORY" >&2 || true
  exit 1
fi
echo "ok route history"

echo "all relay smoke checks passed"
