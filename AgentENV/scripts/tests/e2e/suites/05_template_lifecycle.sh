#!/usr/bin/env bash
set -euo pipefail

SUITE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${SUITE_DIR}/../lib/helpers.sh"
init_suite "05_template"

log "Suite: Template Lifecycle"

TEMPLATE_ALIAS="e2e-test-$(date +%s)"
SHORT_IMAGE_ALIAS="e2e-short-image-$(date +%s)"
SHORT_USER_IMAGE="${E2E_SHORT_USER_IMAGE:-${E2E_DEFAULT_USER_IMAGE}}"

# -- Create template via v3 endpoint --
create_template "$TEMPLATE_ALIAS"
assert_status "$HTTP_STATUS" "202" "POST /v3/templates and start build return 202"

template_id=$(echo "$HTTP_BODY" | jq -r '.templateID // empty')
build_id=$(echo "$HTTP_BODY" | jq -r '.buildID // empty')
assert_not_empty "$template_id" "templateID present"
assert_not_empty "$build_id" "buildID present"
track_template "$template_id"

# -- Poll until build is ready (or error) --
log "Waiting for template build to complete ..."
if wait_for_template_build "$template_id" 120 "$build_id"; then
  _pass "template build reached 'ready'"
else
  _fail "template build reached 'ready'" "ready" "failed or timed out"
fi

# -- List templates, verify new template appears --
api_get "/v2/templates"
assert_status "$HTTP_STATUS" "200" "GET /v2/templates returns 200"
assert_contains "$HTTP_BODY" "$template_id" "template appears in list"

# -- Spawn sandbox from new template --
sandbox_id=$(create_sandbox "$TEMPLATE_ALIAS" 60); _sync_http
assert_status "$HTTP_STATUS" "201" "create sandbox from new template"
assert_not_empty "$sandbox_id" "sandboxID present"
track_sandbox "$sandbox_id"
wait_for_sandbox_state "$sandbox_id" "running" 30

# -- Cleanup --
delete_sandbox "$sandbox_id"
api_delete "/templates/${template_id}"

# -- Create template from an explicit fromImage value --
log "Creating template '${SHORT_IMAGE_ALIAS}' from explicit fromImage '${SHORT_USER_IMAGE}' ..."
create_template "$SHORT_IMAGE_ALIAS" "$SHORT_USER_IMAGE"
assert_status "$HTTP_STATUS" "202" "template build accepts explicit fromImage"

short_template_id=$(echo "$HTTP_BODY" | jq -r '.templateID // empty')
short_build_id=$(echo "$HTTP_BODY" | jq -r '.buildID // empty')
assert_not_empty "$short_template_id" "explicit fromImage templateID present"
assert_not_empty "$short_build_id" "explicit fromImage buildID present"
track_template "$short_template_id"

if wait_for_template_build "$short_template_id" 120 "$short_build_id"; then
  _pass "explicit fromImage template build reached 'ready'"
else
  _fail "explicit fromImage template build reached 'ready'" "ready" "failed or timed out"
fi

short_sandbox_id=$(create_sandbox "$SHORT_IMAGE_ALIAS" 60); _sync_http
assert_status "$HTTP_STATUS" "201" "create sandbox from explicit fromImage template"
assert_not_empty "$short_sandbox_id" "explicit fromImage sandboxID present"
track_sandbox "$short_sandbox_id"
wait_for_sandbox_state "$short_sandbox_id" "running" 30

delete_sandbox "$short_sandbox_id"
api_delete "/templates/${short_template_id}"

suite_summary "05_template"
