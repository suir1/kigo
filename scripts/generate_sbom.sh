#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${KIGO_VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}
OUT=${KIGO_DIST:-"$ROOT/dist"}
TOOL_VERSION=${CYCLONEDX_GOMOD_VERSION:-v1.9.0}

mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
OUTPUT=${KIGO_SBOM_OUTPUT:-"$OUT/kigo-${VERSION}.cdx.json"}

if [[ ! "$VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]; then
  echo "KIGO_VERSION contains characters that are unsafe for SBOM filenames: $VERSION" >&2
  exit 2
fi

if [[ -n "${CYCLONEDX_GOMOD:-}" ]]; then
  command=("$CYCLONEDX_GOMOD")
else
  command=(
    "go"
    "run"
    "github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@${TOOL_VERSION}"
  )
fi

echo "generating CycloneDX ${TOOL_VERSION} SBOM"
(
  cd "$ROOT"
  "${command[@]}" mod \
    -json \
    -output-version 1.6 \
    -type application \
    -output "$OUTPUT" \
    .
)
echo "SBOM written to $OUTPUT"
