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
	"encoding/json"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (*Manager, *pb.Fence) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("cpu memory pids"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: root, Ledger: t.TempDir(), CPUReserveMillis: 250, MemoryReserveMiB: 128, PidsMax: 4096}
	m, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.makeGroup = func(path string) error {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		for _, name := range []string{"cpu.max", "memory.max", "memory.swap.max", "memory.oom.group", "pids.max", "cgroup.events"} {
			if err := os.WriteFile(filepath.Join(path, name), []byte("populated 0\n"), 0600); err != nil {
				return err
			}
		}
		return nil
	}
	m.removeGroup = os.RemoveAll // Ordinary files emulate kernel-created interfaces.
	return m, &pb.Fence{ActorUid: "01900000-0000-7000-8000-000000000001", WorkerPodUid: "pod", WorkerEpoch: 2, WorkerInstanceId: "instance", AssignmentGeneration: 7}
}
func TestLimitsIncludeReservedHostOverheadAndSurviveRestart(t *testing.T) {
	m, f := fixture(t)
	path, err := m.Reserve(f, 2, 512)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"cpu.max": "225000 100000", "memory.max": "671088640", "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "4096"} {
		got, err := os.ReadFile(filepath.Join(path, name))
		if err != nil || string(got) != want {
			t.Fatal(name, string(got), err)
		}
	}
	restarted, err := Open(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.Reserve(f, 2, 512); err != nil || retry != path {
		t.Fatal("reservation lost", err)
	}
	epoch, err := m.Epoch(f)
	if err != nil || epoch <= 0 {
		t.Fatal("missing accounting epoch", err)
	}
	if after, err := restarted.Epoch(f); err != nil || after != epoch {
		t.Fatal("restart changed accounting epoch", err)
	}
	if _, err := restarted.Reserve(f, 2, 513); err == nil {
		t.Fatal("reshaped live memory")
	}
	stale := proto.CloneOf(f)
	stale.WorkerInstanceId = "new"
	if _, err := restarted.Reserve(stale, 2, 512); err == nil {
		t.Fatal("adopted old allocation")
	}
	if err := m.Release(stale); err == nil {
		t.Fatal("stale release")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("stale release removed group")
	}
}
func TestPopulationAndMissingEvidencePreventRelease(t *testing.T) {
	m, f := fixture(t)
	path, err := m.Reserve(f, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	for _, events := range []string{"populated 1\n", "frozen 0\n"} {
		if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte(events), 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.Release(f); err == nil {
			t.Fatal("released unknown/nonempty group")
		}
		_, ledger, _ := m.paths(f)
		if _, err := os.Stat(ledger); err != nil {
			t.Fatal("lost allocation", err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(f); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(f); err != nil {
		t.Fatal("non-idempotent release", err)
	}
}
func TestPartialConfigurationDoesNotBecomeReady(t *testing.T) {
	m, f := fixture(t)
	makeGroup := m.makeGroup
	m.makeGroup = func(path string) error {
		if err := makeGroup(path); err != nil {
			return err
		}
		return os.Remove(filepath.Join(path, "memory.swap.max"))
	}
	if _, err := m.Reserve(f, 1, 128); err == nil {
		t.Fatal("missing controller accepted")
	}
	path, ledger, _ := m.paths(f)
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err = json.Unmarshal(data, &r); err != nil || r.Ready {
		t.Fatal("partial allocation marked ready", err)
	}
	if _, err = m.Reserve(f, 1, 128); err == nil {
		t.Fatal("retry skipped missing limits")
	}
	if err = os.WriteFile(filepath.Join(path, "memory.swap.max"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Reserve(f, 1, 128); err != nil {
		t.Fatal("retry did not complete configuration", err)
	}
	if err = os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Reserve(f, 1, 128); err == nil {
		t.Fatal("lost prepared allocation recreated")
	}
}
func TestStoppedReconcileKeepsLiveOrUntrackedGroups(t *testing.T) {
	m, f := fixture(t)
	path, err := m.Reserve(f, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileStopped(); err == nil {
		t.Fatal("adopted surviving VMM")
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileStopped(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(m.cfg.Root, "aenv-untracked"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileStopped(); err == nil {
		t.Fatal("untracked group ignored")
	}
}
func TestScopeNeverTraversesOutsideWorker(t *testing.T) {
	for input, want := range map[string]string{"0::/": "/sys/fs/cgroup", "0::/kubepods.slice/container.scope": "/sys/fs/cgroup/kubepods.slice/container.scope"} {
		got, err := ScopePath("/sys/fs/cgroup", input)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	for _, input := range []string{"1:cpu:/group", "0::/../host", "0::relative", "0::/group/../host", "0::/group\\bad", "0::/bad\x00"} {
		if _, err := ScopePath("/sys/fs/cgroup", input); err == nil {
			t.Fatal("unsafe scope", input)
		}
	}
	m, f := fixture(t)
	f.ActorUid = "../host"
	if _, err := m.Reserve(f, 1, 128); err == nil {
		t.Fatal("unsafe Actor name")
	}
	if err := os.WriteFile(filepath.Join(m.cfg.Root, "cgroup.subtree_control"), []byte("cpu pids"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(m.cfg); err == nil {
		t.Fatal("missing memory isolation accepted")
	}
}

func TestReserveNeverAdoptsAnUntrackedOrUnfinishedLiveGroup(t *testing.T) {
	m, f := fixture(t)
	path, ledger, _ := m.paths(f)
	if err := m.makeGroup(path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve(f, 1, 128); err == nil {
		t.Fatal("untracked group adopted")
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatal("untracked group acquired a ledger")
	}
	if err := persist(ledger, record{Fence: proto.CloneOf(f), VCPUs: 1, MemoryMiB: 128, Path: path, EpochUnixNano: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve(f, 1, 128); err == nil {
		t.Fatal("unfinished live group reshaped")
	}
}

func TestHostAccountingRejectsDifferentAllocation(t *testing.T) {
	m, f := fixture(t)
	path, err := m.Reserve(f, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"memory.current": "200", "memory.peak": "250", "memory.stat": "inactive_file 50\n", "cpu.stat": "usage_usec 345\n"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sample, err := m.Read(f)
	if err != nil || sample.MemoryCurrentBytes != 200 || sample.MemoryWorkingSetBytes != 150 || sample.MemoryPeakBytes != 250 || sample.CPUUsageUsec != 345 {
		t.Fatal(sample, err)
	}
	other := proto.CloneOf(f)
	other.AssignmentGeneration++
	if _, err := m.Read(other); err == nil {
		t.Fatal("new allocation received old accounting")
	}
	if err = m.Release(f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Read(f); err == nil {
		t.Fatal("released allocation kept measurements")
	}
}
