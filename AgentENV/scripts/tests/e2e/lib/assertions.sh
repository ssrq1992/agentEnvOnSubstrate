#!/usr/bin/env bash
# Lightweight assertion helpers for e2e test suites.

if [[ -z "${E2E_ASSERTIONS_SH_LOADED:-}" ]]; then
  E2E_ASSERTIONS_SH_LOADED=1

  _PASS_COUNT=0
  _FAIL_COUNT=0

  _pass() {
    ((_PASS_COUNT++)) || true
    printf "  %b[PASS]%b %s\n" "${LOG_COLOR_GREEN:-}" "${LOG_COLOR_RESET:-}" "$1"
  }

  _fail() {
    ((_FAIL_COUNT++)) || true
    printf "  %b[FAIL]%b %s\n" "${LOG_COLOR_RED:-}" "${LOG_COLOR_RESET:-}" "$1" >&2
    [[ -n "${2:-}" ]] && printf "         expected: %s\n         got:      %s\n" "$2" "$3" >&2
  }

  assert_eq() {
    local actual="$1" expected="$2" msg="${3:-assert_eq}"
    if [[ "$actual" == "$expected" ]]; then
      _pass "$msg"
    else
      _fail "$msg" "$expected" "$actual"
    fi
  }

  assert_not_eq() {
    local actual="$1" not_expected="$2" msg="${3:-assert_not_eq}"
    if [[ "$actual" != "$not_expected" ]]; then
      _pass "$msg"
    else
      _fail "$msg" "not $not_expected" "$actual"
    fi
  }

  assert_contains() {
    local haystack="$1" needle="$2" msg="${3:-assert_contains}"
    if [[ "$haystack" == *"$needle"* ]]; then
      _pass "$msg"
    else
      _fail "$msg" "*${needle}*" "$haystack"
    fi
  }

  assert_not_empty() {
    local value="$1" msg="${2:-assert_not_empty}"
    if [[ -n "$value" ]]; then
      _pass "$msg"
    else
      _fail "$msg" "(non-empty)" "(empty)"
    fi
  }

  assert_status() {
    local actual="$1" expected="$2" msg="${3:-HTTP status}"
    assert_eq "$actual" "$expected" "$msg (HTTP $expected)"
  }

  assert_json_field() {
    local json="$1" jq_expr="$2" expected="$3" msg="${4:-json field}"
    local actual
    actual=$(echo "$json" | jq -r "$jq_expr" 2>/dev/null) || actual="(jq error)"
    assert_eq "$actual" "$expected" "$msg"
  }

  # Print suite summary and return non-zero if any test failed.
  suite_summary() {
    local suite_name="${1:-suite}"
    local total=$((_PASS_COUNT + _FAIL_COUNT))
    _E2E_SUITE_SUMMARY_RAN=1
    echo ""
    if [[ "$_FAIL_COUNT" -eq 0 ]]; then
      printf "%b[%s] All %d tests passed.%b\n" "${LOG_COLOR_GREEN:-}" "$suite_name" "$total" "${LOG_COLOR_RESET:-}"
    else
      printf "%b[%s] %d/%d tests failed.%b\n" "${LOG_COLOR_RED:-}" "$suite_name" "$_FAIL_COUNT" "$total" "${LOG_COLOR_RESET:-}"
    fi
    return $(( _FAIL_COUNT > 0 ? 1 : 0 ))
  }
fi
