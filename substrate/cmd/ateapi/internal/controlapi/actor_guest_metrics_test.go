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

package controlapi

import (
	"context"
	"github.com/agent-substrate/substrate/internal/apierror"
	private "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"testing"
)

func (f *policyRuntime) ReadGuestStats(_ context.Context, r *private.ReadGuestStatsRequest) (*private.StatsResponse, error) {
	fence := proto.CloneOf(r.Execution.Fence)
	if f.wrongAck.Load() {
		fence.AssignmentGeneration++
	}
	return &private.StatsResponse{Fence: fence, CpuCount: 2, ObservedAtUnixMillis: 1234, MemoryUsedBytes: 50, MemoryTotalBytes: 100, MemoryCacheBytes: 10, CpuUsedPercent: 12.5, DiskUsedBytes: 30, DiskTotalBytes: 100}, nil
}
func TestGuestMetricsChecksIncarnationAndSampleIdentity(t *testing.T) {
	s, _, st, ref, f := policyFixture(t, true)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.GetActorGuestMetricsRequest{Actor: ref.ToObjectRef(), Uid: actor.Metadata.Uid}
	got, err := s.GetActorGuestMetrics(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActorUid != req.Uid || got.MemoryUsedBytes != 50 || got.CpuUsedPercent != 12.5 || got.ObservedAtUnixMillis != 1234 || got.Assignment.AssignmentGeneration != 7 {
		t.Fatal("measurement units or attribution changed", got)
	}
	f.wrongAck.Store(true)
	if _, err = s.GetActorGuestMetrics(t.Context(), req); apierror.Code(err) != codes.Unavailable {
		t.Fatal("wrong allocation sample accepted", err)
	}
	req.Uid = "old-uid"
	if _, err = s.GetActorGuestMetrics(t.Context(), req); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale Actor metrics accepted", err)
	}
}
func TestGuestMetricsDoesNotResumeSuspendedActor(t *testing.T) {
	s, _, st, ref, _ := policyFixture(t, false)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetActorGuestMetrics(t.Context(), &pb.GetActorGuestMetricsRequest{Actor: ref.ToObjectRef(), Uid: actor.Metadata.Uid}); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("suspended metrics accepted", err)
	}
	after, err := st.GetActor(t.Context(), ref)
	if err != nil || !proto.Equal(actor, after) {
		t.Fatal("metrics mutated lifecycle", err)
	}
}
