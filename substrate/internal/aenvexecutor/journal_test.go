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

package aenvexecutor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func journalFixture(t *testing.T) (*Journal, *executorStub, *pb.Fence, *pb.Command, context.Context) {
	t.Helper()
	rpc := &executorStub{effect: pb.Effect_COMPLETED}
	client, err := Handshake(context.Background(), rpc)
	if err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "journal"), client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	fence := &pb.Fence{ProtocolVersion: ProtocolVersion, ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
	return j, rpc, fence, &pb.Command{Action: &pb.Command_Stop{Stop: &pb.StopSpec{}}}, ctx
}

func TestJournalRestartRetainsCompletion(t *testing.T) {
	j, rpc, fence, command, ctx := journalFixture(t)
	if _, err := OpenJournal(j.root, j.client); err == nil {
		t.Fatal("concurrent journal owner accepted")
	}
	if _, err := j.Execute(ctx, fence, "op", command); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(j.root, j.client)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Execute(ctx, fence, "op", command); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Resolve(ctx, fence, "op"); err != nil {
		t.Fatal(err)
	}
	if len(rpc.requests) != 1 {
		t.Fatal("completed operation sent again")
	}
	changed := &pb.Command{Action: &pb.Command_Capture{Capture: &pb.CaptureSpec{}}}
	if _, err := reopened.Execute(ctx, fence, "op", changed); err == nil {
		t.Fatal("payload replacement accepted")
	}
	stale := proto.Clone(fence).(*pb.Fence)
	stale.WorkerEpoch++
	if _, err := reopened.Execute(ctx, stale, "op", command); err == nil {
		t.Fatal("fence replacement accepted")
	}
	if len(rpc.requests) != 1 {
		t.Fatal("conflicting operation sent")
	}
}

func TestJournalTransportFailureKeepsOriginalIntent(t *testing.T) {
	j, rpc, fence, command, ctx := journalFixture(t)
	rpc.err = errors.New("connection lost after request")
	_, err := j.Execute(ctx, fence, "op", command)
	var unknown *EffectUnknown
	if !errors.As(err, &unknown) || len(rpc.requests) != 1 {
		t.Fatalf("expected unknown without automatic retry: %v", err)
	}
	rpc.err = nil
	if _, err := j.Execute(ctx, fence, "op", command); err != nil {
		t.Fatal(err)
	}
	if len(rpc.requests) != 2 || !proto.Equal(rpc.requests[0], rpc.requests[1]) {
		t.Fatal("explicit retry changed identity or payload")
	}
}

func TestJournalWriteFailuresPreserveEffectClassification(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "intent", 2: "result"}[failAt], func(t *testing.T) {
			j, rpc, fence, command, ctx := journalFixture(t)
			writes := 0
			j.write = func(path string, data []byte) error {
				writes++
				if writes == failAt {
					return errors.New("disk full")
				}
				return writeReceipt(path, data)
			}
			_, err := j.Execute(ctx, fence, "op", command)
			var unknown *EffectUnknown
			if err == nil || errors.As(err, &unknown) != (failAt == 2) || len(rpc.requests) != failAt-1 {
				t.Fatalf("incorrect effect classification: calls=%d err=%v", len(rpc.requests), err)
			}
		})
	}
}

func TestJournalRejectsUnsafeReceipt(t *testing.T) {
	j, rpc, fence, command, ctx := journalFixture(t)
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(j.root, receiptKey(fence, "op"))); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Execute(ctx, fence, "op", command); err == nil {
		t.Fatal("symlink receipt accepted")
	}
	if len(rpc.requests) != 0 {
		t.Fatal("unsafe receipt reached executor")
	}
}

type inspectingExecutor struct {
	*executorStub
	observed    *pb.OperationResponse
	inspections int
}

func (s *inspectingExecutor) Inspect(_ context.Context, req *pb.InspectRequest, _ ...grpc.CallOption) (*pb.InspectResponse, error) {
	s.inspections++
	return &pb.InspectResponse{Fence: req.Fence, Operation: s.observed}, nil
}
func TestJournalResolveUnknownWithoutRepeatingMutation(t *testing.T) {
	j, rpc, fence, command, ctx := journalFixture(t)
	inspecting := &inspectingExecutor{executorStub: rpc}
	j.client.RPC = inspecting
	rpc.err = errors.New("lost response")
	if _, err := j.Execute(ctx, fence, "op", command); err == nil {
		t.Fatal("expected unknown result")
	}
	var unknown *EffectUnknown
	if _, err := j.Resolve(ctx, fence, "op"); !errors.As(err, &unknown) {
		t.Fatalf("missing observation is not proof of no effect: %v", err)
	}
	inspecting.observed = &pb.OperationResponse{OperationId: "op", Effect: pb.Effect_COMPLETED}
	if _, err := j.Resolve(ctx, fence, "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Execute(ctx, fence, "op", command); err != nil {
		t.Fatal(err)
	}
	if len(rpc.requests) != 1 || inspecting.inspections != 2 {
		t.Fatal("resolution repeated mutation or discarded completion")
	}
}
func TestJournalResolveRejectsCorruptPayload(t *testing.T) {
	j, rpc, fence, command, ctx := journalFixture(t)
	rpc.err = errors.New("lost response")
	j.Execute(ctx, fence, "op", command)
	path := filepath.Join(j.root, receiptKey(fence, "op"))
	record, err := readReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	record.Command = []byte("corrupt")
	if err := j.save(path, record); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Resolve(ctx, fence, "op"); err == nil {
		t.Fatal("corrupt payload accepted")
	}
}
