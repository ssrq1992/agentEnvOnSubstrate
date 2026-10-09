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
	"github.com/agent-substrate/substrate/cmd/ateapi/internal/store/storetest"
	"github.com/agent-substrate/substrate/internal/apierror"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"testing"
)

type incarnationChangingStore struct {
	store.Interface
	reads int
}

func (s *incarnationChangingStore) GetActor(ctx context.Context, ref resources.ActorRef) (*ateapipb.Actor, error) {
	a, err := s.Interface.GetActor(ctx, ref)
	s.reads++
	if err == nil && s.reads > 1 {
		a = proto.Clone(a).(*ateapipb.Actor)
		a.Metadata.Uid = "replacement"
	}
	return a, err
}

func TestLifecycleUIDPreconditions(t *testing.T) {
	for _, state := range []ateapipb.ActorState{ateapipb.ActorState_ACTOR_STATE_RUNNING, ateapipb.ActorState_ACTOR_STATE_SUSPENDED} {
		t.Run(state.String(), func(t *testing.T) {
			ctx := t.Context()
			st, cleanup := storetest.SetupTestStore(t)
			defer cleanup()
			w := newTestActorWorkflow(t, st, "ns", "tmpl1")
			ref := resources.ActorRef{Atespace: "team-a", Name: "id1"}
			seedWorkflowActor(t, ctx, st, ref, "ns", "tmpl1", state)
			before, err := st.GetActor(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = w.ResumeActor(ctx, ref, "stale-uid", nil); apierror.Code(err) != codes.FailedPrecondition {
				t.Fatalf("resume accepted stale UID: %v", err)
			}
			if _, err = w.SuspendActor(ctx, ref, "stale-uid", nil); apierror.Code(err) != codes.FailedPrecondition {
				t.Fatalf("suspend accepted stale UID: %v", err)
			}
			after, err := st.GetActor(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(before, after) {
				t.Fatal("stale UID mutated Actor")
			}
			if state == ateapipb.ActorState_ACTOR_STATE_RUNNING {
				if _, resumed, err := w.ResumeActor(ctx, ref, before.Metadata.Uid, nil); err != nil || resumed {
					t.Fatalf("current UID running fast path: %v", err)
				}
			} else {
				if _, err := w.SuspendActor(ctx, ref, before.Metadata.Uid, nil); err != nil {
					t.Fatal(err)
				}
				changed := &incarnationChangingStore{Interface: st}
				w.store = changed
				if _, _, err := w.ResumeActor(ctx, ref, before.Metadata.Uid, nil); apierror.Code(err) != codes.FailedPrecondition {
					t.Fatalf("UID was not rechecked after acquiring lease: %v", err)
				}
				if changed.reads != 2 {
					t.Fatalf("reads=%d", changed.reads)
				}
			}
		})
	}
}

func TestSuspendRejectsPreviousAllocationBeforeMutation(t *testing.T) {
	_, w, st, ref, _ := policyFixture(t, true)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []uint64{0, 6, 8} {
		if _, err = w.SuspendActor(t.Context(), ref, actor.Metadata.Uid, &expected); apierror.Code(err) != codes.FailedPrecondition {
			t.Fatalf("generation %d accepted: %v", expected, err)
		}
	}
	current := uint64(7)
	if _, err = w.SuspendActor(t.Context(), ref, "", &current); apierror.Code(err) != codes.InvalidArgument {
		t.Fatal("generation without Actor UID accepted", err)
	}
	after, err := st.GetActor(t.Context(), ref)
	if err != nil || !proto.Equal(actor, after) {
		t.Fatal("stale suspend mutated current allocation", err)
	}
	_, w, st, ref, _ = policyFixture(t, false)
	actor, err = st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.SuspendActor(t.Context(), ref, actor.Metadata.Uid, &current); err != nil {
		t.Fatal("completed suspend retry was not idempotent", err)
	}
}

func TestResumeRejectsChangedSnapshotBeforeMutation(t *testing.T) {
	for _, running := range []bool{false, true} {
		_, w, st, ref, _ := policyFixture(t, running)
		actor, err := st.GetActor(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		oldSource := "s3://bucket/old"
		if _, _, err = w.ResumeActor(t.Context(), ref, actor.Metadata.Uid, &oldSource); apierror.Code(err) != codes.FailedPrecondition {
			t.Fatal("stale resume source accepted", err)
		}
		empty := ""
		if _, _, err = w.ResumeActor(t.Context(), ref, "", &empty); apierror.Code(err) != codes.InvalidArgument {
			t.Fatal("source without UID accepted", err)
		}
		after, err := st.GetActor(t.Context(), ref)
		if err != nil || !proto.Equal(actor, after) {
			t.Fatal("stale resume changed Actor", err)
		}
		if running {
			if _, resumed, err := w.ResumeActor(t.Context(), ref, actor.Metadata.Uid, &empty); err != nil || resumed {
				t.Fatal("current running source was not idempotent", err)
			}
		}
	}
}

func TestSourceFencedResumeRejectsInProgressSuspend(t *testing.T) {
	_, w, st, ref, _ := policyFixture(t, true)
	actor, err := st.GetActor(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	actor, err = st.UpdateActor(t.Context(), ref, store.PreconditionFrom(actor), func(a *ateapipb.Actor) error { a.Status.State = ateapipb.ActorState_ACTOR_STATE_SUSPENDING; return nil })
	if err != nil {
		t.Fatal(err)
	}
	source := ""
	if _, _, err = w.ResumeActor(t.Context(), ref, actor.Metadata.Uid, &source); apierror.Code(err) != codes.FailedPrecondition {
		t.Fatal("source check allowed resume during suspend", err)
	}
	after, err := st.GetActor(t.Context(), ref)
	if err != nil || !proto.Equal(actor, after) {
		t.Fatal("in-progress suspend was mutated", err)
	}
}
