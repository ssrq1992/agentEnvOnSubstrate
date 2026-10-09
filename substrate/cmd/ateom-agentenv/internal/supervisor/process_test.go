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

package supervisor

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

func TestExecutorHelper(t *testing.T) {
	if os.Getenv("AENV_SUPERVISOR_HELPER") != "exit" {
		return
	}
	os.Exit(7)
}
func TestExecutorFailureIsObservedWithoutRestart(t *testing.T) {
	t.Setenv("AENV_SUPERVISOR_HELPER", "exit")
	p, err := Start(os.Args[0], []string{"-test.run=TestExecutorHelper"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("executor exit was not observed")
	}
	if p.Err() == nil {
		t.Fatal("failure was hidden")
	}
	if err := p.Stop(context.Background()); err == nil {
		t.Fatal("failed exit reported graceful")
	}
}
func TestMissingExecutorIsNotStarted(t *testing.T) {
	if _, err := Start("/missing/aenv-executor", nil, io.Discard); err == nil {
		t.Fatal("missing binary accepted")
	}
}
