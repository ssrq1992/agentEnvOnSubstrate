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
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"github.com/agent-substrate/substrate/internal/apierror"
	private "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

func (f *policyRuntime) ReadRuntimeInfo(_ context.Context, r *private.ReadRuntimeInfoRequest) (*private.InspectResponse, error) {
	return &private.InspectResponse{Fence: proto.CloneOf(r.Execution.Fence), State: "RUNNING", EnvdVersion: "0.9.0", CurrentExtensions: &private.ExtensionParams{Json: `{"inherited":true}`}}, nil
}
func (f *policyRuntime) ApplyExtensionParams(_ context.Context, r *private.ApplyExtensionParamsRequest) (*private.ApplyExtensionParamsResponse, error) {
	if f.fail.Load() {
		return nil, status.Error(codes.Unavailable, "unknown result")
	}
	if f.wrongAck.Load() {
		return &private.ApplyExtensionParamsResponse{Effect: private.Effect_NO_EFFECT}, nil
	}
	return &private.ApplyExtensionParamsResponse{Effect: private.Effect_COMPLETED, Approved: &private.ExtensionParams{Json: `{"approved":true}`}}, nil
}
func TestExtensionUnknownExecutionRetainsFrozenIntent(t *testing.T) {
	s, w, st, ref, f := policyFixture(t, true)
	a, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	get := &pb.GetActorExtensionParamsRequest{Actor: ref.ToObjectRef(), Uid: a.Metadata.Uid}
	initial, err := s.GetActorExtensionParams(t.Context(), get)
	if err != nil || initial.GetJson() != `{"inherited":true}` {
		t.Fatal(initial, err)
	}
	req := &pb.UpdateActorExtensionParamsRequest{Actor: ref.ToObjectRef(), Uid: a.Metadata.Uid, AssignmentGeneration: 7, OperationId: "operation-one", PatchJson: `{"desired":true}`}
	f.fail.Store(true)
	if _, err = s.UpdateActorExtensionParams(t.Context(), req); apierror.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	a, err = st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	d := a.Status.AgentenvExtensionDelivery
	if !proto.Equal(d.Pending, req) || d.Approved.Json != initial.Json || !proto.Equal(d.PendingAssignment, a.Status.WorkerAssignment) {
		t.Fatal("lost intent or prematurely approved", d)
	}
	if _, err = s.GetActorExtensionParams(t.Context(), get); apierror.Code(err) != codes.Unavailable {
		t.Fatal("unknown runtime reported approved", err)
	}
	if err = requireConfirmedExtensions(a); apierror.Code(err) != codes.Unavailable {
		t.Fatal("unknown snapshot allowed", err)
	}
	other := proto.CloneOf(req)
	other.PatchJson = `{}`
	if _, err = s.UpdateActorExtensionParams(t.Context(), other); apierror.Code(err) != codes.Aborted {
		t.Fatal("payload conflict accepted", err)
	}
	f.fail.Store(false)
	if err = w.reconcileAgentENVExtensions(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateActorExtensionParams(t.Context(), req)
	if err != nil || got.GetRevision() != 1 || got.GetJson() != `{"approved":true}` {
		t.Fatal(got, err)
	}
	a, err = st.GetActor(t.Context(), ref)
	if err != nil || a.Status.AgentenvExtensionDelivery.Pending != nil {
		t.Fatal(a, err)
	}
	if _, err = s.UpdateActorExtensionParams(t.Context(), other); apierror.Code(err) != codes.Aborted {
		t.Fatal("completed operation altered", err)
	}
}
func TestExtensionNoEffectDoesNotAdvanceRevision(t *testing.T) {
	s, _, st, ref, f := policyFixture(t, true)
	a, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.UpdateActorExtensionParamsRequest{Actor: ref.ToObjectRef(), Uid: a.Metadata.Uid, AssignmentGeneration: 7, OperationId: "rejected-op", PatchJson: `{}`}
	f.wrongAck.Store(true)
	for i := 0; i < 2; i++ {
		if _, err = s.UpdateActorExtensionParams(t.Context(), req); apierror.Code(err) != codes.FailedPrecondition {
			t.Fatal(err)
		}
	}
	a, err = st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	d := a.Status.AgentenvExtensionDelivery
	if d.Pending != nil || !d.LastRejected || d.Approved.Revision != 0 || d.Approved.Json != `{"inherited":true}` {
		t.Fatal(d)
	}
	f.wrongAck.Store(false)
	req.OperationId = "next-op"
	if _, err = s.UpdateActorExtensionParams(t.Context(), req); err != nil {
		t.Fatal("known rejection blocked next operation", err)
	}
}
func TestExtensionRejectsInvalidPatchBeforePersisting(t *testing.T) {
	s, _, st, ref, _ := policyFixture(t, true)
	a, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.UpdateActorExtensionParamsRequest{Actor: ref.ToObjectRef(), Uid: a.Metadata.Uid, AssignmentGeneration: 7, OperationId: "invalid", PatchJson: `null`}
	if _, err = s.UpdateActorExtensionParams(t.Context(), req); apierror.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	after, err := st.GetActor(t.Context(), ref)
	if err != nil || !proto.Equal(a, after) {
		t.Fatal("bad patch persisted", err)
	}
}

func TestExtensionPendingNeverRetargetsNewAllocation(t *testing.T) {
	s, w, st, ref, f := policyFixture(t, true)
	a, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.UpdateActorExtensionParamsRequest{Actor: ref.ToObjectRef(), Uid: a.Metadata.Uid, AssignmentGeneration: 7, OperationId: "old-allocation", PatchJson: `{}`}
	f.fail.Store(true)
	if _, err = s.UpdateActorExtensionParams(t.Context(), req); apierror.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	a, err = st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateActor(t.Context(), ref, store.PreconditionFrom(a), func(a *pb.Actor) error { a.Status.WorkerAssignment.AssignmentGeneration = 8; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.fail.Store(false)
	if err = w.reconcileAgentENVExtensions(t.Context(), ref); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("pending update retargeted", err)
	}
	a, err = st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status.AgentenvExtensionDelivery.PendingAssignment.AssignmentGeneration != 7 || a.Status.AgentenvExtensionDelivery.Pending == nil {
		t.Fatal("old intent discarded", a)
	}
}
