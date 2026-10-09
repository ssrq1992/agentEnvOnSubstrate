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
	"fmt"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"strings"
)

// ValidatePolicyUpdate bounds the private transport envelope. The executor
// parses IP/CIDR/domain rules using the same implementation as standalone.
func ValidatePolicyUpdate(req *pb.ApplyNetworkPolicyRequest) error {
	if err := ValidateOperation(req.GetExecution(), req.GetActorUid(), req.GetTargetWorkerPodUid()); err != nil {
		return err
	}
	p := req.GetPolicy()
	if p == nil || p.Revision == 0 {
		return fmt.Errorf("nonzero policy revision required")
	}
	if _, ok := pb.NetworkPolicy_Base_name[int32(p.Base)]; !ok {
		return fmt.Errorf("unknown network policy base")
	}
	if len(p.AllowOut)+len(p.DenyOut) > 1024 {
		return fmt.Errorf("network policy exceeds rule limit")
	}
	for _, list := range [][]string{p.AllowOut, p.DenyOut} {
		for _, entry := range list {
			if entry == "" || len(entry) > 253 || strings.ContainsAny(entry, "\x00\r\n") {
				return fmt.Errorf("invalid policy entry length or control character")
			}
		}
	}
	return nil
}
