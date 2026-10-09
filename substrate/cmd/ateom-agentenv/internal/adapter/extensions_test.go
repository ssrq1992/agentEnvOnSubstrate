// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package adapter

import (
	"errors"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"testing"
)

func TestExtensionPatchRequiresApprovedAcknowledgment(t *testing.T) {
	s, ops, network, ingress, launch := serviceFixture(t)
	req := &pb.ApplyExtensionParamsRequest{ActorUid: launch.ActorUid, TargetWorkerPodUid: "pod", Execution: proto.CloneOf(launch.Execution), Patch: &pb.ExtensionParams{Json: `{"operation":"set"}`}}
	req.Execution.OperationId = "extension-patch"
	if _, err := s.ApplyExtensionParams(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("missing approval accepted", err)
	}
	ops.approvedExtensions = &pb.ExtensionParams{Json: `{"normalized":"approved"}`}
	response, err := s.ApplyExtensionParams(t.Context(), req)
	if err != nil || !proto.Equal(response.Approved, ops.approvedExtensions) {
		t.Fatal("approved params lost", err)
	}
	if !proto.Equal(ops.calls[1].GetUpdateExtensions(), req.Patch) {
		t.Fatal("patch changed before extension hook")
	}
	ops.err = &aenvexecutor.EffectUnknown{OperationID: req.Execution.OperationId, Cause: errors.New("timeout")}
	if _, err := s.ApplyExtensionParams(t.Context(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("unknown execution approved", err)
	}
	ops.err = nil
	ops.noEffect = true
	response, err = s.ApplyExtensionParams(t.Context(), req)
	if err != nil || response.Effect != pb.Effect_NO_EFFECT || response.Approved != nil {
		t.Fatal("no-effect rejection lost", err)
	}
	before := len(ops.calls)
	req.Execution.Fence.WorkerEpoch--
	if _, err := s.ApplyExtensionParams(t.Context(), req); status.Code(err) != codes.FailedPrecondition || len(ops.calls) != before {
		t.Fatal("stale patch reached executor", err)
	}
	if network.attached != 0 || network.detached != 0 || ingress.activated != 0 || ingress.deactivated != 0 {
		t.Fatal("extension patch changed network ownership")
	}
}
func TestRestoreKeepsCapturedExtensionState(t *testing.T) {
	s, ops, _, _, launch := serviceFixture(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, manifestName), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	launch.AgentenvLaunch.Extensions = &pb.ExtensionParams{Json: `{"initial":"old"}`}
	request := &ateompb.RestoreWorkloadRequest{ActorUid: launch.ActorUid, Execution: proto.CloneOf(launch.Execution), Scope: ateompb.SnapshotScope_SNAPSHOT_SCOPE_FULL, AgentenvLaunch: launch.AgentenvLaunch, ActorDirs: &ateompb.ActorDirs{RestoreDir: source}}
	request.Execution.OperationId = "restore"
	if _, err := s.RestoreWorkload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if ops.calls[0].GetRestore().GetLaunch().GetExtensions() != nil {
		t.Fatal("initial params overwrite captured live state")
	}
	if launch.AgentenvLaunch.Extensions == nil {
		t.Fatal("restore mutated launch request")
	}
}
