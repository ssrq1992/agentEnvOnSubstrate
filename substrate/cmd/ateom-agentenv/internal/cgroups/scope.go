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
package cgroups

import (
	"fmt"
	"path/filepath"
	"strings"
)

func ScopePath(mount, contents string) (string, error) {
	if !filepath.IsAbs(mount) {
		return "", fmt.Errorf("absolute cgroup mount required")
	}
	for _, line := range strings.Split(strings.TrimSpace(contents), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\\") {
				return "", fmt.Errorf("invalid unified cgroup scope")
			}
			return filepath.Join(mount, strings.TrimPrefix(path, "/")), nil
		}
	}
	return "", fmt.Errorf("cgroup v2 Worker scope required")
}
