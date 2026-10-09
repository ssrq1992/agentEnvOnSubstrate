#!/usr/bin/env bash
# Copyright 2026 Google LLC
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
source_dir="${AGENTENV_SOURCE_DIR:-${root}/../AgentENV}/protocols/aenv-executor/v1"
cd "${root}"
mkdir -p internal/proto/aenvexecutorpb
generated="$(mktemp -d)"
trap 'rm -rf "${generated}"' EXIT
go_plugin="$(./hack/run-tool.sh --print-bin-path protoc-gen-go)"
grpc_plugin="$(./hack/run-tool.sh --print-bin-path protoc-gen-go-grpc)"
./hack/protoc.sh -I "${source_dir}" \
  --plugin="protoc-gen-go=${go_plugin}" \
  --plugin="protoc-gen-go-grpc=${grpc_plugin}" \
  --go_out="paths=source_relative:${generated}" \
  --go-grpc_out="paths=source_relative:${generated}" \
  "${source_dir}/executor.proto"
for file in "${generated}"/*.go; do
  cat hack/boilerplate/go.txt "${file}" > "internal/proto/aenvexecutorpb/$(basename "${file}")"
done
