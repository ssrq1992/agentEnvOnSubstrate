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

package adapter

import (
	"context"
	"errors"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestAdapterLimitsBeforeExecuteAndRetainsUnknownReservation(t *testing.T) {
	s, o, n, _, req := serviceFixture(t)
	l := s.Cgroups.(*limits)
	l.err = errors.New("controller missing")
	if _, err := s.RunWorkload(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if len(o.calls) != 0 || n.attached != 0 {
		t.Fatal("VM/network prepared before resource limits")
	}
	l.err = nil
	o.err = &aenvexecutor.EffectUnknown{OperationID: "start", Cause: errors.New("unknown")}
	if _, err := s.RunWorkload(context.Background(), req); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if l.released != 0 || o.calls[0].GetStart().GetCgroupPath() != "/sys/fs/cgroup/aenv-actor-7" {
		t.Fatal("unknown execution released or omitted isolation")
	}
	stop := &ateompb.TerminateWorkloadRequest{ActorUid: req.ActorUid, Execution: req.Execution}
	if _, err := s.TerminateWorkload(context.Background(), stop); status.Code(err) != codes.Unavailable || l.released != 0 {
		t.Fatal("unknown stop released limits", err)
	}
	o.err = nil
	l.err = errors.New("populated")
	if _, err := s.TerminateWorkload(context.Background(), stop); status.Code(err) != codes.Unavailable || n.detached != 0 {
		t.Fatal("populated group release acknowledged", err)
	}
	l.err = nil
	if _, err := s.TerminateWorkload(context.Background(), stop); err != nil || n.detached != 1 {
		t.Fatal("known stop did not release isolation", err)
	}
}
