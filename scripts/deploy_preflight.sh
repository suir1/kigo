#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ENV_FILE="$ROOT/deploy/production.env"
RUNTIME=0
BUILD=1
ALLOW_PLACEHOLDERS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      [[ $# -ge 2 ]] || { echo "--env-file requires a path" >&2; exit 2; }
      ENV_FILE=$2
      shift 2
      ;;
    --runtime)
      RUNTIME=1
      shift
      ;;
    --no-build)
      BUILD=0
      shift
      ;;
    --allow-placeholders)
      ALLOW_PLACEHOLDERS=1
      shift
      ;;
    *)
      echo "usage: $0 [--env-file PATH] [--runtime] [--no-build] [--allow-placeholders]" >&2
      exit 2
      ;;
  esac
done

[[ -f "$ENV_FILE" ]] || {
  echo "production env file not found: $ENV_FILE" >&2
  echo "copy deploy/production.env.example and replace its domain, IP, and secrets" >&2
  exit 2
}
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 2; }
docker compose version >/dev/null

env_value() {
  local key=$1
  awk -v key="$key" '
    /^[[:space:]]*#/ { next }
    index($0, key "=") == 1 {
      print substr($0, length(key) + 2)
      exit
    }
  ' "$ENV_FILE"
}

if [[ "$ALLOW_PLACEHOLDERS" == "0" ]]; then
  domain=$(env_value KIGO_DOMAIN)
  public_ip=$(env_value KIGO_PUBLIC_IP)
  [[ -n "$domain" && "$domain" != *.example ]] || {
    echo "KIGO_DOMAIN must be a real deployment domain, not an .example placeholder" >&2
    exit 2
  }
  [[ -n "$public_ip" && "$public_ip" != 203.0.113.* && "$public_ip" != 198.51.100.* && "$public_ip" != 192.0.2.* ]] || {
    echo "KIGO_PUBLIC_IP must be the server's real public IPv4 address" >&2
    exit 2
  }
  for key in KIGO_NATIVE_RELAY_SECRET KIGO_TURN_SECRET; do
    value=$(env_value "$key")
    [[ ${#value} -ge 32 && "$value" != *replace* && "$value" != *change* ]] || {
      echo "$key must contain an independent random value of at least 32 characters" >&2
      exit 2
    }
  done
fi

compose=(docker compose --env-file "$ENV_FILE" -f "$ROOT/deploy/compose.production.yml")
"${compose[@]}" config --quiet
echo "Compose configuration valid"

if [[ "$RUNTIME" == "1" ]]; then
  docker info >/dev/null 2>&1 || {
    echo "Docker daemon is not running; start it before using --runtime" >&2
    exit 2
  }
  project="kigo-preflight-$$"
  runtime_compose=(docker compose --project-name "$project" --env-file "$ENV_FILE" -f "$ROOT/deploy/compose.production.yml")
  cleanup_runtime() {
    "${runtime_compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  }
  trap cleanup_runtime EXIT
  if [[ "$BUILD" == "1" ]]; then
    "${runtime_compose[@]}" build kigo
  fi
  "${runtime_compose[@]}" run --rm --no-deps kigo serve --check-config
  "${runtime_compose[@]}" run --rm --no-deps --entrypoint sh kigo -c 'command -v wget >/dev/null'
  "${runtime_compose[@]}" run --rm --no-deps --entrypoint caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
  echo "Container configuration valid"
fi
