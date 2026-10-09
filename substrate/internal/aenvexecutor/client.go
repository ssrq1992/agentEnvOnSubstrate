// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package aenvexecutor implements the release-coupled Worker/executor boundary.
package aenvexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const ProtocolVersion = "agentenv-executor-v1"

type Client struct {
	RPC        pb.ExecutorClient
	InstanceID string
	MaxActors  uint32
}

// EffectUnknown must trigger Inspect/Reconcile. It never authorizes retrying
// with a new operation ID, freeing capacity or destroying network attachments.
type EffectUnknown struct {
	OperationID string
	Cause       error
}

func (e *EffectUnknown) Error() string {
	return fmt.Sprintf("operation %s effect unknown: %v", e.OperationID, e.Cause)
}
func (e *EffectUnknown) Unwrap() error { return e.Cause }

func Dial(ctx context.Context, socket string) (*Client, func() error, error) {
	if !filepath.IsAbs(socket) {
		return nil, nil, fmt.Errorf("executor socket must be absolute")
	}
	info, err := os.Lstat(socket)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, nil, fmt.Errorf("executor socket must be private")
	}
	parent, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return nil, nil, err
	}
	if parent.Mode().Perm()&0077 != 0 {
		return nil, nil, fmt.Errorf("executor directory must be private")
	}
	conn, err := grpc.NewClient("passthrough:///aenv-executor", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}))
	if err != nil {
		return nil, nil, err
	}
	client, err := Handshake(ctx, pb.NewExecutorClient(conn))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return client, conn.Close, nil
}

func Handshake(ctx context.Context, rpc pb.ExecutorClient) (*Client, error) {
	caps, err := rpc.Capabilities(ctx, &pb.CapabilitiesRequest{})
	if err != nil {
		return nil, err
	}
	if caps.GetProtocolVersion() != ProtocolVersion || caps.GetWorkerInstanceId() == "" || caps.GetMaxActors() == 0 {
		return nil, fmt.Errorf("invalid executor capability handshake")
	}
	return &Client{RPC: rpc, InstanceID: caps.WorkerInstanceId, MaxActors: caps.MaxActors}, nil
}

// ValidateOperation checks the transport boundary before any node-local effect.
// It does not establish authority: the adapter must also compare the fence to
// its own registered identity and the executor's generation journal.
func ValidateOperation(op *pb.LifecycleOperation, actorUID, podUID string) error {
	if op == nil || op.Fence == nil {
		return fmt.Errorf("execution identity required")
	}
	client := &Client{InstanceID: op.Fence.WorkerInstanceId}
	if err := client.validateFence(op.Fence); err != nil {
		return err
	}
	if op.Fence.ActorUid != actorUID || op.Fence.WorkerPodUid != podUID {
		return fmt.Errorf("execution identity does not match request target")
	}
	return validateOperationID(op.OperationId)
}

func validateOperationID(operationID string) error {
	if operationID == "" || len(operationID) > 128 || strings.IndexFunc(operationID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 {
		return fmt.Errorf("invalid persisted operation ID")
	}
	return nil
}

// Execute requires an operation ID persisted by the lifecycle authority before
// this call. Retries retain that ID and exactly the same command bytes.
func (c *Client) Execute(ctx context.Context, fence *pb.Fence, operationID string, command *pb.Command) (*pb.OperationResponse, error) {
	if err := c.validateFence(fence); err != nil {
		return nil, err
	}
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	if command == nil || command.Action == nil {
		return nil, fmt.Errorf("executor command required")
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		return nil, fmt.Errorf("future operation deadline required")
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(command)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	response, err := c.RPC.Execute(ctx, &pb.OperationRequest{Fence: proto.CloneOf(fence), OperationId: operationID, PayloadDigest: hex.EncodeToString(digest[:]), DeadlineUnixMillis: deadline.UnixMilli(), Command: proto.CloneOf(command)})
	if err != nil {
		return nil, &EffectUnknown{OperationID: operationID, Cause: err}
	}
	if response.GetOperationId() != operationID {
		return nil, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("executor returned different operation identity")}
	}
	switch response.GetEffect() {
	case pb.Effect_COMPLETED:
		if response.ErrorCode != "" {
			return response, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("completed result contains an error")}
		}
		return response, nil
	case pb.Effect_NO_EFFECT:
		return response, fmt.Errorf("executor rejected operation before effects: %s", response.ErrorCode)
	default:
		return response, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("executor result: %s", response.ErrorCode)}
	}
}

func (c *Client) validateFence(f *pb.Fence) error {
	if f == nil || f.ProtocolVersion != ProtocolVersion || f.WorkerInstanceId == "" || f.WorkerInstanceId != c.InstanceID || f.WorkerPodUid == "" || f.WorkerEpoch == 0 || f.AssignmentGeneration == 0 {
		return fmt.Errorf("complete current Worker fence required")
	}
	id, err := uuid.Parse(f.ActorUid)
	if err != nil {
		return fmt.Errorf("invalid Actor UID: %w", err)
	}
	if id.String() != f.ActorUid {
		return fmt.Errorf("Actor UID must be a canonical lowercase UUID")
	}
	return nil
}

func (c *Client) Inspect(ctx context.Context, fence *pb.Fence, operationID string) (*pb.InspectResponse, error) {
	if err := c.validateFence(fence); err != nil {
		return nil, err
	}
	response, err := c.RPC.Inspect(ctx, &pb.InspectRequest{Fence: proto.CloneOf(fence), OperationId: operationID})
	if err != nil {
		return nil, err
	}
	if !proto.Equal(response.GetFence(), fence) {
		return nil, fmt.Errorf("executor returned different assignment identity")
	}
	return response, nil
}

// Stats returns envd guest measurements without converting CPU percentages to
// cumulative CPU time or treating guest memory as host cgroup consumption.
func (c *Client) Stats(ctx context.Context, fence *pb.Fence) (*pb.StatsResponse, error) {
	if err := c.validateFence(fence); err != nil {
		return nil, err
	}
	response, err := c.RPC.Stats(ctx, &pb.StatsRequest{Fence: proto.CloneOf(fence)})
	if err != nil {
		return nil, err
	}
	if err := ValidateGuestStats(fence, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ValidateGuestStats prevents attributing another allocation's sample or
// representing missing/invalid measurements as successfully measured zero.
func ValidateGuestStats(fence *pb.Fence, response *pb.StatsResponse) error {
	if fence == nil || response == nil || !proto.Equal(response.GetFence(), fence) {
		return fmt.Errorf("executor returned different assignment identity")
	}
	if response.CpuCount == 0 || response.ObservedAtUnixMillis <= 0 || response.MemoryUsedBytes > response.MemoryTotalBytes || response.MemoryCacheBytes > response.MemoryTotalBytes || response.DiskUsedBytes > response.DiskTotalBytes || math.IsNaN(float64(response.CpuUsedPercent)) || math.IsInf(float64(response.CpuUsedPercent), 0) || response.CpuUsedPercent < 0 {
		return fmt.Errorf("executor returned invalid guest measurements")
	}
	return nil
}

// ValidateRuntimeInfo binds connection metadata to a live allocation.
func ValidateRuntimeInfo(fence *pb.Fence, info *pb.InspectResponse) error {
	if fence == nil || info == nil || !proto.Equal(fence, info.GetFence()) || info.GetState() != "RUNNING" {
		return fmt.Errorf("runtime is not running under the expected allocation")
	}
	version := info.GetEnvdVersion()
	if version == "" || len(version) > 128 || strings.IndexFunc(version, func(r rune) bool { return r <= 32 || r > 126 }) >= 0 {
		return fmt.Errorf("runtime envd version is unavailable")
	}
	return nil
}
