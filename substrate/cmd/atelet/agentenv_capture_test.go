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

package main

import (
	"context"
	"github.com/agent-substrate/substrate/cmd/atelet/internal/ateletpath"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"github.com/agent-substrate/substrate/internal/resources"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"testing"
)

func (f *fakeAteom) CaptureWorkload(ctx context.Context, req *ateompb.CheckpointWorkloadRequest) (*ateompb.CheckpointWorkloadResponse, error) {
	if err := os.MkdirAll(req.ActorDirs.CheckpointDir, 0700); err != nil {
		return nil, err
	}
	f.recordActorDirs("CaptureWorkload", req.ActorDirs)
	return f.CheckpointWorkload(ctx, req)
}
func TestAgentENVOnlineCapturePreservesSourceAndSeparatesExports(t *testing.T) {
	useTempNodeDirs(t)
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	if _, err := persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}); err != nil {
		t.Fatal(err)
	}
	if err := writeSandboxRecord(uid, &sandboxAssetsRecord{SandboxClass: "agentenv"}); err != nil {
		t.Fatal(err)
	}
	regular := ateletpath.CheckpointStateDir(uid)
	if err := os.MkdirAll(regular, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(regular, "existing")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAteom{snapshotFiles: map[string]string{"agentenv-manifest.v1.json": "portable", "memory.layer": "memory"}}
	serveFakeAteom(t, fake)
	storage := &recordingObjectStorage{}
	herder := &AteomHerder{ateomDialer: newAteomDialer(1), gcsClient: storage}
	exports := map[string]bool{}
	for _, operation := range []string{"capture-one", "capture-two"} {
		execution := proto.CloneOf(op)
		execution.OperationId = operation
		uri, err := resources.NewActorSnapshotURI("gs://bucket/root", "space", uid, operation)
		if err != nil {
			t.Fatal(err)
		}
		req := &ateletpb.CheckpointRequest{Execution: execution, ActorUid: uid, TargetAteomUid: "pod", Atespace: "space", ActorName: "actor", Scope: ateletpb.SnapshotScope_SNAPSHOT_SCOPE_FULL, Type: ateletpb.CheckpointType_CHECKPOINT_TYPE_EXTERNAL, Config: &ateletpb.CheckpointRequest_ExternalConfig{ExternalConfig: &ateletpb.ExternalCheckpointConfiguration{SnapshotUri: uri.String()}}}
		if _, err = herder.Capture(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		dir := fake.actorDirs["CaptureWorkload"].CheckpointDir
		if dir == regular || exports[dir] {
			t.Fatal("capture overwrote another snapshot directory")
		}
		exports[dir] = true
		if _, err = os.Stat(filepath.Join(dir, "memory.layer")); err != nil {
			t.Fatal(err)
		}
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve" {
		t.Fatal("capture removed existing checkpoint")
	}
	record, err := readSandboxRecord(uid)
	if err != nil || len(record.SnapshotFiles) != 0 {
		t.Fatal("capture rewrote source runtime record", err)
	}
	if err = validateAgentENVExisting(uid, op); err != nil {
		t.Fatal("source allocation was cleared", err)
	}
	if len(storage.keys()) != 6 {
		t.Fatalf("incomplete exports: %v", storage.keys())
	}
	if fake.actorDirs["TerminateWorkload"] != nil {
		t.Fatal("online capture terminated source")
	}
}

func TestCaptureIntentPinsDestinationBeforeExecution(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	request := &ateletpb.CheckpointRequest{ActorUid: "actor", Config: &ateletpb.CheckpointRequest_ExternalConfig{ExternalConfig: &ateletpb.ExternalCheckpointConfiguration{SnapshotUri: "gs://bucket/one"}}}
	if err := reserveAgentENVCapture(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := reserveAgentENVCapture(dir, request); err != nil {
		t.Fatal("retry rejected", err)
	}
	request.GetExternalConfig().SnapshotUri = "gs://bucket/two"
	if err := reserveAgentENVCapture(dir, request); err == nil {
		t.Fatal("capture destination changed on retry")
	}
}
