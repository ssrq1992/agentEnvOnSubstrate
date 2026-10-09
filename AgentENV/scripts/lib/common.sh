#!/usr/bin/env bash

if [[ -z "${COMMON_SH_LOADED:-}" ]]; then
  COMMON_SH_LOADED=1
  : "${_E2E_ERROR_REPORTED:=0}"

  if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    LOG_COLOR_GREEN='\033[0;32m'
    LOG_COLOR_RED='\033[0;31m'
    LOG_COLOR_RESET='\033[0m'
  else
    LOG_COLOR_GREEN=''
    LOG_COLOR_RED=''
    LOG_COLOR_RESET=''
  fi

  log() {
    printf "%b[%s]%b %s\n" "${LOG_COLOR_GREEN}" "${LOG_TAG:-INFO}" "${LOG_COLOR_RESET}" "$*"
  }

  warn() {
    printf "%b[WARN]%b %s\n" "${LOG_COLOR_RED}" "${LOG_COLOR_RESET}" "$*" >&2
  }

  error() {
    printf "%b[ERROR]%b %s\n" "${LOG_COLOR_RED}" "${LOG_COLOR_RESET}" "$*" >&2
  }

  die() {
    _E2E_ERROR_REPORTED=1
    error "$*"
    exit 1
  }

  require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
  }

  ensure_sudo() {
    if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
      return
    fi
    require_cmd sudo
    if ! sudo -n true 2>/dev/null; then
      log "Sudo privileges are required for this step."
      sudo true || die "Failed to obtain sudo privileges."
    fi
  }
fi
