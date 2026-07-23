#!/usr/bin/env sh
set -eu

repo=${KIGO_REPO:-suir1/kigo}
version=${KIGO_VERSION:-}
install_dir=${KIGO_INSTALL_DIR:-${PREFIX:-$HOME/.local}/bin}
release_base=${KIGO_RELEASE_BASE_URL:-}
dry_run=${KIGO_INSTALL_DRY_RUN:-}
add_to_path=${KIGO_ADD_TO_PATH:-}

die() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

truthy() {
  case "$1" in
    1 | true | TRUE | yes | YES | on | ON) return 0 ;;
    *) return 1 ;;
  esac
}

shell_quote() {
  printf "%s" "$1" | sed "s/'/'\\\\''/g; 1s/^/'/; \$s/\$/'/"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    die "sha256sum, shasum, or openssl is required to verify the release"
  fi
}

if [ -z "$version" ]; then
  need curl
  case "$repo" in
    *[!A-Za-z0-9._/-]* | /* | */../* | ../* | */.. | ..)
      die "invalid KIGO_REPO: $repo"
      ;;
  esac
  version=$(
    curl -fsSL \
      -H "Accept: application/vnd.github+json" \
      -H "User-Agent: kigo-install" \
      "https://api.github.com/repos/$repo/releases/latest" |
      sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' |
      head -n 1
  ) || version=
  [ -n "$version" ] || die "could not determine the latest release; set KIGO_VERSION explicitly"
fi

case "$version" in
  *[!A-Za-z0-9._+-]* | '') die "invalid KIGO_VERSION: $version" ;;
esac

os_name=${KIGO_TEST_UNAME_S:-$(uname -s)}
arch_name=${KIGO_TEST_UNAME_M:-$(uname -m)}
case "$os_name" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported operating system: $os_name" ;;
esac
case "$arch_name" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch_name" ;;
esac

root="kigo-$version-$os-$arch"
archive="$root.tar.gz"
if [ -z "$release_base" ]; then
  case "$repo" in
    *[!A-Za-z0-9._/-]* | /* | */../* | ../* | */.. | ..)
      die "invalid KIGO_REPO: $repo"
      ;;
  esac
  release_base="https://github.com/$repo/releases/download/$version"
fi
release_base=${release_base%/}
case "$release_base" in
  https://* | file://* | http://127.0.0.1:* | http://localhost:*) ;;
  *) die "KIGO_RELEASE_BASE_URL must use HTTPS, file, or loopback HTTP" ;;
esac
archive_url="$release_base/$archive"
checksums_url="$release_base/SHA256SUMS"

if truthy "$dry_run"; then
  echo "version=$version"
  echo "platform=$os-$arch"
  echo "archive=$archive"
  echo "archive_url=$archive_url"
  echo "checksums_url=$checksums_url"
  echo "install_dir=$install_dir"
  exit 0
fi

need curl
need tar
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

echo "Downloading $archive_url"
curl -fsSL --retry 3 --retry-delay 1 "$archive_url" -o "$work/$archive"
curl -fsSL --retry 3 --retry-delay 1 "$checksums_url" -o "$work/SHA256SUMS"

expected=$(
  awk -v name="$archive" '
    ($2 == name || $2 == "*" name) && $1 ~ /^[0-9A-Fa-f]{64}$/ {
      count++
      digest = tolower($1)
    }
    END {
      if (count == 1) print digest
    }
  ' "$work/SHA256SUMS"
)
[ -n "$expected" ] || die "SHA256SUMS does not contain exactly one entry for $archive"
actual=$(sha256_file "$work/$archive" | tr '[:upper:]' '[:lower:]')
[ "$actual" = "$expected" ] || die "SHA-256 mismatch for $archive"

tar -tzf "$work/$archive" | awk -v root="$root" '
  $0 == root || $0 == root "/" || index($0, root "/") == 1 { next }
  { exit 1 }
' || die "release archive contains a path outside $root"
tar -xzf "$work/$archive" -C "$work"
source_path="$work/$root/kigo"
[ -f "$source_path" ] && [ ! -L "$source_path" ] || die "release archive does not contain $root/kigo"

mkdir -p "$install_dir"
target="$install_dir/kigo"
staged="$install_dir/.kigo.install.$$"
trap 'rm -rf "$work"; rm -f "$staged"' EXIT HUP INT TERM
cp "$source_path" "$staged"
chmod 0755 "$staged"
"$staged" version --json >/dev/null || die "downloaded kigo binary failed its version check"
mv -f "$staged" "$target"

echo "Installed kigo $version to $target"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    if truthy "$add_to_path"; then
      profile=${KIGO_PATH_PROFILE:-$HOME/.profile}
      mkdir -p "$(dirname "$profile")"
      quoted_dir=$(shell_quote "$install_dir")
      if [ -f "$profile" ] && grep -Fq "$quoted_dir" "$profile"; then
        echo "$install_dir is already configured in $profile"
      else
        printf '\n# kigo installer PATH\nexport PATH=%s:$PATH\n' "$quoted_dir" >>"$profile"
        echo "Added $install_dir to PATH in $profile; restart the shell to apply it"
      fi
    else
      echo "Add $install_dir to PATH, or rerun with KIGO_ADD_TO_PATH=1"
    fi
    ;;
esac
