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
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentENVTypedLaunch(t *testing.T) {
	spec := &ateletpb.WorkloadSpec{Containers: []*ateletpb.Container{{Image: "example/image@sha256:" + strings.Repeat("a", 64), Command: []string{"python"}, Args: []string{"app.py"}, Env: []*ateletpb.EnvEntry{{Name: "X", Value: "Y"}}}}}
	rec := &sandboxAssetsRecord{Assets: map[string]assetEntry{"kernel": {SHA256: "b"}, "firecracker": {SHA256: "a"}}}
	paths := map[string]string{"kernel": "/assets/kernel", "firecracker": "/assets/firecracker"}
	spec.Containers[0].AgentenvExtensionParams = `{"custom":{"key":"value"}}`
	launch, err := agentENVLaunch(spec, rec, paths, 2000, 512*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if launch.GetExtensions().GetJson() != spec.Containers[0].AgentenvExtensionParams {
		t.Fatal("extension launch params lost")
	}
	if launch.Vcpus != 2 || launch.MemoryMib != 512 || launch.Assets[0].Name != "firecracker" || launch.Argv[1] != "app.py" {
		t.Fatalf("wrong typed launch: %v", launch)
	}
	for _, cpu := range []int64{0, 500, 1500} {
		if _, err := agentENVLaunch(spec, rec, paths, cpu, 512*1024*1024); err == nil {
			t.Fatal("fractional or unset CPU accepted")
		}
	}
	if _, err := agentENVLaunch(spec, rec, paths, 1000, 513); err == nil {
		t.Fatal("unaligned memory accepted")
	}
	spec.Containers[0].Image = "example/image:latest"
	if _, err := agentENVLaunch(spec, rec, paths, 1000, 512*1024*1024); err == nil {
		t.Fatal("mutable image accepted")
	}
}
func TestAgentENVPreparationReusesCredentialAndRejectsChangedIntent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actor")
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	launch := &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}
	first, err := persistAgentENVLaunch(root, op, launch)
	if err != nil {
		t.Fatal(err)
	}
	again, err := persistAgentENVLaunch(root, op, launch)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(first, again) || len(first.EnvdAccessToken) != 64 || launch.EnvdAccessToken != "" {
		t.Fatal("retry changed token or mutated input")
	}
	launch.MemoryMib = 1024
	if _, err := persistAgentENVLaunch(root, op, launch); err == nil {
		t.Fatal("same operation changed specification")
	}
	op.Fence.AssignmentGeneration++
	next, err := persistAgentENVLaunch(root, op, launch)
	if err != nil {
		t.Fatal(err)
	}
	if next.EnvdAccessToken == first.EnvdAccessToken {
		t.Fatal("new allocation reused credential")
	}
	op.Fence.AssignmentGeneration--
	if _, err := persistAgentENVLaunch(root, op, launch); err == nil {
		t.Fatal("stale allocation accepted")
	}
	info, err := os.Stat(filepath.Join(root, "agentenv-preparation.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential file permissions")
	}
}

func TestAgentENVTerminateRetryAfterAssetCleanup(t *testing.T) {
	useTempNodeDirs(t)
	previous := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	t.Cleanup(func() { agentENVPreparationsDir = previous })
	uid := "01900000-0000-7000-8000-000000000001"
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: uid, WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	if _, err := persistAgentENVLaunch(filepath.Join(agentENVPreparationsDir, uid), op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512}); err != nil {
		t.Fatal(err)
	}
	ateom := &fakeAteom{}
	serveFakeAteom(t, ateom)
	s := &AteomHerder{ateomDialer: newAteomDialer(1), systemInfoVolumes: newSystemInfoVolumeRefresher(nil, nil)}
	op = proto.CloneOf(op)
	op.OperationId = "terminate"
	req := &ateletpb.TerminateRequest{Atespace: "space", ActorName: "actor", ActorUid: uid, TargetAteomUid: "pod", Execution: op}
	// No sandbox assets exist, including after the first termination.
	for range 2 {
		if _, err := s.Terminate(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	if ateom.actorDirs["TerminateWorkload"] == nil {
		t.Fatal("termination did not reach fenced executor")
	}
}

func TestAgentENVLaunchForwardsColdDrivesAndDisk(t *testing.T) {
	options := &ateapipb.AgentENVLaunchConfig{RootfsBytes: 4 * 1024 * 1024 * 1024, ExtraBootArgs: "demo=1", Drives: []*ateapipb.AgentENVAttachedDrive{{Id: "data", Image: "registry/data@sha256:" + strings.Repeat("b", 64), MountPath: "/mnt/data", ReadOnly: true, SubPath: "files"}}}
	spec := &ateletpb.WorkloadSpec{Containers: []*ateletpb.Container{{Image: "registry/root@sha256:" + strings.Repeat("a", 64), AgentenvLaunch: options}}}
	launch, err := agentENVLaunch(spec, &sandboxAssetsRecord{Assets: map[string]assetEntry{}}, nil, 1000, 128*1024*1024)
	if err != nil || launch.RootfsBytes != options.RootfsBytes || launch.ExtraBootArgs != options.ExtraBootArgs || len(launch.Drives) != 1 || launch.Drives[0].SubPath != "files" || launch.Drives[0].ImageDigest != options.Drives[0].Image {
		t.Fatal(launch, err)
	}
}
