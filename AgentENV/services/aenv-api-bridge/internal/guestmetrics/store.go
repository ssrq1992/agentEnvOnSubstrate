// Package guestmetrics retains bounded guest samples for SDK history queries.
package guestmetrics

import (
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"time"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }
type Sample struct {
	Timestamp     time.Time `json:"timestamp"`
	TimestampUnix int64     `json:"timestampUnix"`
	CPUCount      uint32    `json:"cpuCount"`
	CPUUsedPct    float32   `json:"cpuUsedPct"`
	MemUsed       uint64    `json:"memUsed"`
	MemTotal      uint64    `json:"memTotal"`
	MemCache      uint64    `json:"memCache"`
	DiskUsed      uint64    `json:"diskUsed"`
	DiskTotal     uint64    `json:"diskTotal"`
}

func (c *Store) Migrate(ctx context.Context) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(718304243)"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS aenv_bridge; CREATE TABLE IF NOT EXISTS aenv_bridge.metrics_schema(version integer PRIMARY KEY,digest text NOT NULL)`); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(schema))
	expected := hex.EncodeToString(sum[:])
	var version int
	var stored string
	err = tx.QueryRow(ctx, `SELECT version,digest FROM aenv_bridge.metrics_schema ORDER BY version DESC LIMIT 1`).Scan(&version, &stored)
	if err == nil {
		if version != 1 || stored != expected {
			return fmt.Errorf("unsupported or modified metrics migration")
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO aenv_bridge.metrics_schema(version,digest) VALUES(1,$1)`, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Append(ctx context.Context, b metadata.Sandbox, r *pb.GetActorGuestMetricsResponse) error {
	if b.Tenant == "" || b.ExternalID == "" || b.ActorUID == "" || r.GetActorUid() != b.ActorUID || r.GetAssignment().GetExecutorInstanceId() == "" || r.GetAssignment().GetAssignmentGeneration() == 0 || r.GetAssignment().GetAssignmentGeneration() > math.MaxInt64 || r.GetObservedAtUnixMillis() <= 0 || r.GetCpuCount() == 0 {
		return fmt.Errorf("invalid measurement identity")
	}
	if math.IsNaN(float64(r.CpuUsedPercent)) || math.IsInf(float64(r.CpuUsedPercent), 0) || r.CpuUsedPercent < 0 || r.MemoryUsedBytes > r.MemoryTotalBytes || r.MemoryCacheBytes > r.MemoryTotalBytes || r.DiskUsedBytes > r.DiskTotalBytes || r.MemoryTotalBytes > math.MaxInt64 || r.DiskTotalBytes > math.MaxInt64 {
		return fmt.Errorf("invalid measurement values")
	}
	timestamp := time.UnixMilli(r.ObservedAtUnixMillis).UTC()
	sample := Sample{timestamp, timestamp.Unix(), r.CpuCount, r.CpuUsedPercent, r.MemoryUsedBytes, r.MemoryTotalBytes, r.MemoryCacheBytes, r.DiskUsedBytes, r.DiskTotalBytes}
	payload, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	// A late sample cannot bind to another incarnation or resurrect a tombstone.
	_, err = s.Pool.Exec(ctx, `INSERT INTO aenv_bridge.guest_metrics(tenant,external_id,actor_uid,instance_id,generation,observed_ms,sample) SELECT tenant,external_id,actor_uid,$4,$5,$6,$7 FROM aenv_bridge.sandboxes WHERE tenant=$1 AND external_id=$2 AND actor_uid=$3 AND NOT deleted ON CONFLICT DO NOTHING`, b.Tenant, b.ExternalID, b.ActorUID, r.Assignment.ExecutorInstanceId, int64(r.Assignment.AssignmentGeneration), r.ObservedAtUnixMillis, payload)
	return err
}
func (s *Store) History(ctx context.Context, b metadata.Sandbox, start, end *int64) ([]Sample, error) {
	if b.Tenant == "" || b.ExternalID == "" || b.ActorUID == "" {
		return nil, fmt.Errorf("confirmed sandbox identity required")
	}
	if start != nil && *start < 0 || end != nil && *end < 0 || start != nil && end != nil && *start > *end {
		return nil, fmt.Errorf("invalid metric interval")
	}
	rows, err := s.Pool.Query(ctx, `SELECT sample FROM aenv_bridge.guest_metrics WHERE tenant=$1 AND external_id=$2 AND actor_uid=$3 AND collected_at>clock_timestamp()-interval '1 hour' AND ($4::bigint IS NULL OR observed_ms/1000 >= $4) AND ($5::bigint IS NULL OR observed_ms/1000 <= $5) ORDER BY observed_ms,instance_id,generation`, b.Tenant, b.ExternalID, b.ActorUID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := []Sample{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var sample Sample
		if err = json.Unmarshal(raw, &sample); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}
func (s *Store) Prune(ctx context.Context) error {
	// Bound each sweep; reads exclude expired samples even before physical GC.
	_, err := s.Pool.Exec(ctx, `DELETE FROM aenv_bridge.guest_metrics WHERE ctid IN (SELECT ctid FROM aenv_bridge.guest_metrics WHERE collected_at<=clock_timestamp()-interval '1 hour' LIMIT 10000)`)
	return err
}

// Candidates uses a stable keyset cursor, so one bad or stopped Actor cannot
// starve subsequent sandboxes. Runtime state is always checked by Substrate.
func (s *Store) Candidates(ctx context.Context, tenant, id string) ([]metadata.Sandbox, error) {
	rows, err := s.Pool.Query(ctx, `SELECT tenant,external_id,actor_atespace,actor_name,actor_uid FROM aenv_bridge.sandboxes WHERE NOT deleted AND actor_uid IS NOT NULL AND (tenant,external_id)>($1,$2) ORDER BY tenant,external_id LIMIT 100`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.Sandbox
	for rows.Next() {
		var b metadata.Sandbox
		if err = rows.Scan(&b.Tenant, &b.ExternalID, &b.ActorAtespace, &b.ActorName, &b.ActorUID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Latest binds cached metrics to the current execution incarnation. A sample
// from before pause/restore cannot appear in the batch running-sandbox view.
func (s *Store) Latest(ctx context.Context, b metadata.Sandbox, a *pb.WorkerAssignment) (*Sample, error) {
	if b.Tenant == "" || b.ExternalID == "" || b.ActorUID == "" || a.GetExecutorInstanceId() == "" || a.GetAssignmentGeneration() == 0 || a.GetAssignmentGeneration() > math.MaxInt64 {
		return nil, fmt.Errorf("complete runtime metric identity required")
	}
	var wire []byte
	err := s.Pool.QueryRow(ctx, `SELECT m.sample FROM aenv_bridge.guest_metrics m JOIN aenv_bridge.sandboxes s USING(tenant,external_id,actor_uid) WHERE m.tenant=$1 AND m.external_id=$2 AND m.actor_uid=$3 AND m.instance_id=$4 AND m.generation=$5 AND NOT s.deleted AND m.collected_at>clock_timestamp()-interval '1 hour' ORDER BY m.observed_ms DESC LIMIT 1`, b.Tenant, b.ExternalID, b.ActorUID, a.ExecutorInstanceId, int64(a.AssignmentGeneration)).Scan(&wire)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sample Sample
	if err = json.Unmarshal(wire, &sample); err != nil {
		return nil, err
	}
	return &sample, nil
}
