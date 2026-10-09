// Package expiry enforces compatibility timeouts through Substrate. It never
// treats a failed or timed-out deletion as evidence that an Actor stopped.
package expiry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
)

type Store interface {
	ClaimExpired(context.Context, int) ([]metadata.Expiry, error)
	ConfirmDeleted(context.Context, metadata.Request) error
}
type Control interface {
	Delete(context.Context, auth.Tenant, metadata.Sandbox) error
}
type Pauser interface {
	PauseExpired(context.Context, auth.Tenant, metadata.Expiry) error
}

type Worker struct {
	Pauses     Pauser
	Store      Store
	Control    Control
	Tenants    map[string]auth.Tenant
	Batch      int
	RPCTimeout time.Duration
}

func (w *Worker) validate() error {
	if w.Store == nil || w.Control == nil || len(w.Tenants) == 0 || w.Batch < 1 || w.Batch > 1000 || w.RPCTimeout <= 0 {
		return fmt.Errorf("complete expiry worker configuration required")
	}
	for id, t := range w.Tenants {
		if id == "" || t.ID != id || t.Atespace == "" {
			return fmt.Errorf("invalid expiry tenant mapping")
		}
	}
	return nil
}

// Sweep processes a bounded batch. Failures remain durable and are retried in
// a later sweep. Successful siblings are committed independently.
func (w *Worker) Sweep(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	claims, err := w.Store.ClaimExpired(ctx, w.Batch)
	if err != nil {
		return err
	}
	var failures []error
	for _, claim := range claims {
		if err = ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		b := claim.Sandbox
		tenant, ok := w.Tenants[b.Tenant]
		if !ok || tenant.Atespace != b.ActorAtespace || b.ActorUID == "" || (claim.Request.Kind != "delete" && claim.Request.Kind != "suspend") || claim.Request.Tenant != b.Tenant || claim.Request.ExternalID != b.ExternalID {
			failures = append(failures, fmt.Errorf("expiry claim has invalid tenant or Actor identity"))
			continue
		}
		call, cancel := context.WithTimeout(ctx, w.RPCTimeout)

		if claim.Request.Kind == "suspend" {
			if w.Pauses == nil {
				cancel()
				failures = append(failures, fmt.Errorf("expiry pause handler required"))
				continue
			}
			err = w.Pauses.PauseExpired(call, tenant, claim)
			cancel()
			if err != nil {
				failures = append(failures, fmt.Errorf("pause expired sandbox %s: %w", b.ExternalID, err))
			}
			continue
		}
		err = w.Control.Delete(call, tenant, b)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("delete expired sandbox %s: %w", b.ExternalID, err))
			continue
		}
		if err = w.Store.ConfirmDeleted(ctx, claim.Request); err != nil {
			failures = append(failures, fmt.Errorf("commit expired sandbox %s: %w", b.ExternalID, err))
		}
	}
	return errors.Join(failures...)
}

// Run performs one sweep at a time. Reporting failures does not discard their
// intents; cancellation terminates the loop without acknowledging in-flight work.
func (w *Worker) Run(ctx context.Context, interval time.Duration, report func(error)) error {
	if err := w.validate(); err != nil {
		return err
	}
	if interval <= 0 || report == nil {
		return fmt.Errorf("positive expiry interval and error reporter required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.Sweep(ctx); err != nil {
			report(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
