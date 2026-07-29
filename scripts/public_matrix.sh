#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
REPORT_TOOL="$ROOT/scripts/matrix_report.py"
DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  shift
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--dry-run]" >&2
  exit 2
fi

SENDER_HOST=${KIGO_SENDER_HOST:-}
RECEIVER_HOST=${KIGO_RECEIVER_HOST:-}
SENDER_LABEL=${KIGO_SENDER_LABEL:-sender}
RECEIVER_LABEL=${KIGO_RECEIVER_LABEL:-receiver}
SIGNAL_URL=${KIGO_SIGNAL_URL:-}
RELAY=${KIGO_RELAY_ENDPOINT:-}
RELAY_PASS=${KIGO_RELAY_PASS:-}
REMOTE_BIN=${KIGO_REMOTE_BIN:-kigo}
PAYLOAD_BYTES=${KIGO_PUBLIC_PAYLOAD_BYTES:-1048576}
TIMEOUT_SECONDS=${KIGO_PUBLIC_TIMEOUT_SECONDS:-90}
EXPECTED_ROUTE=${KIGO_PUBLIC_EXPECT_ROUTE:-any}
TRANSPORT=${KIGO_PUBLIC_TRANSPORT:-auto}
PAIRING_CODE=${KIGO_PUBLIC_CODE:-}
DIRECT_TIMEOUT=${KIGO_PUBLIC_DIRECT_TIMEOUT:-2s}
UDP_PROBE=${KIGO_PUBLIC_UDP_PROBE:-0}
ARTIFACT_DIR=${KIGO_ARTIFACT_DIR:-"$ROOT/artifacts/public-matrix"}
SSH_CONNECT_TIMEOUT=${KIGO_SSH_CONNECT_TIMEOUT:-15}

VALIDATION=$(mktemp)
WORK=""
SENDER_DIR=""
RECEIVER_DIR=""
SENDER_PID=""
RECEIVER_PID=""

cleanup_remote_process() {
  local host=$1
  local directory=$2
  local name=$3
  if [[ -z "$host" || -z "$directory" ]]; then
    return
  fi
  local pid_file="$directory/$name.pid"
  local command
  printf -v command \
    'if [[ -f %q ]]; then pid=$(cat %q 2>/dev/null || true); [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; fi' \
    "$pid_file" "$pid_file"
  remote_exec "$host" "$command" >/dev/null 2>&1 || true
}

cleanup() {
  rm -f "$VALIDATION"
  if [[ -n "$WORK" ]]; then
    cleanup_remote_process "$SENDER_HOST" "$SENDER_DIR" sender
    cleanup_remote_process "$RECEIVER_HOST" "$RECEIVER_DIR" receiver
    if [[ -n "$SENDER_DIR" ]]; then
      remote_exec "$SENDER_HOST" "rm -rf $(shell_quote "$SENDER_DIR")" >/dev/null 2>&1 || true
    fi
    if [[ -n "$RECEIVER_DIR" ]]; then
      remote_exec "$RECEIVER_HOST" "rm -rf $(shell_quote "$RECEIVER_DIR")" >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

set +e
"$REPORT_TOOL" validate-public \
  --sender-host "$SENDER_HOST" \
  --receiver-host "$RECEIVER_HOST" \
  --sender-label "$SENDER_LABEL" \
  --receiver-label "$RECEIVER_LABEL" \
  --signal-url "$SIGNAL_URL" \
  --relay "$RELAY" \
  --relay-pass "$RELAY_PASS" \
  --remote-bin "$REMOTE_BIN" \
  --payload-bytes "$PAYLOAD_BYTES" \
  --timeout-seconds "$TIMEOUT_SECONDS" \
  --expected-route "$EXPECTED_ROUTE" \
  --output "$VALIDATION"
validation_status=$?
set -e
if [[ $DRY_RUN -eq 1 ]]; then
  cat "$VALIDATION"
  exit "$validation_status"
fi
if [[ $validation_status -ne 0 ]]; then
  cat "$VALIDATION" >&2
  exit "$validation_status"
fi
if [[ "$TRANSPORT" != "auto" && "$TRANSPORT" != "native" && "$TRANSPORT" != "webrtc" ]]; then
  echo "KIGO_PUBLIC_TRANSPORT must be auto, native, or webrtc" >&2
  exit 2
fi
if [[ -n "$PAIRING_CODE" && ! "$PAIRING_CODE" =~ ^[A-Za-z0-9-]{6,64}$ ]]; then
  echo "KIGO_PUBLIC_CODE must be a 6-64 character pairing code" >&2
  exit 2
fi
if [[ "$UDP_PROBE" != "0" && "$UDP_PROBE" != "1" ]]; then
  echo "KIGO_PUBLIC_UDP_PROBE must be 0 or 1" >&2
  exit 2
fi
if [[ ! "$SSH_CONNECT_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
  echo "KIGO_SSH_CONNECT_TIMEOUT must be a positive integer" >&2
  exit 2
fi
for command in ssh python3; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required for the public matrix" >&2
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

SSH=(
  ssh
  -o BatchMode=yes
  -o "ConnectTimeout=$SSH_CONNECT_TIMEOUT"
  -o ServerAliveInterval=10
  -o ServerAliveCountMax=3
)

shell_quote() {
  printf '%q' "$1"
}

command_string() {
  local output=""
  printf -v output '%q ' "$@"
  printf '%s' "$output"
}

remote_exec() {
  local host=$1
  local command=$2
  local quoted
  printf -v quoted '%q' "$command"
  "${SSH[@]}" "$host" "bash -lc $quoted"
}

remote_command_with_pid() {
  local pid_file=$1
  shift
  local command
  local quoted_pid
  command=$(command_string "$@")
  printf -v quoted_pid '%q' "$pid_file"
  printf 'umask 077; printf "%%s\\n" "$$" > %s; exec %s' "$quoted_pid" "$command"
}

wait_with_timeout() {
  local pid=$1
  local timeout=$2
  local deadline=$((SECONDS + timeout))
  while kill -0 "$pid" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      kill "$pid" 2>/dev/null || true
      sleep 0.2
      kill -KILL "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 0.2
  done
  wait "$pid"
}

sanitize_json() {
  local input=$1
  local output=$2
  if ! "$REPORT_TOOL" redact-json --input "$input" --output "$output" 2>/dev/null; then
    printf '{"ok":false,"error":"diagnostic output unavailable"}\n' >"$output"
  fi
}

remote_checksum() {
  local host=$1
  local path=$2
  local quoted
  printf -v quoted '%q' "$path"
  remote_exec "$host" \
    "if command -v sha256sum >/dev/null 2>&1; then sha256sum $quoted | awk '{print \$1}'; else shasum -a 256 $quoted | awk '{print \$1}'; fi"
}

run_remote_json() {
  local host=$1
  local output=$2
  local error_output=$3
  shift 3
  local command
  local status=0
  command=$(command_string "$@")
  remote_exec "$host" "$command" >"$output" 2>"$error_output" || status=$?
  return "$status"
}

WORK=$(mktemp -d)
RAW="$WORK/raw"
mkdir -p "$RAW"
rm -rf "$ARTIFACT_DIR"
mkdir -p "$ARTIFACT_DIR"
cp "$VALIDATION" "$ARTIFACT_DIR/config.json"

set +e
SENDER_DIR=$(remote_exec "$SENDER_HOST" "mktemp -d /tmp/kigo-public-sender.XXXXXX")
sender_temp_status=$?
RECEIVER_DIR=$(remote_exec "$RECEIVER_HOST" "mktemp -d /tmp/kigo-public-receiver.XXXXXX")
receiver_temp_status=$?
set -e
if [[ $sender_temp_status -ne 0 || $receiver_temp_status -ne 0 || -z "$SENDER_DIR" || -z "$RECEIVER_DIR" ]]; then
  echo "could not create remote working directories" >&2
  exit 1
fi

set +e
run_remote_json "$SENDER_HOST" "$RAW/sender-version.json" "$RAW/sender-version.err" \
  "$REMOTE_BIN" version --json
run_remote_json "$RECEIVER_HOST" "$RAW/receiver-version.json" "$RAW/receiver-version.err" \
  "$REMOTE_BIN" version --json
set -e
sanitize_json "$RAW/sender-version.json" "$ARTIFACT_DIR/sender-version.json"
sanitize_json "$RAW/receiver-version.json" "$ARTIFACT_DIR/receiver-version.json"

common_args=(
  --signal "$SIGNAL_URL"
  --web-url "$SIGNAL_URL"
  --transport "$TRANSPORT"
  --no-lan
  --direct-timeout "$DIRECT_TIMEOUT"
)
if [[ -n "$RELAY" ]]; then
  common_args+=(--relay "$RELAY")
fi
if [[ -n "$RELAY_PASS" ]]; then
  common_args+=(--relay-pass "$RELAY_PASS")
fi
if [[ "$UDP_PROBE" == "1" ]]; then
  common_args+=(--udp-probe)
fi

sender_diag_args=(
  "${common_args[@]}"
  --route-history "$SENDER_DIR/route-history.json"
)
receiver_diag_args=(
  "${common_args[@]}"
  --route-history "$RECEIVER_DIR/route-history.json"
)

set +e
run_remote_json "$SENDER_HOST" "$RAW/sender-doctor.json" "$RAW/sender-doctor.err" \
  "$REMOTE_BIN" "${sender_diag_args[@]}" doctor --json --timeout 5s
run_remote_json "$SENDER_HOST" "$RAW/sender-route.json" "$RAW/sender-route.err" \
  "$REMOTE_BIN" "${sender_diag_args[@]}" route --json --pair native-native --timeout 5s
run_remote_json "$RECEIVER_HOST" "$RAW/receiver-doctor.json" "$RAW/receiver-doctor.err" \
  "$REMOTE_BIN" "${receiver_diag_args[@]}" doctor --json --timeout 5s
run_remote_json "$RECEIVER_HOST" "$RAW/receiver-route.json" "$RAW/receiver-route.err" \
  "$REMOTE_BIN" "${receiver_diag_args[@]}" route --json --pair native-native --timeout 5s
set -e
for role in sender receiver; do
  sanitize_json "$RAW/$role-doctor.json" "$ARTIFACT_DIR/$role-doctor.json"
  sanitize_json "$RAW/$role-route.json" "$ARTIFACT_DIR/$role-route.json"
  "$REPORT_TOOL" redact-log \
    --input "$RAW/$role-doctor.err" \
    --output "$ARTIFACT_DIR/$role-doctor.err"
  "$REPORT_TOOL" redact-log \
    --input "$RAW/$role-route.err" \
    --output "$ARTIFACT_DIR/$role-route.err"
done

SOURCE="$SENDER_DIR/payload.bin"
OUTPUT_DIR="$RECEIVER_DIR/output"
OUTPUT="$OUTPUT_DIR/payload.bin"
prepare_command=$(
  command_string bash -c \
    'umask 077; mkdir -p "$1"; head -c "$2" /dev/urandom > "$3"' \
    _ "$SENDER_DIR" "$PAYLOAD_BYTES" "$SOURCE"
)
remote_exec "$SENDER_HOST" "$prepare_command"
remote_exec "$RECEIVER_HOST" "$(command_string mkdir -p "$OUTPUT_DIR")"
INPUT_SHA=$(remote_checksum "$SENDER_HOST" "$SOURCE")

sender_args=(
  "$REMOTE_BIN"
  "${common_args[@]}"
  --route-history "$SENDER_DIR/route-history.json"
  send "$SOURCE"
)
if [[ -n "$PAIRING_CODE" ]]; then
  sender_args+=(--code "$PAIRING_CODE")
fi
receiver_base_args=(
  "$REMOTE_BIN"
  "${common_args[@]}"
  --route-history "$RECEIVER_DIR/route-history.json"
)

sender_command=$(remote_command_with_pid "$SENDER_DIR/sender.pid" "${sender_args[@]}")
started=$(python3 -c 'import time; print(time.time_ns() // 1000000)')
remote_exec "$SENDER_HOST" "$sender_command" >"$RAW/sender.log" 2>"$RAW/sender.err" &
SENDER_PID=$!

CODE=""
for _ in $(seq 1 150); do
  CODE=$(sed -n 's/^Code: //p' "$RAW/sender.log" | head -1)
  if [[ -n "$CODE" ]]; then
    break
  fi
  if ! kill -0 "$SENDER_PID" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
if [[ -z "$CODE" ]]; then
  echo "public sender did not emit a pairing code" >&2
  cleanup_remote_process "$SENDER_HOST" "$SENDER_DIR" sender
  set +e
  wait "$SENDER_PID"
  set -e
  exit 1
fi

receiver_args=(
  "${receiver_base_args[@]}"
  recv "$CODE" --out "$OUTPUT_DIR"
)
receiver_command=$(remote_command_with_pid "$RECEIVER_DIR/receiver.pid" "${receiver_args[@]}")
remote_exec "$RECEIVER_HOST" "$receiver_command" >"$RAW/receiver.log" 2>"$RAW/receiver.err" &
RECEIVER_PID=$!

set +e
wait_with_timeout "$RECEIVER_PID" "$TIMEOUT_SECONDS"
receiver_status=$?
if [[ $receiver_status -ne 0 ]]; then
  cleanup_remote_process "$SENDER_HOST" "$SENDER_DIR" sender
fi
wait_with_timeout "$SENDER_PID" "$TIMEOUT_SECONDS"
sender_status=$?
set -e
finished=$(python3 -c 'import time; print(time.time_ns() // 1000000)')

OUTPUT_SHA=""
if [[ $receiver_status -eq 0 ]]; then
  set +e
  OUTPUT_SHA=$(remote_checksum "$RECEIVER_HOST" "$OUTPUT")
  checksum_status=$?
  set -e
  if [[ $checksum_status -ne 0 ]]; then
    OUTPUT_SHA=""
  fi
fi

for role in sender receiver; do
  "$REPORT_TOOL" redact-log \
    --input "$RAW/$role.log" \
    --output "$ARTIFACT_DIR/$role.log" \
    --code "$CODE"
  "$REPORT_TOOL" redact-log \
    --input "$RAW/$role.err" \
    --output "$ARTIFACT_DIR/$role.err" \
    --code "$CODE"
done

set +e
"$REPORT_TOOL" scenario \
  --name public-native \
  --network-model externally-provisioned-ssh-endpoints \
  --expected-route "$EXPECTED_ROUTE" \
  --sender-exit "$sender_status" \
  --receiver-exit "$receiver_status" \
  --input-sha256 "$INPUT_SHA" \
  --output-sha256 "$OUTPUT_SHA" \
  --duration-ms $((finished - started)) \
  --sender-log "$ARTIFACT_DIR/sender.log" \
  --sender-error "$ARTIFACT_DIR/sender.err" \
  --receiver-log "$ARTIFACT_DIR/receiver.log" \
  --receiver-error "$ARTIFACT_DIR/receiver.err" \
  --sender-doctor "$ARTIFACT_DIR/sender-doctor.json" \
  --receiver-doctor "$ARTIFACT_DIR/receiver-doctor.json" \
  --sender-route "$ARTIFACT_DIR/sender-route.json" \
  --receiver-route "$ARTIFACT_DIR/receiver-route.json" \
  --note "Endpoint hostnames and pairing material are intentionally omitted from artifacts." \
  --output "$ARTIFACT_DIR/scenario.json"
scenario_status=$?

METADATA=$(python3 - "$SENDER_LABEL" "$RECEIVER_LABEL" "$SIGNAL_URL" "$RELAY" "$TRANSPORT" <<'PY'
import json
import sys

sender, receiver, signal, relay, transport = sys.argv[1:]
print(json.dumps({
    "sender": sender,
    "receiver": receiver,
    "signal_url": signal,
    "relay": relay,
    "transport_policy": transport,
    "limitations": [
        "NAT and firewall labels come from endpoint diagnostics and operator context.",
        "A successful route is an observation for these endpoints at this time, not a universal reachability guarantee.",
    ],
}))
PY
)
"$REPORT_TOOL" combine \
  --kind public-endpoint-matrix \
  --metadata "$METADATA" \
  --output "$ARTIFACT_DIR/matrix.json" \
  "$ARTIFACT_DIR/scenario.json"
combine_status=$?
set -e

echo "Public matrix artifacts: $ARTIFACT_DIR/matrix.json"
if [[ $scenario_status -ne 0 ]]; then
  exit "$scenario_status"
fi
exit "$combine_status"
