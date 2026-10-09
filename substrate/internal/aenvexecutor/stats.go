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
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
)

// Stats is a fenced, read-only sample; it does not create a durable mutation.
func (j *Journal) Stats(ctx context.Context, fence *pb.Fence) (*pb.StatsResponse, error) {
	return j.client.Stats(ctx, fence)
}

// Inspect reads current runtime metadata without creating an operation record.
func (j *Journal) Inspect(ctx context.Context, fence *pb.Fence, operationID string) (*pb.InspectResponse, error) {
	return j.client.Inspect(ctx, fence, operationID)
}
