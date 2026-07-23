#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST=${1:-"$ROOT/dist"}
VERSION=${2:-${KIGO_VERSION:-}}

if [[ -z "$VERSION" ]]; then
  echo "usage: $0 [dist-directory] <version>" >&2
  echo "example: $0 dist v0.1.0" >&2
  exit 2
fi
if [[ ! -d "$DIST" ]]; then
  echo "release directory does not exist: $DIST" >&2
  exit 2
fi
DIST=$(cd "$DIST" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

expected=(
  "kigo-${VERSION}-darwin-amd64.tar.gz"
  "kigo-${VERSION}-darwin-arm64.tar.gz"
  "kigo-${VERSION}-linux-amd64.tar.gz"
  "kigo-${VERSION}-linux-arm64.tar.gz"
  "kigo-${VERSION}-windows-amd64.zip"
  "kigo-${VERSION}.cdx.json"
)

for artifact in "${expected[@]}"; do
  if [[ ! -f "$DIST/$artifact" ]]; then
    echo "missing release artifact: $artifact" >&2
    exit 1
  fi
done
if [[ ! -f "$DIST/SHA256SUMS" ]]; then
  echo "missing release artifact: SHA256SUMS" >&2
  exit 1
fi

checksum_count=$(wc -l <"$DIST/SHA256SUMS" | tr -d ' ')
if [[ "$checksum_count" -ne ${#expected[@]} ]]; then
  echo "SHA256SUMS contains $checksum_count entries; expected ${#expected[@]}" >&2
  exit 1
fi
for artifact in "${expected[@]}"; do
  matches=$(awk -v name="$artifact" '
    $2 == name || $2 == "*" name { count++ }
    END { print count + 0 }
  ' "$DIST/SHA256SUMS")
  if [[ "$matches" -ne 1 ]]; then
    echo "SHA256SUMS does not contain exactly one SHA-256 entry for $artifact" >&2
    exit 1
  fi
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$DIST" && sha256sum --check SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$DIST" && shasum -a 256 -c SHA256SUMS)
else
  echo "sha256sum or shasum is required to verify release checksums" >&2
  exit 2
fi

for target in "darwin-amd64" "darwin-arm64" "linux-amd64" "linux-arm64"; do
  archive="kigo-${VERSION}-${target}.tar.gz"
  root="kigo-${VERSION}-${target}"
  listing=$(tar -tzf "$DIST/$archive")
  grep -Fqx "$root/kigo" <<<"$listing"
  grep -Fqx "$root/README.md" <<<"$listing"
  grep -Fq "$root/deploy/" <<<"$listing"
done

windows_archive="kigo-${VERSION}-windows-amd64.zip"
windows_root="kigo-${VERSION}-windows-amd64"
windows_listing=$(unzip -Z1 "$DIST/$windows_archive")
grep -Fqx "$windows_root/kigo.exe" <<<"$windows_listing"
grep -Fqx "$windows_root/README.md" <<<"$windows_listing"
grep -Fq "$windows_root/deploy/" <<<"$windows_listing"

host_os=$(uname -s | tr '[:upper:]' '[:lower:]')
host_arch=$(uname -m)
case "$host_arch" in
  x86_64)
    host_arch=amd64
    ;;
  arm64 | aarch64)
    host_arch=arm64
    ;;
esac
if [[ "$host_os" == "darwin" || "$host_os" == "linux" ]] &&
   [[ "$host_arch" == "amd64" || "$host_arch" == "arm64" ]]; then
  host_root="kigo-${VERSION}-${host_os}-${host_arch}"
  tar -xzf "$DIST/${host_root}.tar.gz" -C "$WORK"
  version_json=$("$WORK/$host_root/kigo" version --json)
  if command -v jq >/dev/null 2>&1; then
    jq -e \
      --arg version "$VERSION" \
      --arg os "$host_os" \
      --arg arch "$host_arch" \
      '.version == $version and .os == $os and .arch == $arch' \
      <<<"$version_json" >/dev/null
  else
    grep -Fq "\"version\": \"$VERSION\"" <<<"$version_json"
    grep -Fq "\"os\": \"$host_os\"" <<<"$version_json"
    grep -Fq "\"arch\": \"$host_arch\"" <<<"$version_json"
  fi

  install_dir="$WORK/installed"
  KIGO_VERSION="$VERSION" \
    KIGO_RELEASE_BASE_URL="file://$DIST" \
    KIGO_INSTALL_DIR="$install_dir" \
    "$ROOT/scripts/install.sh" >/dev/null
  installed_json=$("$install_dir/kigo" version --json)
  grep -Fq "\"version\": \"$VERSION\"" <<<"$installed_json"
fi

sbom="$DIST/kigo-${VERSION}.cdx.json"
if command -v jq >/dev/null 2>&1; then
  jq -e '
    .bomFormat == "CycloneDX" and
    .specVersion == "1.6" and
    .metadata.component.name == "github.com/suir1/kigo" and
    (.components | type == "array" and length > 0)
  ' "$sbom" >/dev/null
else
  grep -Fq '"bomFormat": "CycloneDX"' "$sbom"
  grep -Fq '"specVersion": "1.6"' "$sbom"
  grep -Fq '"name": "github.com/suir1/kigo"' "$sbom"
fi

echo "release artifacts verified in $DIST"
