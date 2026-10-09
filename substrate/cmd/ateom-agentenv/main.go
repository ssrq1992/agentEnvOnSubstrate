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

// Command ateom-agentenv runs the Substrate-controlled embedded AgentENV backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/agent-substrate/substrate/cmd/ateom-agentenv/internal/adapter"
	"github.com/agent-substrate/substrate/cmd/ateom-agentenv/internal/admission"
	"github.com/agent-substrate/substrate/cmd/ateom-agentenv/internal/cgroups"
	"github.com/agent-substrate/substrate/cmd/ateom-agentenv/internal/network"
	"github.com/agent-substrate/substrate/cmd/ateom-agentenv/internal/supervisor"
	"github.com/agent-substrate/substrate/internal/aenvexecutor"
	"github.com/agent-substrate/substrate/internal/ateinterceptors"
	"github.com/agent-substrate/substrate/internal/ateom"
	"github.com/agent-substrate/substrate/internal/ateomtunnel"
	"github.com/agent-substrate/substrate/internal/atunnel"
	"github.com/agent-substrate/substrate/internal/nodepath"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/internal/serverboot"
	"github.com/agent-substrate/substrate/internal/version"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
)

var (
	podUID               = pflag.String("pod-uid", "", "Worker Pod UID")
	actorMilliCPUReserve = pflag.Int64("actor-cpu-reserve-millis", 250, "Host CPU reserve for each admitted Actor")
	actorMemoryReserve   = pflag.Int64("actor-memory-reserve-mib", 128, "Host memory reserve for each admitted Actor")
	maxActors            = pflag.Int("max-actors", 1, "Maximum simultaneous Actors")
	maxDevices           = pflag.Int("max-devices", 64, "Maximum owned ublk devices, including shared and idle pool devices")
	executorBinary       = pflag.String("executor-binary", "/usr/local/bin/aenv-executor", "Embedded executor binary")
	configPath           = pflag.String("executor-config", "/etc/agentenv/config.toml", "Embedded runtime configuration")
	linkPool             = pflag.String("actor-link-pool", "172.30.0.0/16", "Worker-local Actor link subnet; must not overlap cluster networks")
	dnsIP                = pflag.String("actor-dns", "1.1.1.1", "Guest DNS resolver")
	denied               = pflag.StringSlice("platform-denied-cidrs", []string{"169.254.0.0/16", "127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, "Platform destination CIDRs denied regardless of user policy")
	readinessAddress     = pflag.String("readiness-listen-address", "0.0.0.0:8080", "Readiness HTTP address")
	tunnelConfig         = ateomtunnel.RegisterFlags(pflag.CommandLine)
	showVersion          = pflag.Bool("version", false, "Print version")
)

type ingress struct{ tunnel *ateomtunnel.Tunnel }

func (i ingress) Activate(actor resources.ActorAttribution, generation uint64, dial func(context.Context, string, string) (net.Conn, error)) error {
	return i.tunnel.Ingress.ActivateAssignment(actor.Ref.Atespace, actor.Ref.Name, actor.UID, generation, atunnel.DialFunc(dial))
}
func (i ingress) Deactivate(ctx context.Context, actor resources.ActorAttribution) error {
	return i.tunnel.Deactivate(ctx, actor)
}
func main() {
	pflag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}
	serverboot.InitLoggerWithWriter(os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("AgentENV Worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
}
func budget(actors int) (*ateletpb.WorkerResources, uint64, uint64, error) {
	return admission.Compute(ateom.FromFiles(actors), actors, admission.Reserves{
		WorkerMilliCPU: 250, WorkerMemoryMiB: 256,
		ActorMilliCPU: *actorMilliCPUReserve, ActorMemoryMiB: *actorMemoryReserve,
	})
}

func run(ctx context.Context) (runErr error) {
	if *maxDevices < 4 || *maxDevices > 65536 {
		return fmt.Errorf("ublk device budget must be between 4 and 65536")
	}
	if err := resources.ValidateAteomUID(*podUID); err != nil {
		return err
	}
	capacity, cpus, memory, err := budget(*maxActors)
	if err != nil {
		return err
	}
	pool, err := netip.ParsePrefix(*linkPool)
	if err != nil {
		return err
	}
	dns, err := netip.ParseAddr(*dnsIP)
	if err != nil {
		return err
	}
	report := ateom.ReportConfig{SocketPath: nodepath.AteomSupportSocket, CredentialBundlePath: tunnelConfig.CredentialBundle, TrustBundlePath: tunnelConfig.TrustBundle, AteletSPIFFEID: tunnelConfig.BrokerIdentity, Actors: *maxActors, Capacity: capacity}
	identity, err := ateom.ProbeIdentity(ctx, report)
	if err != nil {
		return err
	}
	if identity.GetWorkerPodUid() != *podUID || identity.GetWorkerEpoch() <= 0 {
		return fmt.Errorf("invalid authenticated Worker identity")
	}
	root := nodepath.AteomPath(*podUID)
	runtimeRoot := filepath.Join(root, "executor")
	socket := filepath.Join(root, "executor.sock")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	cgroupRoot, err := cgroups.DelegateCurrent(ctx)
	if err != nil {
		return err
	}
	limits, err := cgroups.Open(cgroups.Config{Root: cgroupRoot, Ledger: filepath.Join(root, "cgroup-allocations"), MemoryReserveMiB: uint64(*actorMemoryReserve), CPUReserveMillis: uint64(*actorMilliCPUReserve), PidsMax: 4096})
	if err != nil {
		return err
	}
	process, err := supervisor.Start(*executorBinary, []string{"--config", *configPath, "--socket", socket, "--root", runtimeRoot, "--worker-pod-uid", *podUID, "--worker-epoch", strconv.FormatInt(identity.WorkerEpoch, 10), "--max-actors", strconv.Itoa(*maxActors), "--max-devices", strconv.Itoa(*maxDevices), "--max-vcpus", strconv.FormatUint(cpus, 10), "--max-memory-mib", strconv.FormatUint(memory, 10), "--cgroup-root", cgroupRoot}, os.Stdout)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := process.Stop(stopCtx); err != nil {
			slog.Error("Executor shutdown failed", slog.Any("error", err))
			runErr = errors.Join(runErr, err)
		}
	}()
	// An executor exit cancels registration and startup too, not only serving.
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-process.Done():
			cancel()
		case <-workerCtx.Done():
		}
	}()
	var client *aenvexecutor.Client
	var closeClient func() error
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for client == nil {
		callCtx, callCancel := context.WithTimeout(workerCtx, time.Second)
		client, closeClient, err = aenvexecutor.Dial(callCtx, socket)
		callCancel()
		if err == nil {
			break
		}
		select {
		case <-workerCtx.Done():
			return fmt.Errorf("executor startup ended: %w", workerCtx.Err())
		case <-deadline.C:
			return fmt.Errorf("executor startup timeout: %w", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer closeClient()
	capabilityCtx, stopCapabilities := context.WithTimeout(workerCtx, 10*time.Second)
	capabilities, err := client.RPC.Capabilities(capabilityCtx, &pb.CapabilitiesRequest{})
	stopCapabilities()
	if err != nil {
		return err
	}
	if capabilities.GetWorkerInstanceId() != client.InstanceID || !slices.Contains(capabilities.GetCapabilities(), "allocation-cgroup-v2") || !slices.Contains(capabilities.GetCapabilities(), "durable-device-ledger-v1") || capabilities.GetMaxDevices() != uint32(*maxDevices) {
		return fmt.Errorf("executor must support mandatory allocation cgroup isolation and durable device ledger")
	}

	reconcileCtx, stopReconcile := context.WithTimeout(workerCtx, 10*time.Second)
	observations, err := client.RPC.Reconcile(reconcileCtx, &pb.ReconcileRequest{WorkerPodUid: *podUID, WorkerEpoch: uint64(identity.WorkerEpoch), WorkerInstanceId: client.InstanceID})
	stopReconcile()
	if err != nil {
		return err
	}
	if len(observations.GetActors()) != 0 {
		return fmt.Errorf("fresh executor unexpectedly contains active Actors")
	}
	if err = limits.ReconcileStopped(); err != nil {
		return err
	}

	journal, err := aenvexecutor.OpenJournal(filepath.Join(root, "go-operations"), client)
	if err != nil {
		return err
	}
	defer journal.Close()
	nets, err := network.Open(network.Config{Root: filepath.Join(root, "network"), MaxActors: *maxActors, LinkPool: pool, DNS: dns, PlatformDeniedCIDRs: *denied}, network.Run)
	if err != nil {
		return err
	}
	if err := network.Run(workerCtx, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	tunnel, err := ateomtunnel.Start(workerCtx, *tunnelConfig, "http://172.31.255.2:80")
	if err != nil {
		return err
	}
	service, err := adapter.New(*podUID, client.InstanceID, runtimeRoot, uint64(identity.WorkerEpoch), journal, nets, ingress{tunnel}, limits)
	if err != nil {
		return err
	}
	listener, err := net.Listen("unix", nodepath.AteomSocketPath(*podUID))
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(nodepath.AteomSocketPath(*podUID), 0600); err != nil {
		return err
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(ateinterceptors.InternalServerUnaryInterceptor))
	ateompb.RegisterAteomServer(server, service)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer server.Stop()
	report.ExpectedEpoch = identity.WorkerEpoch
	report.ExecutorInstanceID = client.InstanceID
	if err := ateom.Report(workerCtx, report); err != nil {
		return err
	}
	readiness := &serverboot.Readiness{}
	go serverboot.StartReadinessServer(workerCtx, *readinessAddress, readiness)
	select {
	case <-workerCtx.Done():
	case err := <-serveErr:
		return err
	}
	readiness.MarkNotReady()
	// Reject new RPCs before the executor begins stopping its VMs.
	stopped := make(chan struct{})
	go func() { server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		server.Stop()
	}
	select {
	case <-process.Done():
		return fmt.Errorf("executor exited; Worker replacement required: %v", process.Err())
	default:
	}
	return nil
}
