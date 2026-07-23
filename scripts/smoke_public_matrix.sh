#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
BIN=${KIGO_BIN:-"$WORK/kigo"}
SERVER_PID=""

cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

find_free_addr() {
  local port
  for port in $(seq 18300 18349); do
    if ! nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      printf '127.0.0.1:%s\n' "$port"
      return 0
    fi
  done
  return 1
}

if [[ -z "${KIGO_BIN:-}" ]]; then
  (cd "$ROOT" && go build -trimpath -o "$BIN" ./cmd/kigo)
fi
if ! LISTEN=$(find_free_addr); then
  echo "No free public-matrix smoke port found in 18300..18349." >&2
  exit 2
fi
SIGNAL_URL="http://$LISTEN"

cat >"$WORK/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while [[ "${1:-}" == "-o" ]]; do
  shift 2
done
if [[ $# -lt 2 ]]; then
  echo "fake ssh expected host and command" >&2
  exit 2
fi
shift
exec bash -c "$1"
SH
chmod +x "$WORK/ssh"

"$BIN" serve --listen "$LISTEN" >"$WORK/service.log" 2>"$WORK/service.err" &
SERVER_PID=$!
for _ in $(seq 1 80); do
  if curl -fsS "$SIGNAL_URL/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$SIGNAL_URL/api/health" >/dev/null

PATH="$WORK:$PATH" \
  KIGO_SENDER_HOST=matrix-sender \
  KIGO_RECEIVER_HOST=matrix-receiver \
  KIGO_SENDER_LABEL=local-smoke-sender \
  KIGO_RECEIVER_LABEL=local-smoke-receiver \
  KIGO_SIGNAL_URL="$SIGNAL_URL" \
  KIGO_REMOTE_BIN="$BIN" \
  KIGO_PUBLIC_PAYLOAD_BYTES=131072 \
  KIGO_PUBLIC_TIMEOUT_SECONDS=30 \
  KIGO_PUBLIC_EXPECT_ROUTE=direct \
  KIGO_ARTIFACT_DIR="$WORK/artifacts" \
  "$ROOT/scripts/public_matrix.sh"

python3 - "$WORK/artifacts/matrix.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    report = json.load(source)
assert report["status"] == "passed", report
assert report["counts"] == {"failed": 0, "passed": 1, "skipped": 0}, report
scenario = report["scenarios"][0]
assert scenario["selected_route"] == "direct", scenario
assert scenario["checksums"]["match"] is True, scenario
PY

python3 - "$WORK/artifacts" <<'PY'
import pathlib
import re
import sys

pattern = re.compile(r"\b[A-HJ-NP-Z2-9]{6}\b")
for path in pathlib.Path(sys.argv[1]).rglob("*"):
    if path.is_file() and pattern.search(path.read_text(encoding="utf-8", errors="replace")):
        raise SystemExit(f"public matrix artifact contains an unredacted pairing code: {path}")
PY
echo "all public matrix smoke checks passed"
