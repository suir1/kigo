#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

expect_line() {
  local output=$1 expected=$2
  grep -Fqx "$expected" <<<"$output" || {
    printf 'missing installer output: %s\n%s\n' "$expected" "$output" >&2
    exit 1
  }
}

darwin=$(
  KIGO_VERSION=v1.2.3 \
  KIGO_INSTALL_DRY_RUN=1 \
  KIGO_TEST_UNAME_S=Darwin \
  KIGO_TEST_UNAME_M=arm64 \
  "$ROOT/scripts/install.sh"
)
expect_line "$darwin" "platform=darwin-arm64"
expect_line "$darwin" "archive=kigo-v1.2.3-darwin-arm64.tar.gz"

linux=$(
  KIGO_VERSION=v1.2.3 \
  KIGO_INSTALL_DRY_RUN=1 \
  KIGO_TEST_UNAME_S=Linux \
  KIGO_TEST_UNAME_M=x86_64 \
  "$ROOT/scripts/install.sh"
)
expect_line "$linux" "platform=linux-amd64"
expect_line "$linux" "archive=kigo-v1.2.3-linux-amd64.tar.gz"

mock_bin="$WORK/mock-bin"
mkdir -p "$mock_bin"
printf '%s\n' \
  '#!/bin/sh' \
  'for arg do url=$arg; done' \
  'case "$url" in' \
  '  */releases/latest) exit 22 ;;' \
  '  */releases\?per_page=100) printf '\''[{"tag_name":"v1.2.3-alpha.2","prerelease":true}]\n'\'' ;;' \
  '  *) exit 22 ;;' \
  'esac' >"$mock_bin/curl"
chmod +x "$mock_bin/curl"
prerelease=$(
  PATH="$mock_bin:$PATH" \
  KIGO_INSTALL_DRY_RUN=1 \
  KIGO_TEST_UNAME_S=Linux \
  KIGO_TEST_UNAME_M=x86_64 \
    "$ROOT/scripts/install.sh"
)
expect_line "$prerelease" "version=v1.2.3-alpha.2"
expect_line "$prerelease" "archive=kigo-v1.2.3-alpha.2-linux-amd64.tar.gz"

version=v9.8.7-test
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "offline installer test unsupported on this host"; exit 0 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "offline installer test unsupported on this architecture"; exit 0 ;;
esac
release="$WORK/release"
root="kigo-$version-$os-$arch"
mkdir -p "$release/$root"
printf '%s\n' '#!/bin/sh' 'printf '\''{"version":"v9.8.7-test"}\n'\''' >"$release/$root/kigo"
chmod +x "$release/$root/kigo"
tar -czf "$release/$root.tar.gz" -C "$release" "$root"
rm -rf "$release/$root"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$release" && sha256sum "$root.tar.gz" >SHA256SUMS)
else
  (cd "$release" && shasum -a 256 "$root.tar.gz" >SHA256SUMS)
fi

install_dir="$WORK/bin"
KIGO_VERSION=$version \
KIGO_RELEASE_BASE_URL="file://$release" \
KIGO_INSTALL_DIR="$install_dir" \
  "$ROOT/scripts/install.sh"
test -x "$install_dir/kigo"
test "$("$install_dir/kigo")" = '{"version":"v9.8.7-test"}'

printf 'tampered\n' >>"$release/$root.tar.gz"
if KIGO_VERSION=$version \
  KIGO_RELEASE_BASE_URL="file://$release" \
  KIGO_INSTALL_DIR="$WORK/tampered-bin" \
  "$ROOT/scripts/install.sh" >"$WORK/tampered.log" 2>&1; then
  echo "installer accepted an archive with the wrong SHA-256" >&2
  exit 1
fi
grep -Fq "SHA-256 mismatch" "$WORK/tampered.log"
test ! -e "$WORK/tampered-bin/kigo"

printf 'installer smoke passed\n'
