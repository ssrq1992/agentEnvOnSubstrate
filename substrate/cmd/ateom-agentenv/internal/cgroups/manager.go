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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/internal/cgroupstats"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	Root, Ledger                       string
	MemoryReserveMiB, CPUReserveMillis uint64
	PidsMax                            uint64
}
type Manager struct {
	cfg         Config
	mu          sync.Mutex
	makeGroup   func(string) error
	removeGroup func(string) error
}
type record struct {
	Fence         *pb.Fence
	VCPUs         uint32
	MemoryMiB     uint64
	Path          string
	Ready         bool
	EpochUnixNano int64
}

func Open(cfg Config) (*Manager, error) {
	if !filepath.IsAbs(cfg.Root) || !filepath.IsAbs(cfg.Ledger) || cfg.PidsMax < 1 || cfg.CPUReserveMillis > 1<<30 || cfg.MemoryReserveMiB > 1<<30 {
		return nil, fmt.Errorf("bounded cgroup configuration required")
	}
	for _, name := range []string{"cpu", "memory", "pids"} {
		data, err := os.ReadFile(filepath.Join(cfg.Root, "cgroup.subtree_control"))
		if err != nil {
			return nil, err
		}
		if !contains(string(data), name) {
			return nil, fmt.Errorf("mandatory cgroup controller %s is not delegated", name)
		}
	}
	if err := os.MkdirAll(cfg.Ledger, 0700); err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg, makeGroup: func(path string) error { return os.Mkdir(path, 0755) }, removeGroup: os.Remove}, nil
}
func contains(s, w string) bool {
	for _, v := range strings.Fields(s) {
		if v == w {
			return true
		}
	}
	return false
}
func (m *Manager) paths(f *pb.Fence) (string, string, error) {
	if f == nil || f.ActorUid == "" || strings.ContainsAny(f.ActorUid, "/\\.\x00") || f.AssignmentGeneration == 0 || f.WorkerPodUid == "" || f.WorkerEpoch == 0 || f.WorkerInstanceId == "" {
		return "", "", fmt.Errorf("complete allocation fence required")
	}
	leaf := "aenv-" + f.ActorUid + "-" + strconv.FormatUint(f.AssignmentGeneration, 10)
	return filepath.Join(m.cfg.Root, leaf), filepath.Join(m.cfg.Ledger, f.ActorUid+".json"), nil
}
func existingWrite(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}
func empty(path string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(b))
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "populated" {
			return fields[i+1] == "0", nil
		}
	}
	return false, fmt.Errorf("cgroup population unknown")
}

// Reserve persists the exact allocation before writing limits or launching a VM.
// An unknown execution retains this reservation and cannot be reshaped in place.
func (m *Manager) Reserve(f *pb.Fence, cpus uint32, memory uint64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, ledger, err := m.paths(f)
	if err != nil {
		return "", err
	}
	if cpus == 0 || cpus > 1000000 || memory == 0 || memory > 1<<30 {
		return "", fmt.Errorf("bounded machine resources required")
	}
	wanted := record{Fence: proto.CloneOf(f), VCPUs: cpus, MemoryMiB: memory, Path: path, EpochUnixNano: time.Now().UnixNano()}
	data, err := os.ReadFile(ledger)
	if err == nil {
		var old record
		if json.Unmarshal(data, &old) != nil || !proto.Equal(old.Fence, f) || old.VCPUs != cpus || old.MemoryMiB != memory || old.Path != path || old.EpochUnixNano <= 0 {
			return "", fmt.Errorf("cgroup allocation or machine specification changed")
		}
		wanted.EpochUnixNano = old.EpochUnixNano
		// The group can already contain a running VM: do not modify its limits.
		if _, err = os.Stat(path); err == nil && old.Ready {
			return path, nil
		} else if old.Ready {
			return "", fmt.Errorf("prepared cgroup disappeared")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if _, groupErr := os.Stat(path); groupErr == nil {
		return "", fmt.Errorf("untracked cgroup cannot be adopted")
	} else if !os.IsNotExist(groupErr) {
		return "", groupErr
	}
	if _, groupErr := os.Stat(path); groupErr == nil {
		vacant, e := empty(path)
		if e != nil {
			return "", e
		}
		if !vacant {
			return "", fmt.Errorf("unfinished cgroup remains populated")
		}
	} else if !os.IsNotExist(groupErr) {
		return "", groupErr
	}
	if err := persist(ledger, wanted); err != nil {
		return "", err
	}
	if err := m.makeGroup(path); err != nil && !os.IsExist(err) {
		return "", err
	}
	for name, value := range map[string]string{
		"cpu.max":         strconv.FormatUint((uint64(cpus)*1000+m.cfg.CPUReserveMillis)*100, 10) + " 100000",
		"memory.max":      strconv.FormatUint((memory+m.cfg.MemoryReserveMiB)*1024*1024, 10),
		"memory.swap.max": "0", "memory.oom.group": "1", "pids.max": strconv.FormatUint(m.cfg.PidsMax, 10),
	} {
		if err := existingWrite(filepath.Join(path, name), value); err != nil {
			return "", fmt.Errorf("configure %s: %w", name, err)
		}
	}
	wanted.Ready = true
	if err := persist(ledger, wanted); err != nil {
		return "", err
	}
	return path, nil
}

// Release is called only after the executor confirms complete VM/device stop.
// A populated cgroup or unknown population must retain the durable reservation.
func (m *Manager) Release(f *pb.Fence) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, ledger, err := m.paths(f)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(ledger)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var old record
	if json.Unmarshal(data, &old) != nil || !proto.Equal(old.Fence, f) || old.Path != path {
		return fmt.Errorf("cgroup release allocation mismatch")
	}
	if _, err = os.Stat(path); err == nil {
		vacant, e := empty(path)
		if e != nil {
			return e
		}
		if !vacant {
			return fmt.Errorf("Actor cgroup remains populated")
		}
		if err = m.removeGroup(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Remove(ledger); err != nil {
		return err
	}
	return syncDir(filepath.Dir(ledger))
}
func persist(path string, r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cgroup-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ReconcileStopped removes empty reservations after a fresh executor's fenced
// reconciliation reports no Actors. Its durable live journal must have passed
// the executor's startup barrier; a process restart alone is not stop evidence.
func (m *Manager) ReconcileStopped() error {
	entries, err := os.ReadDir(m.cfg.Ledger)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.cfg.Ledger, entry.Name()))
		if err != nil {
			return err
		}
		var r record
		if json.Unmarshal(data, &r) != nil || r.Fence == nil || entry.Name() != r.Fence.ActorUid+".json" {
			return fmt.Errorf("invalid persisted cgroup allocation")
		}
		if err = m.Release(r.Fence); err != nil {
			return err
		}
	}
	// Untracked groups may hold forgotten VMs. Never delete or adopt them.
	groups, err := os.ReadDir(m.cfg.Root)
	if err != nil {
		return err
	}
	for _, entry := range groups {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "aenv-") && entry.Name() != "aenv-supervisor" {
			return fmt.Errorf("untracked Actor cgroup requires Worker fencing")
		}
	}
	return nil
}

func (m *Manager) Read(f *pb.Fence) (cgroupstats.Sample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, ledger, err := m.paths(f)
	if err != nil {
		return cgroupstats.Sample{}, err
	}
	data, err := os.ReadFile(ledger)
	if err != nil {
		return cgroupstats.Sample{}, err
	}
	var r record
	if json.Unmarshal(data, &r) != nil || !r.Ready || !proto.Equal(r.Fence, f) || r.Path != path {
		return cgroupstats.Sample{}, fmt.Errorf("resource sample allocation mismatch")
	}
	return cgroupstats.Read(path)
}

func (m *Manager) Epoch(f *pb.Fence) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, ledger, err := m.paths(f)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(ledger)
	if err != nil {
		return 0, err
	}
	var r record
	if json.Unmarshal(data, &r) != nil || !r.Ready || !proto.Equal(r.Fence, f) || r.Path != path || r.EpochUnixNano <= 0 {
		return 0, fmt.Errorf("activation accounting epoch unavailable")
	}
	return r.EpochUnixNano, nil
}
