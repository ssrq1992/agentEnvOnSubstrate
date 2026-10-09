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
	"encoding/json"
	"fmt"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
)

func ValidateExtensionParams(params *pb.ExtensionParams) error {
	if params == nil || len(params.Json) == 0 || len(params.Json) > 65536 {
		return fmt.Errorf("extension JSON object required within size limit")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(params.Json), &value); err != nil || value == nil {
		return fmt.Errorf("invalid extension JSON object")
	}
	return nil
}
func ValidateExtensionUpdate(req *pb.ApplyExtensionParamsRequest) error {
	if err := ValidateOperation(req.GetExecution(), req.GetActorUid(), req.GetTargetWorkerPodUid()); err != nil {
		return err
	}
	return ValidateExtensionParams(req.GetPatch())
}
