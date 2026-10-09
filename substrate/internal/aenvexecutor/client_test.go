// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aenvexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"

	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type executorStub struct {
	pb.ExecutorClient
	requests []*pb.OperationRequest
	effect   pb.Effect
	err      error
}

func (s *executorStub) Capabilities(context.Context, *pb.CapabilitiesRequest, ...grpc.CallOption) (*pb.CapabilitiesResponse, error) {
	return &pb.CapabilitiesResponse{ProtocolVersion: ProtocolVersion, WorkerInstanceId: "instance", MaxActors: 4}, nil
}
func (s *executorStub) Execute(_ context.Context, r *pb.OperationRequest, _ ...grpc.CallOption) (*pb.OperationResponse, error) {
	s.requests = append(s.requests, r)
	if s.err != nil {
		return nil, s.err
	}
	return &pb.OperationResponse{OperationId: r.OperationId, Effect: s.effect}, nil
}

func TestExecutePreservesIdentityAndPayloadAcrossRetries(t *testing.T) {
	rpc := &executorStub{effect: pb.Effect_COMPLETED}
	client, err := Handshake(context.Background(), rpc)
	if err != nil {
		t.Fatal(err)
	}
	fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
	command := &pb.Command{Action: &pb.Command_Stop{Stop: &pb.StopSpec{}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := client.Execute(ctx, fence, "persisted-operation", command); err != nil {
			t.Fatal(err)
		}
	}
	if len(rpc.requests) != 2 || !proto.Equal(rpc.requests[0], rpc.requests[1]) {
		t.Fatal("retry changed operation")
	}
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(command)
	digest := sha256.Sum256(payload)
	if rpc.requests[0].PayloadDigest != hex.EncodeToString(digest[:]) {
		t.Fatal("wrong wire digest")
	}
	fence.WorkerInstanceId = "old"
	if _, err := client.Execute(ctx, fence, "late-operation", command); err == nil || len(rpc.requests) != 2 {
		t.Fatal("stale request reached executor")
	}
}

func TestUnknownEffectsAreNeverRetriedAutomatically(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effect pb.Effect
		err    error
	}{
		{"transport", pb.Effect_EFFECT_UNSPECIFIED, errors.New("connection lost")},
		{"unknown", pb.Effect_EFFECT_UNKNOWN, nil},
		{"unspecified", pb.Effect_EFFECT_UNSPECIFIED, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &executorStub{effect: tc.effect, err: tc.err}
			client, _ := Handshake(context.Background(), rpc)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
			_, err := client.Execute(ctx, fence, "op", &pb.Command{Action: &pb.Command_Stop{Stop: &pb.StopSpec{}}})
			var unknown *EffectUnknown
			if !errors.As(err, &unknown) || len(rpc.requests) != 1 {
				t.Fatalf("effect handling: %v, %d calls", err, len(rpc.requests))
			}
		})
	}
}

func TestKnownRejectionAndCanonicalActorIdentity(t *testing.T) {
	rpc := &executorStub{effect: pb.Effect_NO_EFFECT}
	client, err := Handshake(context.Background(), rpc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
	command := &pb.Command{Action: &pb.Command_Stop{Stop: &pb.StopSpec{}}}
	_, err = client.Execute(ctx, fence, "rejected", command)
	var unknown *EffectUnknown
	if err == nil || errors.As(err, &unknown) {
		t.Fatalf("explicit pre-effect rejection must be distinguished from unknown: %v", err)
	}
	fence.ActorUid = "urn:uuid:" + fence.ActorUid
	_, err = client.Execute(ctx, fence, "alias", command)
	if err == nil || len(rpc.requests) != 1 {
		t.Fatal("Actor identity alias bypassed canonical validation")
	}
}

func TestLifecycleOperationTargetValidation(t *testing.T) {
	fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
	op := &pb.LifecycleOperation{Fence: fence, OperationId: "persisted"}
	if err := ValidateOperation(op, fence.ActorUid, "pod"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*pb.LifecycleOperation){
		func(op *pb.LifecycleOperation) { op.Fence.WorkerPodUid = "other" },
		func(op *pb.LifecycleOperation) { op.Fence.ActorUid = "01900000-0000-7000-8000-000000000002" },
		func(op *pb.LifecycleOperation) { op.Fence.WorkerInstanceId = "" },
		func(op *pb.LifecycleOperation) { op.Fence.WorkerEpoch = 0 },
		func(op *pb.LifecycleOperation) { op.Fence.AssignmentGeneration = 0 },
		func(op *pb.LifecycleOperation) { op.OperationId = "../op" },
	} {
		changed := proto.CloneOf(op)
		mutate(changed)
		if err := ValidateOperation(changed, fence.ActorUid, "pod"); err == nil {
			t.Fatal("invalid operation accepted")
		}
	}
}

type statsStub struct {
	pb.ExecutorClient
	sample *pb.StatsResponse
	calls  int
}

func (s *statsStub) Stats(_ context.Context, _ *pb.StatsRequest, _ ...grpc.CallOption) (*pb.StatsResponse, error) {
	s.calls++
	return s.sample, nil
}
func TestStatsRequiresCurrentAssignmentAndValidMeasurement(t *testing.T) {
	fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
	for _, name := range []string{"valid", "stale response", "nil response", "invalid time", "missing cpu count", "invalid cache", "invalid memory", "invalid disk", "nan cpu", "infinite cpu", "negative cpu", "stale request"} {
		t.Run(name, func(t *testing.T) {
			response := &pb.StatsResponse{Fence: proto.CloneOf(fence), CpuCount: 2, ObservedAtUnixMillis: 1234, MemoryUsedBytes: 50, MemoryTotalBytes: 100, DiskUsedBytes: 100, DiskTotalBytes: 200, CpuUsedPercent: 12.5}
			request := proto.CloneOf(fence)
			switch name {
			case "stale response":
				response.Fence.AssignmentGeneration++
			case "nil response":
				response = nil
			case "invalid time":
				response.ObservedAtUnixMillis = 0
			case "missing cpu count":
				response.CpuCount = 0
			case "invalid cache":
				response.MemoryCacheBytes = 101
			case "invalid memory":
				response.MemoryUsedBytes = 101
			case "invalid disk":
				response.DiskUsedBytes = 201
			case "nan cpu":
				response.CpuUsedPercent = float32(math.NaN())
			case "infinite cpu":
				response.CpuUsedPercent = float32(math.Inf(1))
			case "negative cpu":
				response.CpuUsedPercent = -1
			case "stale request":
				request.WorkerInstanceId = "old"
			}
			rpc := &statsStub{sample: response}
			client := &Client{RPC: rpc, InstanceID: "instance"}
			got, err := client.Stats(context.Background(), request)
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected result: %v, %v", got, err)
			}
			if name == "valid" && !proto.Equal(got, response) {
				t.Fatal("measurement changed")
			}
			if name == "stale request" && rpc.calls != 0 {
				t.Fatal("stale request reached executor")
			}
		})
	}
}
