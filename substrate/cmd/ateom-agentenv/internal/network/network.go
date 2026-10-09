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

// Package network owns the namespace and link topology of embedded Actors.
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
)

type Runner func(context.Context, string, ...string) error

func Run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, output)
	}
	return nil
}

type Config struct {
	Root      string
	MaxActors int
	// LinkPool supplies one /30 per Actor; it must not overlap Pod/service routes.
	LinkPool            netip.Prefix
	DNS                 netip.Addr
	PlatformDeniedCIDRs []string
}
type record struct {
	OwnedNamespace bool                  `json:"ownedNamespace"`
	OwnedLink      bool                  `json:"ownedLink"`
	Fence          *pb.Fence             `json:"fence"`
	Slot           int                   `json:"slot"`
	State          string                `json:"state"`
	Attachment     *pb.NetworkAttachment `json:"attachment"`
}
type Manager struct {
	mu      sync.Mutex
	cfg     Config
	run     Runner
	records map[string]*record
}

func Open(cfg Config, run Runner) (*Manager, error) {
	if !filepath.IsAbs(cfg.Root) || cfg.MaxActors < 1 || !cfg.LinkPool.IsValid() || !cfg.LinkPool.Addr().Is4() || cfg.LinkPool != cfg.LinkPool.Masked() || cfg.LinkPool.Bits() < 16 || cfg.LinkPool.Bits() > 30 || cfg.MaxActors > 1<<(30-cfg.LinkPool.Bits()) || !cfg.DNS.Is4() || run == nil {
		return nil, fmt.Errorf("invalid network ownership configuration")
	}
	for _, cidr := range cfg.PlatformDeniedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(cfg.Root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cfg.Root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("network ledger must be private")
	}
	m := &Manager{cfg: cfg, run: run, records: map[string]*record{}}
	entries, err := os.ReadDir(cfg.Root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("unsafe network ledger entry")
		}
		data, err := os.ReadFile(filepath.Join(cfg.Root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		if r.Fence == nil || entry.Name() != r.Fence.ActorUid+".json" {
			return nil, fmt.Errorf("corrupt network ownership")
		}
		// Reopening does not prove that mounts or detached VMs survived unchanged.
		if r.State != "stopped" {
			return nil, fmt.Errorf("unreconciled network for Actor %s; fence Worker before cleanup", r.Fence.ActorUid)
		}
		m.records[r.Fence.ActorUid] = &r
	}
	return m, nil
}

func (m *Manager) save(r *record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.cfg.Root, ".network-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(m.cfg.Root, r.Fence.ActorUid+".json")); err != nil {
		return err
	}
	d, err := os.Open(m.cfg.Root)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (m *Manager) names(f *pb.Fence) (string, string) {
	// Generation is unique across all assignments, including other Worker Pods.
	suffix := strconv.FormatUint(f.AssignmentGeneration, 36)
	return "aenv-" + suffix, "av" + suffix
}
func (m *Manager) Attach(ctx context.Context, fence *pb.Fence, policy *pb.NetworkPolicy) (*pb.NetworkAttachment, error) {
	if err := aenvexecutor.ValidateOperation(&pb.LifecycleOperation{Fence: fence, OperationId: "network"}, fence.GetActorUid(), fence.GetWorkerPodUid()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.records[fence.ActorUid]; old != nil {
		if old.Fence.AssignmentGeneration > fence.AssignmentGeneration {
			return nil, fmt.Errorf("stale network allocation")
		}
		if old.Fence.AssignmentGeneration == fence.AssignmentGeneration {
			if !proto.Equal(old.Fence, fence) || old.State != "ready" {
				return nil, fmt.Errorf("network allocation is stopped or unresolved")
			}
			attachment := proto.CloneOf(old.Attachment)
			attachment.Policy = proto.CloneOf(policy)
			return attachment, nil
		}
		if old.State != "stopped" {
			return nil, fmt.Errorf("previous network allocation is not stopped")
		}
	}
	used := map[int]bool{}
	for _, r := range m.records {
		if r.State != "stopped" {
			used[r.Slot] = true
		}
	}
	slot := 0
	for used[slot] {
		slot++
	}
	if slot >= m.cfg.MaxActors {
		return nil, fmt.Errorf("network Actor budget exhausted")
	}
	base := m.cfg.LinkPool.Addr()
	for i := 0; i < slot*4; i++ {
		base = base.Next()
	}
	host := base.Next()
	peer := host.Next()
	ns, link := m.names(fence)
	attachment := &pb.NetworkAttachment{NetnsPath: filepath.Join("/run/netns", ns), TapName: "tap0", GuestIpv4: "172.31.255.2", GatewayIpv4: "172.31.255.1", GuestMac: "02:fc:00:00:00:02", Mtu: 1500, Policy: proto.CloneOf(policy), InteractionIpv4: peer.String(), Netmask: "255.255.255.252", DnsIpv4: m.cfg.DNS.String(), PlatformDeniedCidrs: append([]string(nil), m.cfg.PlatformDeniedCIDRs...)}
	r := &record{Fence: proto.CloneOf(fence), Slot: slot, State: "creating", Attachment: attachment}
	if err := m.save(r); err != nil {
		return nil, err
	}
	m.records[fence.ActorUid] = r
	// Never replace or delete pre-existing objects. Partial creation remains
	// reserved until an explicit confirmed Stop permits cleanup.
	commands := [][]string{
		{"ip", "netns", "add", ns},
		{"ip", "link", "add", link, "type", "veth", "peer", "name", "vpeer", "netns", ns},
		{"ip", "addr", "add", host.String() + "/30", "dev", link},
		{"ip", "link", "set", link, "up"},
		{"ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up"},
		{"ip", "netns", "exec", ns, "ip", "addr", "add", peer.String() + "/30", "dev", "vpeer"},
		{"ip", "netns", "exec", ns, "ip", "link", "set", "vpeer", "up"},
		{"ip", "netns", "exec", ns, "ip", "tuntap", "add", "dev", "tap0", "mode", "tap"},
		{"ip", "netns", "exec", ns, "ip", "addr", "add", "172.31.255.1/30", "dev", "tap0"},
		{"ip", "netns", "exec", ns, "ip", "link", "set", "tap0", "up"},
		{"ip", "netns", "exec", ns, "ip", "route", "add", "default", "via", host.String()},
		{"ip", "netns", "exec", ns, "sysctl", "-w", "net.ipv4.ip_forward=1"},
		{"ip", "netns", "exec", ns, "iptables", "-w", "-t", "nat", "-A", "POSTROUTING", "-s", "172.31.255.2/32", "-o", "vpeer", "-j", "SNAT", "--to-source", peer.String()},
		{"ip", "netns", "exec", ns, "iptables", "-w", "-t", "nat", "-A", "PREROUTING", "-i", "vpeer", "-d", peer.String(), "-j", "DNAT", "--to-destination", "172.31.255.2"},
		{"iptables", "-w", "-t", "nat", "-A", "POSTROUTING", "-s", peer.String() + "/32", "-m", "comment", "--comment", ns, "-j", "MASQUERADE"},
	}
	for index, cmd := range commands {
		if err := m.run(ctx, cmd[0], cmd[1:]...); err != nil {
			return nil, err
		}
		if index == 0 {
			r.OwnedNamespace = true
		}
		if index == 1 {
			r.OwnedLink = true
		}
		if index < 2 {
			if err := m.save(r); err != nil {
				return nil, err
			}
		}
	}
	r.State = "ready"
	if err := m.save(r); err != nil {
		r.State = "creating"
		return nil, err
	}
	return proto.CloneOf(attachment), nil
}

// Detach is called only after the executor confirms that this allocation has
// stopped. Failed cleanup remains reserved and is retried explicitly.
func (m *Manager) Detach(ctx context.Context, fence *pb.Fence) error {
	if err := aenvexecutor.ValidateOperation(&pb.LifecycleOperation{Fence: fence, OperationId: "network"}, fence.GetActorUid(), fence.GetWorkerPodUid()); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[fence.ActorUid]
	if r == nil {
		r = &record{Fence: proto.CloneOf(fence), State: "stopped"}
		if err := m.save(r); err != nil {
			return err
		}
		m.records[fence.ActorUid] = r
		return nil
	}
	if !proto.Equal(r.Fence, fence) {
		return fmt.Errorf("network cleanup fence mismatch")
	}
	if r.State == "stopped" {
		return nil
	}
	if r.State != "ready" && r.State != "removing" && !(r.State == "creating" && r.OwnedNamespace) {
		return fmt.Errorf("partial network creation requires ownership reconciliation")
	}
	r.State = "removing"
	if err := m.save(r); err != nil {
		return err
	}
	ns, link := m.names(fence)
	// Check each object before deleting so a repeated confirmed cleanup is safe.
	nat := []string{"-w", "-t", "nat", "-C", "POSTROUTING", "-s", r.Attachment.InteractionIpv4 + "/32", "-m", "comment", "--comment", ns, "-j", "MASQUERADE"}
	if err := m.run(ctx, "iptables", nat...); err == nil {
		nat[3] = "-D"
		if err := m.run(ctx, "iptables", nat...); err != nil {
			return err
		}
	} else if !absent(err) {
		return err
	}
	if r.OwnedLink {
		if err := m.run(ctx, "ip", "link", "show", link); err == nil {
			if err := m.run(ctx, "ip", "link", "delete", link); err != nil {
				return err
			}
		} else if !absent(err) {
			return err
		}
	}
	if _, err := os.Lstat(r.Attachment.NetnsPath); err == nil {
		if err := m.run(ctx, "ip", "netns", "delete", ns); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	r.State = "stopped"
	return m.save(r)
}

func absent(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}
