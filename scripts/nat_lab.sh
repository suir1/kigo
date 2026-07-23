#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
REPORT_TOOL="$ROOT/scripts/matrix_report.py"
ARTIFACT_DIR=${KIGO_ARTIFACT_DIR:-"$ROOT/artifacts/nat-lab"}
TRANSFER_TIMEOUT=${KIGO_NAT_TIMEOUT_SECONDS:-45}
PAYLOAD_BYTES=${KIGO_NAT_PAYLOAD_BYTES:-786432}

print_plan() {
  cat <<'JSON'
{
  "schema_version": 1,
  "kind": "nat-lab-plan",
  "scenarios": [
    {
      "name": "routed-open",
      "model": "two routed IPv4 endpoint networks",
      "expected_route": "direct"
    },
    {
      "name": "port-preserving-nat",
      "model": "separate SNAT routers with fixed port-preserving inbound mappings",
      "expected_route": "direct"
    },
    {
      "name": "relay-fallback",
      "model": "separate SNAT routers without inbound mappings",
      "expected_route": "relay"
    },
    {
      "name": "ipv6-routed",
      "model": "two routed IPv6 ULA endpoint networks",
      "expected_route": "direct",
      "optional": true
    }
  ],
  "limitations": [
    "The NAT cases use Linux conntrack and explicit namespace routers.",
    "The port-preserving case uses deterministic DNAT and is not a claim of cone-NAT discovery.",
    "The relay-fallback case is not a full CGNAT or carrier firewall reproduction.",
    "Real residential, mobile, enterprise, and CGNAT behavior requires the public endpoint matrix."
  ]
}
JSON
}

if [[ "${1:-}" == "--dry-run" ]]; then
  print_plan
  exit 0
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--dry-run]" >&2
  exit 2
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "nat_lab.sh requires Linux network namespaces; use --dry-run to inspect the plan" >&2
  exit 2
fi
if [[ $EUID -ne 0 ]]; then
  echo "nat_lab.sh must run as root, for example: sudo env KIGO_BIN=/path/to/kigo $0" >&2
  exit 2
fi
for command in ip iptables timeout sha256sum python3; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required for the NAT lab" >&2
    exit 2
  fi
done
ARTIFACT_DIR=$(python3 - "$ARTIFACT_DIR" <<'PY'
import os
import sys

print(os.path.abspath(os.path.expanduser(sys.argv[1])))
PY
)
if [[ "$ARTIFACT_DIR" == "/" || "$ARTIFACT_DIR" == "$ROOT" ]]; then
  echo "KIGO_ARTIFACT_DIR must be a dedicated output directory, not $ARTIFACT_DIR" >&2
  exit 2
fi
if [[ ! "$TRANSFER_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
  echo "KIGO_NAT_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 2
fi
if [[ ! "$PAYLOAD_BYTES" =~ ^[1-9][0-9]*$ ]]; then
  echo "KIGO_NAT_PAYLOAD_BYTES must be a positive integer" >&2
  exit 2
fi

WORK=$(mktemp -d)
BIN=${KIGO_BIN:-"$WORK/kigo"}
SUFFIX=$(printf '%04x' $(( $$ % 65536 )))
WAN="kg-wan-$SUFFIX"
ROUTER1="kg-r1-$SUFFIX"
ROUTER2="kg-r2-$SUFFIX"
SENDER="kg-s-$SUFFIX"
RECEIVER="kg-r-$SUFFIX"
SERVICE_PID=""
RELAY_PID=""
IPV6_AVAILABLE=0
SCENARIO_REPORTS=()

stop_group() {
  local pid=${1:-}
  if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
    return
  fi
  kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  sleep 0.2
  kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup() {
  stop_group "$SERVICE_PID"
  stop_group "$RELAY_PID"
  for namespace in "$SENDER" "$RECEIVER" "$ROUTER1" "$ROUTER2" "$WAN"; do
    for pid in $(ip netns pids "$namespace" 2>/dev/null || true); do
      kill -KILL "$pid" 2>/dev/null || true
    done
    ip netns del "$namespace" 2>/dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

if [[ -z "${KIGO_BIN:-}" ]]; then
  (cd "$ROOT" && go build -trimpath -o "$BIN" ./cmd/kigo)
fi
if [[ ! -x "$BIN" ]]; then
  echo "Kigo binary is not executable: $BIN" >&2
  exit 2
fi

rm -rf "$ARTIFACT_DIR"
mkdir -p "$ARTIFACT_DIR" "$WORK/raw"

create_pair() {
  local index=$1
  local left_ns=$2
  local left_name=$3
  local right_ns=$4
  local right_name=$5
  local left_host="kl${index}${SUFFIX}"
  local right_host="kr${index}${SUFFIX}"
  ip link add "$left_host" type veth peer name "$right_host"
  ip link set "$left_host" netns "$left_ns"
  ip link set "$right_host" netns "$right_ns"
  ip -n "$left_ns" link set "$left_host" name "$left_name"
  ip -n "$right_ns" link set "$right_host" name "$right_name"
}

for namespace in "$WAN" "$ROUTER1" "$ROUTER2" "$SENDER" "$RECEIVER"; do
  ip netns add "$namespace"
  ip -n "$namespace" link set lo up
done
create_pair 1 "$SENDER" eth0 "$ROUTER1" lan0
create_pair 2 "$ROUTER1" wan0 "$WAN" r1
create_pair 3 "$RECEIVER" eth0 "$ROUTER2" lan0
create_pair 4 "$ROUTER2" wan0 "$WAN" r2

ip -n "$SENDER" addr add 10.71.1.2/24 dev eth0
ip -n "$ROUTER1" addr add 10.71.1.1/24 dev lan0
ip -n "$ROUTER1" addr add 198.18.71.2/24 dev wan0
ip -n "$WAN" addr add 198.18.71.1/24 dev r1
ip -n "$RECEIVER" addr add 10.72.2.2/24 dev eth0
ip -n "$ROUTER2" addr add 10.72.2.1/24 dev lan0
ip -n "$ROUTER2" addr add 198.18.72.2/24 dev wan0
ip -n "$WAN" addr add 198.18.72.1/24 dev r2

for spec in \
  "$SENDER eth0" \
  "$ROUTER1 lan0" \
  "$ROUTER1 wan0" \
  "$WAN r1" \
  "$RECEIVER eth0" \
  "$ROUTER2 lan0" \
  "$ROUTER2 wan0" \
  "$WAN r2"; do
  read -r namespace interface <<<"$spec"
  ip -n "$namespace" link set "$interface" up
done

ip -n "$SENDER" route add default via 10.71.1.1
ip -n "$RECEIVER" route add default via 10.72.2.1
ip -n "$ROUTER1" route add default via 198.18.71.1
ip -n "$ROUTER2" route add default via 198.18.72.1
for namespace in "$WAN" "$ROUTER1" "$ROUTER2"; do
  ip netns exec "$namespace" sysctl -q -w net.ipv4.ip_forward=1
done

set +e
ip -n "$SENDER" -6 addr add fd71:1::2/64 dev eth0 &&
  ip -n "$ROUTER1" -6 addr add fd71:1::1/64 dev lan0 &&
  ip -n "$ROUTER1" -6 addr add fd71:100::2/64 dev wan0 &&
  ip -n "$WAN" -6 addr add fd71:100::1/64 dev r1 &&
  ip -n "$RECEIVER" -6 addr add fd72:2::2/64 dev eth0 &&
  ip -n "$ROUTER2" -6 addr add fd72:2::1/64 dev lan0 &&
  ip -n "$ROUTER2" -6 addr add fd72:100::2/64 dev wan0 &&
  ip -n "$WAN" -6 addr add fd72:100::1/64 dev r2 &&
  ip -n "$SENDER" -6 route add default via fd71:1::1 &&
  ip -n "$RECEIVER" -6 route add default via fd72:2::1 &&
  ip -n "$ROUTER1" -6 route add default via fd71:100::1 &&
  ip -n "$ROUTER2" -6 route add default via fd72:100::1 &&
  ip netns exec "$WAN" sysctl -q -w net.ipv6.conf.all.forwarding=1 &&
  ip netns exec "$ROUTER1" sysctl -q -w net.ipv6.conf.all.forwarding=1 &&
  ip netns exec "$ROUTER2" sysctl -q -w net.ipv6.conf.all.forwarding=1
if [[ $? -eq 0 ]]; then
  IPV6_AVAILABLE=1
fi
set -e

SIGNAL_URL=http://198.18.71.1:18080
RELAY_ADDR=198.18.71.1:19090
TOKEN_SECRET="nat-lab-$SUFFIX"

ip netns exec "$WAN" "$BIN" relay \
  --listen 0.0.0.0:19090 \
  --room-ttl 2m \
  --token-secret "$TOKEN_SECRET" \
  --no-lan-announce \
  >"$WORK/relay.log" 2>"$WORK/relay.err" &
RELAY_PID=$!
ip netns exec "$WAN" "$BIN" serve \
  --listen 0.0.0.0:18080 \
  --public-url "$SIGNAL_URL" \
  --native-relay "$RELAY_ADDR" \
  --native-relay-secret "$TOKEN_SECRET" \
  --native-relay-credential-ttl 5m \
  >"$WORK/service.log" 2>"$WORK/service.err" &
SERVICE_PID=$!

for _ in $(seq 1 100); do
  if ip netns exec "$WAN" python3 -c \
    'import urllib.request; urllib.request.urlopen("http://127.0.0.1:18080/api/health", timeout=1).read()' \
    >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
if ! ip netns exec "$WAN" python3 -c \
  'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:18080/api/health", timeout=2).read().decode())' \
  >"$ARTIFACT_DIR/service-health.json"; then
  echo "NAT lab service did not become ready" >&2
  sed -n '1,160p' "$WORK/service.err" >&2 || true
  exit 1
fi

reset_network() {
  for namespace in "$WAN" "$ROUTER1" "$ROUTER2"; do
    ip netns exec "$namespace" iptables -F
    ip netns exec "$namespace" iptables -t nat -F
    ip netns exec "$namespace" iptables -P FORWARD ACCEPT
  done
  ip -n "$WAN" route del 10.71.1.0/24 2>/dev/null || true
  ip -n "$WAN" route del 10.72.2.0/24 2>/dev/null || true
}

configure_routed() {
  reset_network
  ip -n "$WAN" route add 10.71.1.0/24 via 198.18.71.2 dev r1
  ip -n "$WAN" route add 10.72.2.0/24 via 198.18.72.2 dev r2
}

configure_nat() {
  local inbound=${1:-no}
  reset_network
  ip netns exec "$ROUTER1" iptables -t nat -A POSTROUTING \
    -s 10.71.1.0/24 -o wan0 -j SNAT --to-source 198.18.71.2
  ip netns exec "$ROUTER2" iptables -t nat -A POSTROUTING \
    -s 10.72.2.0/24 -o wan0 -j SNAT --to-source 198.18.72.2
  if [[ "$inbound" == "yes" ]]; then
    ip netns exec "$ROUTER1" iptables -t nat -A PREROUTING \
      -i wan0 -p tcp --dport 41001 -j DNAT --to-destination 10.71.1.2:41001
    ip netns exec "$ROUTER2" iptables -t nat -A PREROUTING \
      -i wan0 -p tcp --dport 41002 -j DNAT --to-destination 10.72.2.2:41002
  fi
}

sanitize_json() {
  local input=$1
  local output=$2
  if ! "$REPORT_TOOL" redact-json --input "$input" --output "$output" 2>/dev/null; then
    printf '{"ok":false,"error":"diagnostic output unavailable"}\n' >"$output"
  fi
}

run_diagnostics() {
  local namespace=$1
  local listen=$2
  local advertise=$3
  local directory=$4
  local role=$5
  local raw_doctor="$WORK/raw/${directory##*/}-$role-doctor.json"
  local raw_route="$WORK/raw/${directory##*/}-$role-route.json"
  local args=(
    --signal "$SIGNAL_URL"
    --relay "$RELAY_ADDR"
    --no-lan
    --no-route-history
    --direct-listen "$listen"
  )
  if [[ -n "$advertise" ]]; then
    args+=(--direct-advertise "$advertise")
  fi
  set +e
  ip netns exec "$namespace" "$BIN" "${args[@]}" doctor --json --timeout 3s >"$raw_doctor" 2>/dev/null
  ip netns exec "$namespace" "$BIN" "${args[@]}" route --json --pair native-native --timeout 3s >"$raw_route" 2>/dev/null
  set -e
  sanitize_json "$raw_doctor" "$directory/$role-doctor.json"
  sanitize_json "$raw_route" "$directory/$role-route.json"
}

wait_for_code() {
  local log=$1
  local code=""
  for _ in $(seq 1 120); do
    code=$(sed -n 's/^Code: //p' "$log" | head -1)
    if [[ -n "$code" ]]; then
      printf '%s\n' "$code"
      return 0
    fi
    sleep 0.1
  done
  return 1
}

now_millis() {
  python3 -c 'import time; print(time.time_ns() // 1000000)'
}

run_scenario() {
  local name=$1
  local network_model=$2
  local expected_route=$3
  local mode=$4
  local sender_listen=$5
  local receiver_listen=$6
  local sender_advertise=$7
  local receiver_advertise=$8
  local directory="$ARTIFACT_DIR/$name"
  local raw_prefix="$WORK/raw/$name"
  local source="$WORK/$name-payload.bin"
  local output_dir="$WORK/$name-output"
  local output="$output_dir/$(basename "$source")"
  local code=""
  local sender_pid=""
  local sender_status=125
  local receiver_status=125
  local started
  local finished
  local input_sha=""
  local output_sha=""
  local scenario_status=0
  mkdir -p "$directory" "$output_dir"

  case "$mode" in
    routed)
      configure_routed
      ;;
    nat-mapped)
      configure_nat yes
      ;;
    nat-blocked)
      configure_nat no
      ;;
    ipv6)
      configure_routed
      ip -n "$WAN" -6 route replace fd71:1::/64 via fd71:100::2 dev r1
      ip -n "$WAN" -6 route replace fd72:2::/64 via fd72:100::2 dev r2
      ;;
    *)
      echo "unknown NAT lab mode: $mode" >&2
      return 2
      ;;
  esac

  python3 - "$source" "$PAYLOAD_BYTES" <<'PY'
import os
import sys

path, size_text = sys.argv[1:]
remaining = int(size_text)
with open(path, "wb") as output:
    while remaining:
        chunk = os.urandom(min(65536, remaining))
        output.write(chunk)
        remaining -= len(chunk)
PY
  input_sha=$(sha256sum "$source" | awk '{print $1}')

  run_diagnostics "$SENDER" "$sender_listen" "$sender_advertise" "$directory" sender
  run_diagnostics "$RECEIVER" "$receiver_listen" "$receiver_advertise" "$directory" receiver

  local sender_args=(
    --signal "$SIGNAL_URL"
    --web-url "$SIGNAL_URL"
    --no-lan
    --no-route-history
    --direct-timeout 1200ms
    --direct-listen "$sender_listen"
  )
  local receiver_args=(
    --signal "$SIGNAL_URL"
    --web-url "$SIGNAL_URL"
    --no-lan
    --no-route-history
    --direct-timeout 1200ms
    --direct-listen "$receiver_listen"
  )
  if [[ -n "$sender_advertise" ]]; then
    sender_args+=(--direct-advertise "$sender_advertise")
  fi
  if [[ -n "$receiver_advertise" ]]; then
    receiver_args+=(--direct-advertise "$receiver_advertise")
  fi
  if [[ "$mode" == "routed" || "$mode" == "ipv6" ]]; then
    sender_args+=(--transport native)
    receiver_args+=(--transport native)
  fi

  started=$(now_millis)
  ip netns exec "$SENDER" timeout "${TRANSFER_TIMEOUT}s" \
    "$BIN" "${sender_args[@]}" send "$source" \
    >"$raw_prefix-sender.log" 2>"$raw_prefix-sender.err" &
  sender_pid=$!
  if code=$(wait_for_code "$raw_prefix-sender.log"); then
    set +e
    ip netns exec "$RECEIVER" timeout "${TRANSFER_TIMEOUT}s" \
      "$BIN" "${receiver_args[@]}" recv "$code" --out "$output_dir" \
      >"$raw_prefix-receiver.log" 2>"$raw_prefix-receiver.err"
    receiver_status=$?
    if [[ $receiver_status -ne 0 ]]; then
      stop_group "$sender_pid"
    fi
    wait "$sender_pid"
    sender_status=$?
    set -e
  else
    echo "$name: sender did not emit a pairing code" >&2
    : >"$raw_prefix-receiver.log"
    : >"$raw_prefix-receiver.err"
    stop_group "$sender_pid"
    sender_status=124
  fi
  finished=$(now_millis)
  if [[ -f "$output" ]]; then
    output_sha=$(sha256sum "$output" | awk '{print $1}')
  fi

  "$REPORT_TOOL" redact-log \
    --input "$raw_prefix-sender.log" \
    --output "$directory/sender.log" \
    --code "$code"
  "$REPORT_TOOL" redact-log \
    --input "$raw_prefix-sender.err" \
    --output "$directory/sender.err" \
    --code "$code"
  "$REPORT_TOOL" redact-log \
    --input "$raw_prefix-receiver.log" \
    --output "$directory/receiver.log" \
    --code "$code"
  "$REPORT_TOOL" redact-log \
    --input "$raw_prefix-receiver.err" \
    --output "$directory/receiver.err" \
    --code "$code"

  set +e
  "$REPORT_TOOL" scenario \
    --name "$name" \
    --network-model "$network_model" \
    --expected-route "$expected_route" \
    --sender-exit "$sender_status" \
    --receiver-exit "$receiver_status" \
    --input-sha256 "$input_sha" \
    --output-sha256 "$output_sha" \
    --duration-ms $((finished - started)) \
    --sender-log "$directory/sender.log" \
    --sender-error "$directory/sender.err" \
    --receiver-log "$directory/receiver.log" \
    --receiver-error "$directory/receiver.err" \
    --sender-doctor "$directory/sender-doctor.json" \
    --receiver-doctor "$directory/receiver-doctor.json" \
    --sender-route "$directory/sender-route.json" \
    --receiver-route "$directory/receiver-route.json" \
    --note "Synthetic Linux namespace model; see matrix metadata for limitations." \
    --output "$directory/scenario.json"
  scenario_status=$?
  set -e
  SCENARIO_REPORTS+=("$directory/scenario.json")
  if [[ $scenario_status -eq 0 ]]; then
    echo "ok $name"
  else
    echo "FAIL $name" >&2
    sed -n '1,200p' "$directory/sender.log" >&2 || true
    sed -n '1,200p' "$directory/sender.err" >&2 || true
    sed -n '1,200p' "$directory/receiver.log" >&2 || true
    sed -n '1,200p' "$directory/receiver.err" >&2 || true
  fi
}

run_scenario \
  routed-open \
  routed-ipv4 \
  direct \
  routed \
  10.71.1.2:41001 \
  10.72.2.2:41002 \
  10.71.1.2:41001 \
  10.72.2.2:41002

run_scenario \
  port-preserving-nat \
  static-port-preserving-snat-dnat \
  direct \
  nat-mapped \
  0.0.0.0:41001 \
  0.0.0.0:41002 \
  "" \
  ""

run_scenario \
  relay-fallback \
  outbound-only-snat \
  relay \
  nat-blocked \
  0.0.0.0:41001 \
  0.0.0.0:41002 \
  "" \
  ""

if [[ $IPV6_AVAILABLE -eq 1 ]]; then
  run_scenario \
    ipv6-routed \
    routed-ipv6-ula \
    direct \
    ipv6 \
    "[fd71:1::2]:41001" \
    "[fd72:2::2]:41002" \
    "[fd71:1::2]:41001" \
    "[fd72:2::2]:41002"
else
  mkdir -p "$ARTIFACT_DIR/ipv6-routed"
  "$REPORT_TOOL" scenario \
    --name ipv6-routed \
    --network-model routed-ipv6-ula \
    --expected-route direct \
    --skipped \
    --reason "IPv6 namespace addressing or forwarding is unavailable on this runner." \
    --output "$ARTIFACT_DIR/ipv6-routed/scenario.json"
  SCENARIO_REPORTS+=("$ARTIFACT_DIR/ipv6-routed/scenario.json")
  echo "skip ipv6-routed"
fi

METADATA=$(cat <<'JSON'
{
  "topology": "sender -> router1 -> WAN services <- router2 <- receiver",
  "address_space": "RFC 2544 IPv4 benchmark ranges plus IPv6 ULA",
  "limitations": [
    "Synthetic Linux conntrack behavior is deterministic but does not reproduce every consumer NAT.",
    "The port-preserving scenario uses fixed DNAT matching the direct listener ports.",
    "The relay fallback scenario approximates outbound-only NAT, not carrier-grade NAT state sharing.",
    "Use scripts/public_matrix.sh for observations from real external networks."
  ]
}
JSON
)

set +e
"$REPORT_TOOL" combine \
  --kind nat-lab \
  --metadata "$METADATA" \
  --output "$ARTIFACT_DIR/matrix.json" \
  "${SCENARIO_REPORTS[@]}"
status=$?
set -e
echo "NAT lab artifacts: $ARTIFACT_DIR/matrix.json"
exit "$status"
