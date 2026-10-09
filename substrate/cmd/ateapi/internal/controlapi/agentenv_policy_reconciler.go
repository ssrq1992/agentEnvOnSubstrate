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
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"log/slog"
	"time"
)

// StartAgentENVPolicyReconciler repairs pending deliveries after an API restart.
// All replicas can scan: the existing Actor lease serializes each mutation.
// Page progress survives failed actors, so one blocked Worker cannot starve the
// rest of the namespace. Errors never turn a pending revision into an ACK.
func (s *RPCService) StartAgentENVPolicyReconciler(ctx context.Context) {
	go func() {
		token := ""
		for ctx.Err() == nil {
			read, cancel := context.WithTimeout(ctx, 10*time.Second)
			page, err := s.impl.ListActors(read, "", store.ListOptions{PageSize: 25, PageToken: token})
			cancel()
			if err != nil {
				token = ""
				slog.ErrorContext(ctx, "AgentENV policy scan failed", "error", err)
			} else {
				for _, actor := range page.Items {
					state := actor.GetStatus().GetState()
					if state != ateapipb.ActorState_ACTOR_STATE_RUNNING && state != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
						continue
					}
					if actor.GetStatus().GetAgentenvExtensionDelivery().GetPending() != nil {
						repair, cancel := context.WithTimeout(ctx, 2*time.Second)
						if e := s.actorWorkflow.reconcileAgentENVExtensions(repair, resources.ActorRefFromActor(actor)); e != nil {
							slog.DebugContext(ctx, "AgentENV extension remains pending", "actor", resources.ActorRefFromActor(actor), "error", e)
						}
						cancel()
					}
					call, stop := context.WithTimeout(ctx, 2*time.Second)
					err = s.actorWorkflow.reconcileAgentENVPolicy(call, resources.ActorRefFromActor(actor))
					stop()
					if err != nil && ctx.Err() == nil {
						slog.DebugContext(ctx, "AgentENV policy remains pending", "actor", resources.ActorRefFromActor(actor), "error", err)
					}
				}
				token = page.NextPageToken
			}
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (w *ActorWorkflow) reconcileAgentENVPolicy(ctx context.Context, ref resources.ActorRef) error {
	// Avoid a lease and template read for the ordinary backend's common path.
	policy, err := w.store.GetEgressPolicy(ctx, ref)
	if err != nil && !policyMissing(err) {
		return err
	}
	if policy.GetAgentenv() == nil {
		actor, e := w.store.GetActor(ctx, ref)
		if e != nil {
			return e
		}
		if actor.GetStatus().GetAgentenvPolicyDelivery() == nil {
			return nil
		}
	}
	ctx, lease, err := w.acquireActorLease(ctx, ref)
	if err != nil {
		return err
	}
	defer lease.Close()
	actor, err := w.store.GetActor(ctx, ref)
	if err != nil {
		return err
	}
	if err = requireAgentENVPolicyActor(ctx, w, actor); err != nil {
		return err
	}
	actor, err = w.syncPolicy(ctx, actor)
	if err != nil {
		return err
	}
	actor, err = w.applyPolicy(ctx, actor)
	if err != nil {
		return err
	}
	_, err = w.finishPolicyDelete(ctx, actor)
	return err
}
