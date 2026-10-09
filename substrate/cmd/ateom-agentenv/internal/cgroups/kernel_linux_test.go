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

package cgroups

import (
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeCgroupIsolation(t *testing.T) {
	root := os.Getenv("AENV_CGROUP_TEST_ROOT")
	if root == "" {
		if kernelRequired() {
			t.Fatal("AENV_CGROUP_TEST_ROOT must select an isolated delegated cgroup v2 test scope")
		}
		t.Skip("native cgroup isolation not executed: isolated delegated test scope missing")
	}
	if !strings.HasPrefix(root, "/sys/fs/cgroup/") || filepath.Clean(root) != root {
		t.Fatal("isolated child test scope under cgroupfs required")
	}
	m, err := Open(Config{Root: root, Ledger: t.TempDir(), CPUReserveMillis: 250, MemoryReserveMiB: 128, PidsMax: 128})
	if err != nil {
		t.Fatal(err)
	}
	fence := &pb.Fence{ActorUid: uuid.NewString(), WorkerPodUid: "test-pod", WorkerEpoch: 1, WorkerInstanceId: "test-executor", AssignmentGeneration: 1}
	path, err := m.Reserve(fence, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	group, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd())}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
		deadline := time.Now().Add(2 * time.Second)
		for {
			if e := m.Release(fence); e == nil {
				return
			} else if time.Now().After(deadline) {
				t.Error(e)
				return
			}
			time.Sleep(time.Millisecond * 10)
		}
	})
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "cgroup"))
	if err != nil || !strings.Contains(string(contents), "/"+filepath.Base(path)) {
		t.Fatal("child escaped resource group", err)
	}
	if vacant, err := empty(path); err != nil || vacant {
		t.Fatal("live group reported empty", err)
	}
	if err = m.Release(fence); err == nil {
		t.Fatal("running allocation released")
	}
	// A real read verifies that the kernel accepted the host/guest memory cap.
	bytes, err := os.ReadFile(filepath.Join(path, "memory.max"))
	if err != nil || strings.TrimSpace(string(bytes)) != "268435456" {
		t.Fatal("memory limit not applied", string(bytes), err)
	}
}
