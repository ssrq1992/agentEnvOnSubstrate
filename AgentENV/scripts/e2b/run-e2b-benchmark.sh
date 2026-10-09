#!/usr/bin/env bash
set -euo pipefail

INFRA_REPO_URL="${INFRA_REPO_URL:-https://github.com/Loken-Lau/infra.git}"
INFRA_DIR="${INFRA_DIR:-infra}"
KERNEL_BUCKET_PATH="${KERNEL_BUCKET_PATH:-gs://e2b-prod-public-builds/kernels/}"

log() {
  echo "[$(date '+%H:%M:%S')] $*"
}

ensure_gsutil() {
  local sdk_dir="${HOME}/google-cloud-sdk"
  local sdk_bin="${sdk_dir}/bin"
  local sdk_gsutil="${sdk_bin}/gsutil"

  if command -v gsutil >/dev/null 2>&1; then
    log "gsutil is already available at: $(command -v gsutil)"
    gsutil version -l | head -n 5 || true
    return 0
  fi

  if [[ -x "${sdk_gsutil}" ]]; then
    log "Reusing cached gsutil from ${sdk_gsutil}"
    export PATH="${sdk_bin}:${PATH}"
  fi

  if command -v gsutil >/dev/null 2>&1; then
    log "gsutil is now available at: $(command -v gsutil)"
    gsutil version -l | head -n 5 || true
    return 0
  fi

  log "gsutil not found, attempting installation"
  sudo apt-get update

  if ! command -v curl >/dev/null 2>&1; then
    sudo apt-get install -y curl
  fi

  if ! sudo apt-get install -y google-cloud-cli; then
    log "google-cloud-cli apt package unavailable, falling back to installer script"
    if [[ -d "${sdk_dir}" && ! -x "${sdk_gsutil}" ]]; then
      log "Removing stale SDK directory at ${sdk_dir}"
      rm -rf "${sdk_dir}"
    fi
    curl -fsSL https://sdk.cloud.google.com > /tmp/install-gcloud.sh
    bash /tmp/install-gcloud.sh --disable-prompts --install-dir="${HOME}"
  fi

  if [[ -x "${sdk_gsutil}" ]]; then
    export PATH="${sdk_bin}:${PATH}"
  fi

  if ! command -v gsutil >/dev/null 2>&1; then
    log "gsutil installation failed"
    exit 1
  fi

  gsutil version -l | head -n 5 || true
}

verify_kernel_bucket_access() {
  log "Verifying kernel bucket access: ${KERNEL_BUCKET_PATH}"
  local first_entry
  first_entry="$(gsutil ls "${KERNEL_BUCKET_PATH}" | head -n 1 || true)"
  if [[ -z "${first_entry}" ]]; then
    log "Kernel bucket check failed: no objects listed from ${KERNEL_BUCKET_PATH}"
    exit 1
  fi
  log "Kernel bucket reachable, sample object: ${first_entry}"
}

run_e2b_benchmark() {
  log "Preparing infra workspace"
  rm -rf "${INFRA_DIR}"
  git clone "${INFRA_REPO_URL}" "${INFRA_DIR}"

  local benchmark_status=0
  local cleanup_status=0

  pushd "${INFRA_DIR}" >/dev/null
  log "Running local bootstrap up to base template"
  if make local-bootstrap-up-to-base-template; then
    log "Running template restore benchmark"
    if ./benchmark-template-restore.sh -n 50 -t base; then
      log "Template restore benchmark completed"
    else
      benchmark_status=$?
      log "Template restore benchmark failed with exit code ${benchmark_status}"
    fi
  else
    benchmark_status=$?
    log "local-bootstrap-up-to-base-template failed with exit code ${benchmark_status}"
  fi

  log "Running local dev cleanup after benchmark"
  if (cd scripts && ./local-dev-cleanup-after-benchmark.sh); then
    log "local-dev-cleanup-after-benchmark.sh completed"
  else
    cleanup_status=$?
    log "local-dev-cleanup-after-benchmark.sh failed with exit code ${cleanup_status}"
  fi
  popd >/dev/null

  if [[ ${benchmark_status} -ne 0 ]]; then
    return "${benchmark_status}"
  fi

  if [[ ${cleanup_status} -ne 0 ]]; then
    return "${cleanup_status}"
  fi
}

main() {
  log "Starting E2B benchmark runner"
  ensure_gsutil
  verify_kernel_bucket_access
  run_e2b_benchmark
  log "E2B benchmark runner completed"
}

main "$@"
