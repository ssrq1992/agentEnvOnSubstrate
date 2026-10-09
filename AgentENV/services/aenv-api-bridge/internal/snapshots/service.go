// Package snapshots publishes SDK snapshot identities through native capture.
package snapshots

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Info struct {
	ID         string    `json:"snapshotID"`
	Names      []string  `json:"names"`
	CPUCount   int       `json:"cpuCount"`
	MemoryMB   int       `json:"memoryMB"`
	DiskSizeMB int       `json:"diskSizeMB"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
type Record struct {
	Tenant                                                  string
	Source                                                  metadata.Sandbox
	Info                                                    Info
	TagName, TagUID, TemplateName, TemplateUID, SnapshotURI string
	Generation                                              uint64
}
type plan struct {
	Record Record
	Alias  string
}
type Store interface {
	LockOperation(context.Context, string, string) (func(), error)
	Profile(context.Context, metadata.Sandbox) (metadata.Profile, error)
	ReserveCapture(context.Context, metadata.Sandbox, metadata.Request, []byte, string) (metadata.Capture, error)
	Capture(context.Context, metadata.Request) (metadata.Capture, error)
	ConfirmCapture(context.Context, metadata.Capture, string, string, json.RawMessage, time.Time) error
	ClaimCaptures(context.Context, int) ([]metadata.Capture, error)
	Snapshot(context.Context, string, string) (json.RawMessage, error)
	SnapshotPageFiltered(context.Context, string, *time.Time, string, int, string, string) ([]json.RawMessage, error)
}
type Control interface {
	Get(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
	Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error)
	Capture(context.Context, auth.Tenant, metadata.Sandbox, uint64, string) (*pb.Tag, error)
}
type Service struct {
	Store   Store
	Control Control
	Tenants map[string]auth.Tenant
}

func (s *Service) Create(ctx context.Context, t auth.Tenant, b metadata.Sandbox, alias, key string) (Info, error) {
	if len(key) > 200 || len(alias) > 256 || b.Tenant != t.ID || b.ActorAtespace != t.Atespace || b.ActorUID == "" {
		return Info{}, status.Error(codes.InvalidArgument, "invalid snapshot request")
	}
	for _, c := range alias {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return Info{}, status.Error(codes.InvalidArgument, "invalid snapshot name")
		}
	}
	if key == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Info{}, err
		}
		key = hex.EncodeToString(random[:])
	}
	op := sha256.Sum256([]byte(t.ID + "\x00" + b.ExternalID + "\x00" + key))
	payload, _ := json.Marshal([]string{t.ID, b.ExternalID, b.ActorUID, alias})
	digest := sha256.Sum256(payload)
	request := metadata.Request{Tenant: t.ID, ExternalID: b.ExternalID, ID: "capture-" + hex.EncodeToString(op[:]), Kind: "capture", Digest: hex.EncodeToString(digest[:])}
	existing, err := s.Store.Capture(ctx, request)
	if err == nil {
		return s.execute(ctx, t, existing)
	}
	if !errors.Is(err, metadata.ErrNotFound) {
		return Info{}, err
	}
	actor, err := s.Control.Get(ctx, t, b)
	if err != nil {
		return Info{}, err
	}
	if actor.GetMetadata().GetUid() != b.ActorUID || actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || actor.GetStatus().GetWorkerAssignment().GetAssignmentGeneration() == 0 {
		return Info{}, status.Error(codes.FailedPrecondition, "snapshot source must be running")
	}
	template, err := s.Control.Template(ctx, t, actor.GetActorTemplate().GetName())
	if err != nil {
		return Info{}, err
	}
	if template.GetMetadata().GetAtespace() != t.Atespace || template.GetMetadata().GetUid() == "" {
		return Info{}, metadata.ErrConflict
	}
	profile, err := s.Store.Profile(ctx, b)
	if err != nil {
		return Info{}, err
	}
	op[6] = (op[6] & 15) | 64
	op[8] = (op[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", op[:4], op[4:6], op[6:8], op[8:10], op[10:16])
	names := []string{}
	if alias != "" {
		names = append(names, alias)
	}
	p := plan{Alias: alias, Record: Record{Tenant: t.ID, Source: b, Generation: actor.Status.WorkerAssignment.AssignmentGeneration, TagName: "snapshot-" + hex.EncodeToString(op[:20]), TemplateName: template.Metadata.Name, TemplateUID: template.Metadata.Uid, Info: Info{ID: id, Names: names, CPUCount: profile.CPUCount, MemoryMB: profile.MemoryMB, DiskSizeMB: profile.DiskSizeMB}}}
	wire, err := json.Marshal(p)
	if err != nil {
		return Info{}, err
	}
	job, err := s.Store.ReserveCapture(ctx, b, request, wire, alias)
	if err != nil {
		return Info{}, err
	}
	return s.execute(ctx, t, job)
}
func (s *Service) execute(ctx context.Context, t auth.Tenant, job metadata.Capture) (Info, error) {
	unlock, err := s.Store.LockOperation(ctx, t.ID, job.Request.ExternalID)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	job, err = s.Store.Capture(ctx, job.Request)
	if err != nil {
		return Info{}, err
	}
	if job.Request.State == "completed" {
		var record Record
		err = json.Unmarshal(job.Request.Result, &record)
		return record.Info, err
	}
	if job.Request.State != "pending" {
		return Info{}, metadata.ErrConflict
	}
	var p plan
	if err = json.Unmarshal(job.Prepared, &p); err != nil {
		return Info{}, err
	}
	record := p.Record
	if record.Tenant != t.ID || record.Source.ActorAtespace != t.Atespace || record.Source.ActorUID != job.Source.ActorUID || record.Source.ExternalID != job.Request.ExternalID || record.Source.ActorName != job.Source.ActorName || record.Generation == 0 {
		return Info{}, metadata.ErrConflict
	}
	tag, err := s.Control.Capture(ctx, t, record.Source, record.Generation, record.TagName)
	if err != nil {
		return Info{}, err
	}
	if tag.GetMetadata().GetUid() == "" || tag.GetMetadata().GetAtespace() != t.Atespace || tag.GetMetadata().GetName() != record.TagName || tag.GetStatus().GetActorTemplateUid() != record.TemplateUID || tag.GetStatus().GetCaptureActorUid() != record.Source.ActorUID || tag.GetStatus().GetCaptureAssignment().GetAssignmentGeneration() != record.Generation || tag.GetStatus().GetSnapshot().GetContentScope() != pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL || tag.GetStatus().GetSnapshot().GetSnapshotUri() == "" {
		return Info{}, metadata.ErrConflict
	}
	created, updated := tag.Metadata.GetCreateTime(), tag.Metadata.GetUpdateTime()
	if created == nil || updated == nil || created.CheckValid() != nil || updated.CheckValid() != nil || updated.AsTime().Before(created.AsTime()) {
		return Info{}, fmt.Errorf("committed snapshot timestamps required")
	}
	record.TagUID = tag.Metadata.Uid
	record.SnapshotURI = tag.Status.Snapshot.SnapshotUri
	record.Info.CreatedAt = created.AsTime()
	record.Info.UpdatedAt = updated.AsTime()
	wire, err := json.Marshal(record)
	if err != nil {
		return Info{}, err
	}
	if err = s.Store.ConfirmCapture(ctx, job, record.Info.ID, record.TagUID, wire, record.Info.CreatedAt); err != nil {
		return Info{}, err
	}
	return record.Info, nil
}
func (s *Service) Get(ctx context.Context, tenant, id string) (Info, error) {
	wire, err := s.Store.Snapshot(ctx, tenant, id)
	if err != nil {
		return Info{}, err
	}
	var record Record
	if err = json.Unmarshal(wire, &record); err != nil {
		return Info{}, err
	}
	if record.Tenant != tenant {
		return Info{}, metadata.ErrNotFound
	}
	return record.Info, nil
}
func (s *Service) Page(ctx context.Context, tenant string, before *time.Time, id string, limit int, source, name string) ([]Info, error) {
	records, err := s.Store.SnapshotPageFiltered(ctx, tenant, before, id, limit, source, name)
	if err != nil {
		return nil, err
	}
	out := []Info{}
	for _, wire := range records {
		var record Record
		if err = json.Unmarshal(wire, &record); err != nil {
			return nil, err
		}
		if record.Tenant != tenant {
			return nil, metadata.ErrNotFound
		}
		out = append(out, record.Info)
	}
	return out, nil
}
func (s *Service) Run(ctx context.Context, onError func(error)) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		jobs, err := s.Store.ClaimCaptures(ctx, 10)
		if err != nil && onError != nil {
			onError(err)
		}
		for _, job := range jobs {
			tenant, ok := s.Tenants[job.Request.Tenant]
			if !ok {
				if onError != nil {
					onError(metadata.ErrConflict)
				}
				continue
			}
			call, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_, err = s.execute(call, tenant, job)
			cancel()
			if err != nil && onError != nil {
				onError(err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
