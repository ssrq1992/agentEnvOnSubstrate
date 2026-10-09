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

package admission

import (
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestBudgetReservesEveryActor(t *testing.T) {
	source := &ateletpb.WorkerResources{Resources: &ateletpb.Resources{Limits: []*ateletpb.Limits{{Name: "cpu", Quantity: "8"}, {Name: "memory", Quantity: "8Gi"}}}}
	before := proto.CloneOf(source)
	reserve := Reserves{WorkerMilliCPU: 250, ActorMilliCPU: 250, WorkerMemoryMiB: 256, ActorMemoryMiB: 128}
	_, cpu, mem, err := Compute(source, 4, reserve)
	if err != nil || cpu != 6 || mem != 7424 {
		t.Fatalf("wrong admitted budget %d %d %v", cpu, mem, err)
	}
	if !proto.Equal(source, before) {
		t.Fatal("input capacity mutated")
	}
	if _, _, _, err := Compute(source, 100, reserve); err == nil {
		t.Fatal("overhead exceeding limit accepted")
	}
	if _, _, _, err := Compute(source, 0, reserve); err == nil {
		t.Fatal("zero actor budget accepted")
	}
	if _, _, _, err := Compute(source, 1, Reserves{ActorMemoryMiB: -1}); err == nil {
		t.Fatal("negative reserve accepted")
	}
	if _, _, _, err := Compute(nil, 1, reserve); err == nil {
		t.Fatal("missing limits accepted")
	}
	source.Resources.Limits = append(source.Resources.Limits, source.Resources.Limits[0])
	if _, _, _, err := Compute(source, 1, reserve); err == nil {
		t.Fatal("duplicate limit accepted")
	}
}
