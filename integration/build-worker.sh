#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${AENV_RUNTIME_TAG:?Set the image tag for the modified AgentENV runtime}"
: "${AENV_WORKER_TAG:?Set the image tag for the combined Worker}"
command -v docker >/dev/null || { echo 'Docker is required to build the Linux runtime assets' >&2; exit 1; }
docker build --platform linux/amd64 -f "${root}/AgentENV/deploy/docker/Dockerfile.agentenv" \
  --build-arg EXTRA_RUNTIME_PACKAGES=procps -t "${AENV_RUNTIME_TAG}" "${root}/AgentENV"
runtime_id="$(docker image inspect --format '{{.Id}}' "${AENV_RUNTIME_TAG}")"
runtime_build_tag="aenv-runtime-build:${runtime_id#sha256:}"
docker tag "${runtime_id}" "${runtime_build_tag}"
docker build --platform linux/amd64 -f "${root}/integration/Dockerfile.worker" \
  --build-arg "AGENTENV_RUNTIME_IMAGE=${runtime_build_tag}" -t "${AENV_WORKER_TAG}" "${root}"
# Image IDs are recorded so a release can pin the exact built artifacts.
docker image inspect --format '{{.Id}}' "${AENV_RUNTIME_TAG}" "${AENV_WORKER_TAG}"
