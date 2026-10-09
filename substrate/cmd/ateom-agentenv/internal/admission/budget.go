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

// Package admission computes guest capacity after reserving Worker overhead.
package admission

import (
	"fmt"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/resources"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/api/resource"
	"strconv"
)

type Reserves struct{ WorkerMilliCPU, ActorMilliCPU, WorkerMemoryMiB, ActorMemoryMiB int64 }

func Compute(reported *ateletpb.WorkerResources, actors int, reserve Reserves) (*ateletpb.WorkerResources, uint64, uint64, error) {
	if actors < 1 || actors > 16384 {
		return nil, 0, 0, fmt.Errorf("max-actors must be between 1 and 16384")
	}
	for _, value := range []int64{reserve.WorkerMilliCPU, reserve.ActorMilliCPU, reserve.WorkerMemoryMiB, reserve.ActorMemoryMiB} {
		if value < 0 || value > 1<<30 {
			return nil, 0, 0, fmt.Errorf("invalid bounded runtime reserve")
		}
	}
	var cpus, mem int64
	seen := map[string]bool{}
	for _, limit := range reported.GetResources().GetLimits() {
		if seen[limit.Name] {
			return nil, 0, 0, fmt.Errorf("duplicate resource limit")
		}
		seen[limit.Name] = true
		q, err := resource.ParseQuantity(limit.Quantity)
		if err != nil || q.Sign() < 0 {
			return nil, 0, 0, fmt.Errorf("invalid Worker resource limit")
		}
		switch limit.Name {
		case resources.ResourceCPU:
			if q.Cmp(resource.MustParse("1000000")) > 0 {
				return nil, 0, 0, fmt.Errorf("Worker CPU limit exceeds supported range")
			}
			cpus = (q.MilliValue() - reserve.WorkerMilliCPU - int64(actors)*reserve.ActorMilliCPU) / 1000
		case resources.ResourceMemory:
			if q.Cmp(resource.MustParse("1Pi")) > 0 {
				return nil, 0, 0, fmt.Errorf("Worker memory limit exceeds supported range")
			}
			mem = q.Value()/(1024*1024) - reserve.WorkerMemoryMiB - int64(actors)*reserve.ActorMemoryMiB
		}
	}
	if cpus < 1 || mem < 128 {
		return nil, 0, 0, fmt.Errorf("Worker limits must leave at least one vCPU and 128 MiB after runtime reserves")
	}
	result := proto.CloneOf(reported)
	result.Resources = &ateletpb.Resources{Limits: []*ateletpb.Limits{{Name: resources.ResourceCPU, Quantity: strconv.FormatInt(cpus, 10)}, {Name: resources.ResourceMemory, Quantity: strconv.FormatInt(mem, 10) + "Mi"}}}
	return result, uint64(cpus), uint64(mem), nil
}
