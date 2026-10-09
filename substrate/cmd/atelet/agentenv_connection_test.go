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
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type runtimeInfoAteom struct {
	mu sync.Mutex
	ateompb.UnimplementedAteomServer
	version string
	wrong   bool
	stopped bool
}

func (s *runtimeInfoAteom) ReadRuntimeInfo(_ context.Context, r *pb.ReadRuntimeInfoRequest) (*pb.InspectResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fence := proto.CloneOf(r.Execution.Fence)
	if s.wrong {
		fence.AssignmentGeneration++
	}
	state := "RUNNING"
	if s.stopped {
		state = "STOPPED"
	}
	return &pb.InspectResponse{Fence: fence, State: state, EnvdVersion: s.version}, nil
}
func TestAgentENVConnectionRequiresExactAllocation(t *testing.T) {
	old := agentENVPreparationsDir
	agentENVPreparationsDir = t.TempDir()
	defer func() { agentENVPreparationsDir = old }()
	op := &pb.LifecycleOperation{OperationId: "activate", Fence: &pb.Fence{ProtocolVersion: "agentenv-executor-v1", ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "executor", AssignmentGeneration: 7}}
	root := filepath.Join(agentENVPreparationsDir, op.Fence.ActorUid)
	launch, err := persistAgentENVLaunch(root, op, &pb.LaunchSpec{Vcpus: 1, MemoryMib: 512})
	if err != nil {
		t.Fatal(err)
	}
	read := proto.CloneOf(op)
	read.OperationId = "connect"
	request := &ateletpb.ReadAgentENVConnectionRequest{ActorUid: op.Fence.ActorUid, TargetAteomUid: "pod", Execution: read}
	fake := &runtimeInfoAteom{version: "0.5.1"}
	serveFakeAteom(t, fake)
	herder := &AteomHerder{ateomDialer: newAteomDialer(1)}
	response, err := herder.ReadAgentENVConnection(t.Context(), request)
	if err != nil || response.GetEnvdAccessToken() != launch.EnvdAccessToken || response.GetEnvdVersion() != "0.5.1" {
		t.Fatal("credential lookup failed", err)
	}
	for _, field := range []string{"epoch", "generation", "instance", "pod", "actor"} {
		t.Run(field, func(t *testing.T) {
			r := proto.CloneOf(request)
			switch field {
			case "epoch":
				r.Execution.Fence.WorkerEpoch++
			case "generation":
				r.Execution.Fence.AssignmentGeneration++
			case "instance":
				r.Execution.Fence.WorkerInstanceId = "new"
			case "pod":
				r.TargetAteomUid = "other"
			case "actor":
				r.ActorUid = "01900000-0000-7000-8000-000000000002"
			}
			if _, err := (&AteomHerder{}).ReadAgentENVConnection(t.Context(), r); err == nil {
				t.Fatal("stale allocation exposed credential")
			}
		})
	}
	for _, mode := range []string{"missing-version", "wrong-assignment", "stopped"} {
		fake.mu.Lock()
		fake.version = "0.5.1"
		fake.wrong = false
		fake.stopped = false
		switch mode {
		case "missing-version":
			fake.version = ""
		case "wrong-assignment":
			fake.wrong = true
		case "stopped":
			fake.stopped = true
		}
		fake.mu.Unlock()
		if _, err = herder.ReadAgentENVConnection(t.Context(), request); err == nil {
			t.Fatal("invalid runtime info exposed credential", mode)
		}
	}
	if err = os.Chmod(filepath.Join(root, "agentenv-preparation.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = (&AteomHerder{}).ReadAgentENVConnection(t.Context(), request); err == nil {
		t.Fatal("public credential file accepted")
	}
}
