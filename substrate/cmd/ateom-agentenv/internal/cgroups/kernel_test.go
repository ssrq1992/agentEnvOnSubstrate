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

package cgroups

import (
	"os"
	"testing"
)

func kernelRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("AENV_REQUIRE_CGROUP_TESTS") == "true"
}
func TestKernelRequirement(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("AENV_REQUIRE_CGROUP_TESTS", "")
	if kernelRequired() {
		t.Fatal("local optional branch")
	}
	t.Setenv("CI", "true")
	if !kernelRequired() {
		t.Fatal("CI must require kernel isolation tests")
	}
	t.Setenv("CI", "")
	t.Setenv("AENV_REQUIRE_CGROUP_TESTS", "true")
	if !kernelRequired() {
		t.Fatal("explicit kernel tests must be required")
	}
}
