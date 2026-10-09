#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# This is a component verification entry point, not F01-F11 acceptance.
for program in go cargo rustc python3; do
  command -v "${program}" >/dev/null || { echo "Missing required tool: ${program}" >&2; exit 1; }
done
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || {
  echo 'Source verification requires Linux amd64 (Rust backend is Linux-specific).' >&2
  exit 1
}

cd "${root}/substrate"
[[ "$(go env GOVERSION)" == go1.27.0 ]] || { echo 'Expected Go 1.27.0' >&2; exit 1; }
[[ "$(rustc --version)" == 'rustc 1.99.0 '* ]] || { echo 'Expected Rust 1.99.0' >&2; exit 1; }
export REQUIRE_DOCKER=true
export AENV_REQUIRE_CGROUP_TESTS=true
bash hack/update/codegen.sh
go test -race ./cmd/ateom-agentenv/internal/... ./internal/aenvexecutor ./internal/cgroupstats ./internal/podidentityissuer ./cmd/ate-generic-manifests ./cmd/ate-identity-bootstrap ./cmd/atelet/internal/trustbundle ./cmd/ateapi/internal/apivalidation
# These tests do not use envtest. Name the files to avoid the controller
# package's TestMain, which exits before m.Run when -short is supplied.
go test -race \
  cmd/atecontroller/internal/controllers/workerpool_apply.go \
  cmd/atecontroller/internal/controllers/workerpool_agentenv_test.go \
  cmd/atecontroller/internal/controllers/egressmitmtrust_controller.go \
  cmd/atecontroller/internal/controllers/egressmitmtrust_controller_test.go \
  -run 'TestAgentENVPodShapeAndStableCertificates|TestExistingBackendsRemainUnprivileged|TestEgressTrustConfigMapProvider|TestAgentENVActorLimitIsExplicit|TestAgentENVDeviceLimitIsExplicit'
go test -race ./cmd/atelet ./cmd/ateapi/internal/controlapi ./cmd/ateapi/internal/authz ./cmd/ateapi/internal/scheduling ./cmd/atecontroller/internal/workersync
go build ./cmd/ateom-agentenv ./cmd/atelet ./cmd/atecontroller ./cmd/podidentityissuer ./cmd/podidentityagent ./cmd/ate-generic-manifests ./cmd/ate-identity-bootstrap

export PROTOC="${root}/substrate/bin/protoc-install/bin/protoc"
cd "${root}/AgentENV"
cargo fmt --all -- --check
cargo check --locked --bin aenv-executor
cargo clippy --locked --bin aenv-executor -- -D warnings
cargo test --locked --bin aenv-executor
cargo test --locked --lib embedded::
cargo test --locked -p uvm-ublk-daemon --lib device_ledger::tests
cargo test --locked -p uvm-ublk-daemon --lib client::tests::shutdown_
cargo test --locked -p uvm-ublk-daemon --test ublk_daemon_test shutdown_rejects_unconfirmed_cleanup_without_response
cargo test --locked --lib portable_tools_are_verified_locally_and_not_serialized_as_host_paths
cargo test --locked --lib worker_platform_cidrs_apply_to_proxy_upstreams
cargo test --locked --lib sandbox::firecracker::instance::tests::scoped_spawn_joins_before_exec_without_moving_parent
cargo test --locked --lib sandbox::firecracker::instance::tests::stop_
cargo test --locked --lib sandbox::firecracker::startup_pack::tests::confirmed_stop_
cd "${root}/AgentENV/services"
go test -race ./gateway/... ./shared/config
cd "${root}/AgentENV/services/aenv-api-bridge"
# Database contracts are mandatory in this Linux verification entry point.
REQUIRE_CATALOG_DATABASE=true go test -race ./...
go vet ./...
go build ./cmd/aenv-api-bridge

python3 -m unittest discover -s "${root}/integration" -p 'test_*.py'
