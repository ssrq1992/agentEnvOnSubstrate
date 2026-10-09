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
	"errors"
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store"
	"testing"

	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type uncertainActorStore struct {
	crashActorStore
	actor *ateapipb.Actor
}

func (s uncertainActorStore) GetActor(context.Context, resources.ActorRef) (*ateapipb.Actor, error) {
	return s.actor, nil
}
func TestAgentENVUnknownExecutionCannotBeCrashedAndReleased(t *testing.T) {
	actor := &ateapipb.Actor{Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RESUMING, WorkerAssignment: &ateapipb.WorkerAssignment{ExecutorInstanceId: "executor", AssignmentGeneration: 7, WorkerEpoch: 2}}}
	before := proto.CloneOf(actor)
	// All mutation methods are intentionally absent: calling one would panic.
	err := crashActor(t.Context(), uncertainActorStore{actor: actor}, resources.ActorRef{Atespace: "space", Name: "actor"}, "resume", "RPC failed")
	if apierror.Code(err) != codes.Unavailable {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proto.Equal(actor, before) {
		t.Fatal("unknown execution lost assignment")
	}
}
func TestAgentENVEpochIsNotPhysicalFence(t *testing.T) {
	for _, class := range []string{"agentenv", ""} {
		t.Run(class, func(t *testing.T) {
			worker := &ateapipb.Worker{SandboxClass: class}
			claim := &ateapipb.ActorAssignment{ExecutorInstanceId: "executor", WorkerEpoch: 1}
			if err := (&WorkerWorkflow{}).releaseBoundActor(t.Context(), worker, claim); apierror.Code(err) != codes.Unavailable {
				t.Fatalf("Worker deletion erased unconfirmed assignment: %v", err)
			}
			if err := (&WorkerWorkflow{}).releaseEarlierAssignment(t.Context(), worker, claim, 2); err == nil {
				t.Fatal("epoch change released unconfirmed runtime")
			}
		})
	}
}

func TestAgentENVDatabaseBindingRequiresCurrentExecutorAndPreservesOldClaim(t *testing.T) {
	ctx := t.Context()
	persistence := newTestPersistence(t)
	name := testWorkerUID("pod-1")
	worker, err := persistence.CreateWorker(ctx, &ateapipb.Worker{Metadata: &ateapipb.ResourceMetadata{Name: name}, SandboxClass: "agentenv", WorkerPodUid: name, Epoch: 3, Status: &ateapipb.WorkerStatus{State: ateapipb.WorkerState_WORKER_STATE_ACTIVE, ObservedEpoch: 2, RegisteredEpoch: 3, ExecutorInstanceId: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	assignment := &ateapipb.ActorAssignment{ActorUid: "actor-uid"}
	if err = persistence.BindActorToWorker(ctx, name, assignment, nil); !errors.Is(err, store.ErrVersionConflict) {
		t.Fatal("unreconciled epoch accepted", err)
	}
	worker, err = persistence.UpdateWorker(ctx, name, store.PreconditionFrom(worker), func(w *ateapipb.Worker) error { w.Status.ObservedEpoch = 3; w.Status.RegisteredEpoch = 2; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = persistence.BindActorToWorker(ctx, name, assignment, nil); !errors.Is(err, store.ErrVersionConflict) {
		t.Fatal("unregistered epoch accepted", err)
	}
	worker, err = persistence.UpdateWorker(ctx, name, store.PreconditionFrom(worker), func(w *ateapipb.Worker) error { w.Status.RegisteredEpoch = 3; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = persistence.BindActorToWorker(ctx, name, assignment, nil); err != nil {
		t.Fatal(err)
	}
	generation := assignment.AssignmentGeneration
	if generation == 0 || assignment.ExecutorInstanceId != "one" {
		t.Fatal("missing assigned fence")
	}
	worker, err = persistence.GetWorker(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = persistence.UpdateWorker(ctx, name, store.PreconditionFrom(worker), func(w *ateapipb.Worker) error {
		w.Epoch = 4
		w.Status.ObservedEpoch = 4
		w.Status.RegisteredEpoch = 4
		w.Status.ExecutorInstanceId = "two"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = persistence.BindActorToWorker(ctx, name, assignment, nil); !errors.Is(err, store.ErrVersionConflict) {
		t.Fatal("old live claim rebound", err)
	}
	stored, err := persistence.GetWorkerAssignment(ctx, name, "actor-uid")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AssignmentGeneration != generation || stored.WorkerEpoch != 3 || stored.ExecutorInstanceId != "one" {
		t.Fatal("old assignment overwritten")
	}
}
