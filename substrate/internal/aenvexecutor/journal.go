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

package aenvexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/agent-substrate/substrate/internal/actorlock"
	pb "github.com/agent-substrate/substrate/internal/proto/aenvexecutorpb"
	"google.golang.org/protobuf/proto"
)

// Journal retains the exact command before sending it to the executor. It does
// not allocate actors or authorize assignments; the control plane supplies the
// fence and operation ID. An unanswered RPC never becomes a completed receipt.
type Journal struct {
	root      string
	client    *Client
	lock      *os.File
	actors    *actorlock.Locks
	lifecycle sync.RWMutex
	closed    bool
	write     func(string, []byte) error
}

type receipt struct {
	Fence    []byte `json:"fence"`
	Command  []byte `json:"command"`
	Digest   string `json:"digest"`
	Response []byte `json:"response,omitempty"`
}

// OpenJournal obtains an exclusive process lock. The directory must be a
// Worker-private, persistent directory rather than an ephemeral executor cwd.
func OpenJournal(root string, client *Client) (*Journal, error) {
	if !filepath.IsAbs(root) || client == nil {
		return nil, fmt.Errorf("absolute journal root and executor client required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("journal root must be a private directory, not a symlink")
	}
	lockPath := filepath.Join(root, ".lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("journal lock must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("journal already in use: %w", err)
	}
	return &Journal{root: root, client: client, lock: lock, actors: actorlock.New(), write: writeReceipt}, nil
}

// Close waits for in-flight calls before releasing ownership of the journal.
func (j *Journal) Close() error {
	j.lifecycle.Lock()
	defer j.lifecycle.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return j.lock.Close()
}

func receiptKey(fence *pb.Fence, operationID string) string {
	// Hashing also bounds paths and prevents caller-controlled separators from
	// creating a directory outside the journal, even before RPC validation.
	digest := sha256.Sum256([]byte(fence.GetActorUid() + "/" + strconv.FormatUint(fence.GetAssignmentGeneration(), 10) + "/" + operationID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func readReceipt(path string) (*receipt, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<20 {
		return nil, fmt.Errorf("unsafe or oversized operation receipt")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	decoder.DisallowUnknownFields()
	var result receipt
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("invalid receipt trailing data")
	}
	return &result, nil
}

func writeReceipt(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (j *Journal) save(path string, record *receipt) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("operation receipt exceeds size limit")
	}
	return j.write(path, data)
}

func terminalResponse(record *receipt, operationID string) (*pb.OperationResponse, error) {
	if len(record.Response) == 0 {
		return nil, nil
	}
	response := &pb.OperationResponse{}
	if err := proto.Unmarshal(record.Response, response); err != nil {
		return nil, fmt.Errorf("invalid persisted operation response: %w", err)
	}
	if response.OperationId != operationID {
		return nil, fmt.Errorf("persisted operation identity mismatch")
	}
	if response.Effect == pb.Effect_COMPLETED && response.ErrorCode == "" {
		return response, nil
	}
	if response.Effect == pb.Effect_NO_EFFECT {
		return response, fmt.Errorf("executor rejected operation before effects: %s", response.ErrorCode)
	}
	return nil, nil
}

// Execute is one caller-initiated attempt. A repeated call reuses the recorded
// command, never generates an operation ID and never changes its fence. A
// completed call is answered from disk, including after a Go process restart.
func (j *Journal) Execute(ctx context.Context, fence *pb.Fence, operationID string, command *pb.Command) (*pb.OperationResponse, error) {
	j.lifecycle.RLock()
	defer j.lifecycle.RUnlock()
	if j.closed {
		return nil, fmt.Errorf("journal closed")
	}
	if err := j.client.validateFence(fence); err != nil {
		return nil, err
	}
	if operationID == "" || command == nil || command.Action == nil {
		return nil, fmt.Errorf("operation identity and command required")
	}
	if !j.actors.Lock(ctx, fence.ActorUid) {
		return nil, ctx.Err()
	}
	defer j.actors.Unlock(fence.ActorUid)
	commandWire, err := proto.MarshalOptions{Deterministic: true}.Marshal(command)
	if err != nil {
		return nil, err
	}
	fenceWire, err := proto.MarshalOptions{Deterministic: true}.Marshal(fence)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(commandWire)
	desired := &receipt{Fence: fenceWire, Command: commandWire, Digest: hex.EncodeToString(digest[:])}
	path := filepath.Join(j.root, receiptKey(fence, operationID))
	record, err := readReceipt(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		record = desired
		if err := j.save(path, record); err != nil {
			return nil, fmt.Errorf("persist operation before execution: %w", err)
		}
	case err != nil:
		return nil, err
	default:
		if !bytes.Equal(record.Fence, desired.Fence) || !bytes.Equal(record.Command, desired.Command) || record.Digest != desired.Digest {
			return nil, fmt.Errorf("operation identity reused with different fence or payload")
		}
		if result, err := terminalResponse(record, operationID); result != nil || err != nil {
			return result, err
		}
	}
	result, executeErr := j.client.Execute(ctx, fence, operationID, command)
	if result != nil {
		record.Response, err = proto.Marshal(result)
		if err != nil {
			return nil, &EffectUnknown{OperationID: operationID, Cause: err}
		}
		if err := j.save(path, record); err != nil {
			return nil, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("persist execution result: %w", err)}
		}
	}
	return result, executeErr
}

// Resolve records an executor's journal observation without issuing a mutating
// command. Absence is not proof of nonexecution and does not authorize cleanup.
func (j *Journal) Resolve(ctx context.Context, fence *pb.Fence, operationID string) (*pb.OperationResponse, error) {
	j.lifecycle.RLock()
	defer j.lifecycle.RUnlock()
	if j.closed {
		return nil, fmt.Errorf("journal closed")
	}
	if err := j.client.validateFence(fence); err != nil {
		return nil, err
	}
	if !j.actors.Lock(ctx, fence.ActorUid) {
		return nil, ctx.Err()
	}
	defer j.actors.Unlock(fence.ActorUid)
	path := filepath.Join(j.root, receiptKey(fence, operationID))
	record, err := readReceipt(path)
	if err != nil {
		return nil, err
	}
	observedFence := &pb.Fence{}
	if err := proto.Unmarshal(record.Fence, observedFence); err != nil {
		return nil, err
	}
	if !proto.Equal(observedFence, fence) {
		return nil, fmt.Errorf("operation fence changed")
	}
	digest := sha256.Sum256(record.Command)
	if record.Digest != hex.EncodeToString(digest[:]) {
		return nil, fmt.Errorf("persisted command digest mismatch")
	}
	if result, err := terminalResponse(record, operationID); result != nil || err != nil {
		return result, err
	}
	observed, err := j.client.Inspect(ctx, fence, operationID)
	if err != nil {
		return nil, &EffectUnknown{OperationID: operationID, Cause: err}
	}
	result := observed.GetOperation()
	if result == nil || result.OperationId != operationID {
		return nil, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("executor cannot confirm operation result")}
	}
	record.Response, err = proto.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := j.save(path, record); err != nil {
		return nil, &EffectUnknown{OperationID: operationID, Cause: err}
	}
	if result, err := terminalResponse(record, operationID); result != nil || err != nil {
		return result, err
	}
	return result, &EffectUnknown{OperationID: operationID, Cause: fmt.Errorf("executor operation remains unresolved")}
}
