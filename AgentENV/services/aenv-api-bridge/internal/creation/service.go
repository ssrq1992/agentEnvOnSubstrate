// Package creation persists SDK launch intent and executes it through Substrate.
package creation

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/api/resource"
	"sort"
	"time"
)

type Template struct {
	NativeUID    string `json:"nativeUID,omitempty"`
	NativeDigest string `json:"nativeDigest,omitempty"`
	Tenant       string `json:"tenant"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Alias        string `json:"alias"`
	EnvdPort     int    `json:"envdPort"`
	EnvdVersion  string `json:"envdVersion"`
	CPUCount     int    `json:"cpuCount"`
	MemoryMB     int    `json:"memoryMB"`
	DiskSizeMB   int    `json:"diskSizeMB"`
}
type Input struct {
	cold       *coldPreparation
	TemplateID string `json:"templateID"`
	Timeout    *int   `json:"timeout,omitempty"`
	AutoPause  *bool  `json:"autoPause,omitempty"`
	AutoResume *struct {
		Enabled bool `json:"enabled"`
	} `json:"autoResume,omitempty"`
	Secure   *bool `json:"secure,omitempty"`
	Internet *bool `json:"allow_internet_access,omitempty"`
	Network  *struct {
		AllowPublicTraffic *bool    `json:"allowPublicTraffic,omitempty"`
		AllowOut           []string `json:"allowOut,omitempty"`
		DenyOut            []string `json:"denyOut,omitempty"`
	} `json:"network,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	EnvVars  map[string]string `json:"envVars,omitempty"`
	// These fields remain explicitly parsed until their typed execution path is added.
	CustomExtensionParams json.RawMessage `json:"customExtensionParams,omitempty"`
	MCP                   json.RawMessage `json:"mcp,omitempty"`
	VolumeMounts          json.RawMessage `json:"volumeMounts,omitempty"`
}
type Plan struct {
	Cold              bool `json:",omitempty"`
	SourceTag         *pb.ObjectRef
	SharedTemplateUID string
	SourceTagUID      string
	Template          []byte
	Access            metadata.Access
	Profile           metadata.Profile
	Policy            *pb.AgentENVNetworkPolicy
}
type Store interface {
	LockOperation(context.Context, string, string) (func(), error)
	Creation(context.Context, string, string) (metadata.Creation, error)
	ReserveCreation(context.Context, metadata.Sandbox, metadata.Request, []byte) (metadata.Creation, error)
	BindActor(context.Context, string, string, string) error
	PutAccess(context.Context, metadata.Sandbox, metadata.Access) error
	ConfirmCreated(context.Context, metadata.Creation, string, metadata.Profile) error
	ClaimCreations(context.Context, int) ([]metadata.Creation, error)
	Get(context.Context, string, string) (metadata.Sandbox, error)
}
type Control interface {
	Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error)
	EnsureTemplate(context.Context, auth.Tenant, *pb.ActorTemplate) error
	Create(context.Context, auth.Tenant, metadata.Sandbox, string) (*pb.Actor, error)
	InitialNetworkPolicy(context.Context, auth.Tenant, metadata.Sandbox, *pb.AgentENVNetworkPolicy) error
	Resume(context.Context, auth.Tenant, metadata.Sandbox) (*pb.Actor, error)
	Connect(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ConnectActorResponse, error)
	ExtensionParams(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ActorExtensionParams, error)
}
type Registry interface {
	Lookup(context.Context, string, string) (Template, error)
}

type Service struct {
	ColdTemplates map[string]string
	Images        ImageResolver
	Registry      Registry
	Store         Store
	Control       Control
	Templates     []Template
	Tenants       map[string]auth.Tenant
}

func (s *Service) Create(ctx context.Context, t auth.Tenant, in Input, key string, v2 bool) (metadata.Sandbox, *pb.ConnectActorResponse, error) {
	seconds := 15
	if v2 {
		seconds = 300
	}
	if in.Timeout != nil {
		seconds = *in.Timeout
	}
	if in.TemplateID == "" || seconds < 0 || uint64(seconds) > 4294967295 || v2 && seconds == 0 || len(key) > 200 {
		return metadata.Sandbox{}, nil, status.Error(codes.InvalidArgument, "invalid creation request")
	}
	// Never silently drop requested storage or extension behavior.
	for _, raw := range []json.RawMessage{in.VolumeMounts} {
		if len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" && string(raw) != "[]" {
			return metadata.Sandbox{}, nil, fmt.Errorf("typed volume launch configuration is required")
		}
	}
	var registered *Template
	if in.cold != nil {
		registered = &in.cold.Profile
	} else if s.Registry != nil {
		profile, err := s.Registry.Lookup(ctx, t.ID, in.TemplateID)
		if err != nil {
			return metadata.Sandbox{}, nil, err
		}
		if profile.Tenant != t.ID || profile.ID != in.TemplateID && profile.Alias != in.TemplateID {
			return metadata.Sandbox{}, nil, metadata.ErrNotFound
		}
		registered = &profile
	} else {
		for i := range s.Templates {
			p := &s.Templates[i]
			if p.Tenant == t.ID && (p.ID == in.TemplateID || p.Alias == in.TemplateID) {
				registered = p
				break
			}
		}
	}
	if registered == nil {
		return metadata.Sandbox{}, nil, metadata.ErrNotFound
	}
	var base *pb.ActorTemplate
	var err error
	if in.cold != nil {
		base = in.cold.Base
	} else {
		base, err = s.Control.Template(ctx, t, registered.Name)
	}
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	if registered.NativeUID != "" {
		digest, err := NativeTemplateDigest(base)
		if err != nil {
			return metadata.Sandbox{}, nil, err
		}
		if base.GetMetadata().GetUid() != registered.NativeUID || digest != registered.NativeDigest {
			return metadata.Sandbox{}, nil, status.Error(codes.FailedPrecondition, "registered native template identity or specification changed")
		}
	}
	if err := ValidateTemplateResources(*registered, base); err != nil {
		return metadata.Sandbox{}, nil, err
	}
	kind := "create"
	if in.cold != nil {
		kind = "cold"
	}
	id, actorName, err := creationIdentity(t.ID, key, kind)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	b := metadata.Sandbox{Tenant: t.ID, ExternalID: id, ActorAtespace: t.Atespace, ActorName: actorName, TemplateAlias: in.TemplateID, TimeoutSeconds: seconds}
	if in.cold != nil {
		b.TemplateAlias = "cold"
	}
	template := proto.CloneOf(base)
	template.Metadata = &pb.ResourceMetadata{Atespace: t.Atespace, Name: b.ActorName}
	template.Status = nil
	if len(in.CustomExtensionParams) > 0 && string(in.CustomExtensionParams) != "null" {
		var params map[string]json.RawMessage
		if json.Unmarshal(in.CustomExtensionParams, &params) != nil || params == nil {
			return b, nil, status.Error(codes.InvalidArgument, "extension parameters must be a JSON object")
		}
		canonical, _ := json.Marshal(params)
		if len(canonical) > 65536 {
			return b, nil, status.Error(codes.InvalidArgument, "extension parameters exceed limit")
		}
		template.Containers[0].AgentenvExtensionParams = string(canonical)
	}
	env := map[string]string{}
	for _, v := range template.Containers[0].Env {
		env[v.Name] = v.Value
	}
	for k, v := range in.EnvVars {
		if k == "" || len(k) > 256 || len(v) > 32768 {
			return b, nil, status.Error(codes.InvalidArgument, "invalid environment variable")
		}
		env[k] = v
	}
	template.Containers[0].Env = nil
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		template.Containers[0].Env = append(template.Containers[0].Env, &pb.EnvVar{Name: name, Value: env[name]})
	}
	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(template)
	if err != nil {
		return b, nil, err
	}
	access := metadata.Access{AutoPause: in.AutoPause == nil || *in.AutoPause, Secure: v2 || in.Secure != nil && *in.Secure, AllowPublicTraffic: true, AutoResume: in.AutoResume != nil && in.AutoResume.Enabled, EnvdPort: registered.EnvdPort}
	policy := &pb.AgentENVNetworkPolicy{}
	if in.Internet != nil {
		if *in.Internet {
			policy.Base = pb.AgentENVNetworkPolicy_ALLOW
		} else {
			policy.Base = pb.AgentENVNetworkPolicy_DENY
		}
	}
	if in.Network != nil {
		policy.AllowOut = in.Network.AllowOut
		policy.DenyOut = in.Network.DenyOut
		if in.Network.AllowPublicTraffic != nil {
			access.AllowPublicTraffic = *in.Network.AllowPublicTraffic
		}
	}
	plan := Plan{Cold: in.cold != nil, Template: wire, Access: access, Policy: policy, Profile: metadata.Profile{TemplateID: registered.ID, Alias: registered.Alias, EnvdVersion: registered.EnvdVersion, CPUCount: registered.CPUCount, MemoryMB: registered.MemoryMB, DiskSizeMB: registered.DiskSizeMB, Metadata: in.Metadata}}
	prepared, _ := json.Marshal(plan)
	payload := creationPayload(t.ID, in, v2)
	sum := sha256.Sum256(payload)
	r := metadata.Request{Tenant: t.ID, ExternalID: id, ID: "create-" + id, Kind: "create", Digest: hex.EncodeToString(sum[:])}
	intent, err := s.Store.ReserveCreation(ctx, b, r, prepared)
	if err != nil {
		return b, nil, err
	}
	if err = s.execute(ctx, t, intent); err != nil {
		return b, nil, err
	}
	b, err = s.Store.Get(ctx, b.Tenant, b.ExternalID)
	if err != nil {
		return b, nil, err
	}
	connection, err := s.Control.Connect(ctx, t, b)
	return b, connection, err
}
func (s *Service) execute(ctx context.Context, t auth.Tenant, i metadata.Creation) error {
	unlock, err := s.Store.LockOperation(ctx, i.Sandbox.Tenant, i.Sandbox.ExternalID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := s.Store.Creation(ctx, i.Sandbox.Tenant, i.Sandbox.ExternalID)
	if err != nil {
		return err
	}
	if current.Request.ID != i.Request.ID || current.Request.Digest != i.Request.Digest {
		return metadata.ErrConflict
	}
	i = current
	if i.Request.State == "completed" {
		return nil
	}
	if i.Request.State != "pending" || i.Sandbox.Tenant != t.ID || i.Sandbox.ActorAtespace != t.Atespace {
		return metadata.ErrConflict
	}
	var plan Plan
	if err := json.Unmarshal(i.Prepared, &plan); err != nil {
		return err
	}
	var template pb.ActorTemplate
	if err := proto.Unmarshal(plan.Template, &template); err != nil {
		return err
	}
	if template.GetMetadata().GetAtespace() != t.Atespace || plan.SourceTag == nil && template.GetMetadata().GetName() != i.Sandbox.ActorName {
		return metadata.ErrConflict
	}
	var actor *pb.Actor
	if plan.SourceTag != nil {
		if plan.SourceTag.GetAtespace() != t.Atespace || plan.SharedTemplateUID == "" {
			return metadata.ErrConflict
		}
		existing, err := s.Control.Template(ctx, t, template.Metadata.Name)
		if err != nil {
			return err
		}
		if existing.GetMetadata().GetUid() != plan.SharedTemplateUID {
			return metadata.ErrConflict
		}
		restore, ok := s.Control.(interface {
			CreateFromSnapshot(context.Context, auth.Tenant, metadata.Sandbox, string, *pb.ObjectRef, string) (*pb.Actor, error)
		})
		if !ok {
			return fmt.Errorf("snapshot creation control required")
		}
		actor, err = restore.CreateFromSnapshot(ctx, t, i.Sandbox, template.Metadata.Name, plan.SourceTag, plan.SourceTagUID)
	} else {
		if err := s.Control.EnsureTemplate(ctx, t, &template); err != nil {
			return err
		}
		actor, err = s.Control.Create(ctx, t, i.Sandbox, template.Metadata.Name)
	}
	if err != nil {
		return err
	}
	uid := actor.GetMetadata().GetUid()
	if uid == "" {
		return metadata.ErrConflict
	}
	if err = s.Store.BindActor(ctx, i.Sandbox.Tenant, i.Sandbox.ExternalID, uid); err != nil {
		return err
	}
	i.Sandbox.ActorUID = uid
	if err = s.Store.PutAccess(ctx, i.Sandbox, plan.Access); err != nil {
		return err
	}
	if err = s.Control.InitialNetworkPolicy(ctx, t, i.Sandbox, plan.Policy); err != nil {
		return err
	}
	actor, err = s.Control.Resume(ctx, t, i.Sandbox)
	if err != nil {
		return err
	}
	if actor.GetStatus().GetState() != pb.ActorState_ACTOR_STATE_RUNNING || actor.GetMetadata().GetUid() != uid {
		return metadata.ErrConflict
	}
	connection, err := s.Control.Connect(ctx, t, i.Sandbox)
	if err != nil {
		return err
	}
	if connection.GetActor().GetMetadata().GetUid() != uid || connection.GetEnvdVersion() == "" {
		return metadata.ErrConflict
	}
	// Observe actual snapshot/runtime parameters before starting the SDK timer.
	// The control-plane record then serves GET even after the first durable pause.
	params, err := s.Control.ExtensionParams(ctx, t, i.Sandbox)
	if err != nil {
		return err
	}
	var approved map[string]json.RawMessage
	if len(params.GetJson()) == 0 || len(params.GetJson()) > 65536 || json.Unmarshal([]byte(params.GetJson()), &approved) != nil || approved == nil {
		return metadata.ErrConflict
	}
	if plan.Cold {
		if connection.RootfsBytes == 0 || connection.RootfsBytes%(1024*1024) != 0 {
			return fmt.Errorf("actual cold root disk capacity required")
		}
		plan.Profile.DiskSizeMB = int(connection.RootfsBytes / (1024 * 1024))
	}
	plan.Profile.EnvdVersion = connection.EnvdVersion
	return s.Store.ConfirmCreated(ctx, i, uid, plan.Profile)
}
func (s *Service) Run(ctx context.Context, report func(error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		claims, err := s.Store.ClaimCreations(ctx, 10)
		if err != nil {
			report(err)
		} else {
			var failures []error
			for _, i := range claims {
				t, ok := s.Tenants[i.Sandbox.Tenant]
				if !ok {
					failures = append(failures, metadata.ErrConflict)
					continue
				}
				call, cancel := context.WithTimeout(ctx, 2*time.Minute)
				err = s.execute(call, t, i)
				cancel()
				if err != nil {
					failures = append(failures, err)
				}
			}
			if err = errors.Join(failures...); err != nil {
				report(err)
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) Validate() error {
	if s.Store == nil || s.Control == nil || s.Registry == nil && len(s.Templates) == 0 || len(s.Tenants) == 0 {
		return fmt.Errorf("creation dependencies and template profiles required")
	}
	seen := map[string]bool{}
	for _, p := range s.Templates {
		t, ok := s.Tenants[p.Tenant]
		if !ok || t.Atespace == "" || p.ID == "" || p.Name == "" || p.EnvdPort < 1 || p.EnvdPort > 65535 || p.EnvdVersion == "" || p.CPUCount < 1 || p.MemoryMB < 1 || p.DiskSizeMB < 1 {
			return fmt.Errorf("invalid template profile")
		}
		aliases := []string{p.ID}
		if p.Alias != p.ID {
			aliases = append(aliases, p.Alias)
		}
		for _, alias := range aliases {
			if alias == "" {
				continue
			}
			key := p.Tenant + "\x00" + alias
			if seen[key] {
				return fmt.Errorf("ambiguous template alias")
			}
			seen[key] = true
		}
	}
	return nil
}

// NativeTemplateDigest freezes the launch specification, excluding server metadata/status.
func NativeTemplateDigest(base *pb.ActorTemplate) (string, error) {
	if base == nil {
		return "", fmt.Errorf("native template required")
	}
	frozen := proto.CloneOf(base)
	frozen.Metadata = nil
	frozen.Status = nil
	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(frozen)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(wire)
	return hex.EncodeToString(sum[:]), nil
}

func ValidateTemplateResources(profile Template, base *pb.ActorTemplate) error {
	if base == nil || len(base.Containers) != 1 || profile.CPUCount < 1 || profile.MemoryMB < 1 || profile.DiskSizeMB < 1 || profile.EnvdVersion == "" || profile.EnvdPort < 1 || profile.EnvdPort > 65535 {
		return fmt.Errorf("complete native template profile required")
	}

	var cpu, memory int64
	for _, limit := range base.GetResources().GetLimits() {
		q, e := resource.ParseQuantity(limit.Quantity)
		if e != nil {
			return e
		}
		if limit.Name == "cpu" {
			cpu = q.MilliValue()
		}
		if limit.Name == "memory" {
			memory = q.Value()
		}
	}
	if cpu != int64(profile.CPUCount)*1000 || memory != int64(profile.MemoryMB)*1024*1024 {
		return status.Error(codes.FailedPrecondition, "template profile differs from explicit machine resources")
	}

	return nil
}
