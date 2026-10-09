//go:build linux

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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// DelegateCurrent scopes all changes to the current container's cgroup, even
// when a privileged container inherits the host cgroup namespace.
func DelegateCurrent(ctx context.Context) (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	scope, err := ScopePath("/sys/fs/cgroup", string(data))
	if err != nil {
		return "", err
	}
	procs, err := os.ReadFile(filepath.Join(scope, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	if !contains(string(procs), strconv.Itoa(os.Getpid())) {
		return "", fmt.Errorf("resolved cgroup does not contain Worker process")
	}
	// A Worker must have finite container memory and CPU limits. This also
	// refuses the real host root when launched outside a Worker container.
	for _, name := range []string{"memory.max", "cpu.max"} {
		b, e := os.ReadFile(filepath.Join(scope, name))
		if e != nil {
			return "", e
		}
		fields := strings.Fields(string(b))
		if len(fields) == 0 {
			return "", fmt.Errorf("empty Worker cgroup limit")
		}
		if n, e := strconv.ParseUint(fields[0], 10, 64); e != nil || n == 0 {
			return "", fmt.Errorf("finite Worker %s required", name)
		}
	}
	if err = unix.Mount("none", "/sys/fs/cgroup", "", unix.MS_BIND|unix.MS_REMOUNT, ""); err != nil {
		return "", fmt.Errorf("cgroup writable remount: %w", err)
	}
	leaf := filepath.Join(scope, "aenv-supervisor")
	if err = os.Mkdir(leaf, 0755); err != nil && !os.IsExist(err) {
		return "", err
	}
	drained := false
	for attempt := 0; attempt < 100; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		b, e := os.ReadFile(filepath.Join(scope, "cgroup.procs"))
		if e != nil {
			return "", e
		}
		pids := strings.Fields(string(b))
		if len(pids) == 0 {
			drained = true
			break
		}
		for _, pid := range pids {
			if e = existingWrite(filepath.Join(leaf, "cgroup.procs"), pid); e != nil && !errors.Is(e, unix.ESRCH) {
				return "", e
			}
		}
	}
	if !drained {
		return "", fmt.Errorf("Worker cgroup did not drain")
	}
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if err = existingWrite(filepath.Join(scope, "cgroup.subtree_control"), "+"+controller); err != nil {
			return "", err
		}
	}
	return scope, nil
}
