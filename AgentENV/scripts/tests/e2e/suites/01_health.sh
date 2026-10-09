#!/usr/bin/env bash
set -euo pipefail

SUITE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${SUITE_DIR}/../lib/helpers.sh"
init_suite "01_health"

log "Suite: Health Check"

api_get "/health"
assert_status "$HTTP_STATUS" "204" "GET /health"

suite_summary "01_health"
