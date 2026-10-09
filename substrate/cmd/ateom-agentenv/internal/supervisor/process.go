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

// Package supervisor runs exactly one executor for the lifetime of a Worker.
package supervisor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Process struct {
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func Start(binary string, args []string, output io.Writer) (*Process, error) {
	if binary == "" {
		return nil, fmt.Errorf("executor binary required")
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &Process{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}
func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Err() error            { p.mu.Lock(); defer p.mu.Unlock(); return p.err }

// Stop allows Rust to stop its VMs and release ublk devices. A timeout kills
// the process group and returns failure, never claiming graceful cleanup.
func (p *Process) Stop(ctx context.Context) error {
	select {
	case <-p.done:
		return p.Err()
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		select {
		case <-p.done:
			return p.Err()
		default:
			return err
		}
	}
	select {
	case <-p.done:
		return p.Err()
	case <-ctx.Done():
		killErr := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
		return fmt.Errorf("executor shutdown incomplete: %w (kill: %v)", ctx.Err(), killErr)
	}
}
