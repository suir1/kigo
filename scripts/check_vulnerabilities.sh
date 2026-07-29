#!/usr/bin/env bash
set -euo pipefail

version=${GOVULNCHECK_VERSION:-v1.6.0}
exec go run "golang.org/x/vuln/cmd/govulncheck@${version}" "$@"
