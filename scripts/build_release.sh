#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=${KIGO_VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${KIGO_COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
DATE=${KIGO_DATE:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}
OUT=${KIGO_DIST:-"$ROOT/dist"}
DEFAULT_SERVICE_URL=${KIGO_DEFAULT_SERVICE_URL:-}

mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

if [[ ! "$VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]; then
  echo "KIGO_VERSION contains characters that are unsafe for release filenames: $VERSION" >&2
  exit 2
fi

if [[ -n "$DEFAULT_SERVICE_URL" &&
  ( ! "$DEFAULT_SERVICE_URL" =~ ^https?://[^/@[:space:]?#]+(/[^[:space:]?#]*)?$ || "$DEFAULT_SERVICE_URL" == */ ) ]]; then
  echo "KIGO_DEFAULT_SERVICE_URL must be an http(s) URL without credentials, query, fragment, whitespace, or trailing slash" >&2
  exit 2
fi

rm -rf "$OUT"/kigo-* "$OUT"/SHA256SUMS

targets=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
  "windows amd64"
)

ldflags="-s -w -X github.com/suir1/kigo/internal/version.Version=$VERSION -X github.com/suir1/kigo/internal/version.Commit=$COMMIT -X github.com/suir1/kigo/internal/version.Date=$DATE"
if [[ -n "$DEFAULT_SERVICE_URL" ]]; then
  ldflags+=" -X github.com/suir1/kigo/internal/app.defaultSignalURL=$DEFAULT_SERVICE_URL"
  ldflags+=" -X github.com/suir1/kigo/internal/app.defaultWebURL=$DEFAULT_SERVICE_URL"
fi

for target in "${targets[@]}"; do
  read -r goos goarch <<<"$target"
  name="kigo-${VERSION}-${goos}-${goarch}"
  bin="$OUT/$name/kigo"
  archive="$OUT/$name.tar.gz"
  if [[ "$goos" == "windows" ]]; then
    bin="$OUT/$name/kigo.exe"
    archive="$OUT/$name.zip"
  fi
  mkdir -p "$OUT/$name"
  echo "building $name"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="$ldflags" -o "$bin" ./cmd/kigo)
  cp "$ROOT/README.md" "$OUT/$name/README.md"
  cp -R "$ROOT/deploy" "$OUT/$name/deploy"
  if [[ "$goos" == "windows" ]]; then
    (cd "$OUT" && zip -qr "$(basename "$archive")" "$name")
  else
    (cd "$OUT" && tar -czf "$(basename "$archive")" "$name")
  fi
  rm -rf "$OUT/$name"
done

KIGO_VERSION="$VERSION" \
  KIGO_DIST="$OUT" \
  KIGO_SBOM_OUTPUT="$OUT/kigo-${VERSION}.cdx.json" \
  "$ROOT/scripts/generate_sbom.sh"

shopt -s nullglob
artifacts=(
  "$OUT"/kigo-*.tar.gz
  "$OUT"/kigo-*.zip
  "$OUT"/kigo-*.cdx.json
)
if [[ ${#artifacts[@]} -ne 6 ]]; then
  echo "expected five archives and one SBOM, found ${#artifacts[@]} artifacts" >&2
  exit 1
fi

artifact_names=()
for artifact in "${artifacts[@]}"; do
  artifact_names+=("$(basename "$artifact")")
done
IFS=$'\n' artifact_names=($(printf '%s\n' "${artifact_names[@]}" | LC_ALL=C sort))
unset IFS

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUT" && sha256sum "${artifact_names[@]}" > SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$OUT" && shasum -a 256 "${artifact_names[@]}" > SHA256SUMS)
else
  echo "sha256sum or shasum is required to create release checksums" >&2
  exit 2
fi

echo "release artifacts written to $OUT"
