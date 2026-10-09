package creation

import (
	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"context"
	"encoding/json"
	"errors"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

type storeStub struct {
	i                 metadata.Creation
	confirmed, access bool
}

func (s *storeStub) ReserveCreation(_ context.Context, b metadata.Sandbox, r metadata.Request, p []byte) (metadata.Creation, error) {
	if s.i.Request.ID != "" {
		if s.i.Request.Digest != r.Digest {
			return metadata.Creation{}, metadata.ErrConflict
		}
		return s.i, nil
	}
	r.State = "pending"
	s.i = metadata.Creation{Sandbox: b, Request: r, Prepared: append([]byte(nil), p...)}
	return s.i, nil
}
func (s *storeStub) BindActor(_ context.Context, _, _, uid string) error {
	if s.i.Sandbox.ActorUID != "" && s.i.Sandbox.ActorUID != uid {
		return metadata.ErrConflict
	}
	s.i.Sandbox.ActorUID = uid
	return nil
}
func (s *storeStub) PutAccess(context.Context, metadata.Sandbox, metadata.Access) error {
	s.access = true
	return nil
}
func (s *storeStub) ConfirmCreated(_ context.Context, _ metadata.Creation, _ string, _ metadata.Profile) error {
	s.confirmed = true
	s.i.Request.State = "completed"
	return nil
}
func (s *storeStub) ClaimCreations(context.Context, int) ([]metadata.Creation, error) {
	return nil, nil
}
func (s *storeStub) Get(context.Context, string, string) (metadata.Sandbox, error) {
	return s.i.Sandbox, nil
}

type controlStub struct {
	template         *pb.ActorTemplate
	launch           *pb.ActorTemplate
	fail             bool
	creates, resumes int
	policy           bool
	extensionErr     error
	extensionReads   int
}

func (c *controlStub) Template(context.Context, auth.Tenant, string) (*pb.ActorTemplate, error) {
	return c.template, nil
}
func (c *controlStub) EnsureTemplate(_ context.Context, _ auth.Tenant, p *pb.ActorTemplate) error {
	c.launch = proto.CloneOf(p)
	return nil
}
func (c *controlStub) actor(b metadata.Sandbox) *pb.Actor {
	return &pb.Actor{Metadata: &pb.ResourceMetadata{Uid: "uid", Atespace: b.ActorAtespace, Name: b.ActorName}, Status: &pb.ActorStatus{State: pb.ActorState_ACTOR_STATE_RUNNING}, ActorTemplate: &pb.ObjectRef{Name: b.ActorName}}
}
func (c *controlStub) Create(_ context.Context, _ auth.Tenant, b metadata.Sandbox, _ string) (*pb.Actor, error) {
	c.creates++
	return c.actor(b), nil
}
func (c *controlStub) InitialNetworkPolicy(context.Context, auth.Tenant, metadata.Sandbox, *pb.AgentENVNetworkPolicy) error {
	c.policy = true
	return nil
}
func (c *controlStub) Resume(_ context.Context, _ auth.Tenant, b metadata.Sandbox) (*pb.Actor, error) {
	c.resumes++
	if !c.policy {
		panic("started without initial network policy")
	}
	if c.fail {
		return nil, context.DeadlineExceeded
	}
	return c.actor(b), nil
}
func (c *controlStub) Connect(_ context.Context, _ auth.Tenant, b metadata.Sandbox) (*pb.ConnectActorResponse, error) {
	return &pb.ConnectActorResponse{RootfsBytes: 2 * 1024 * 1024 * 1024, Actor: c.actor(b), EnvdVersion: "live-envd", EnvdAccessToken: "runtime-secret"}, nil
}
func (c *controlStub) ExtensionParams(context.Context, auth.Tenant, metadata.Sandbox) (*pb.ActorExtensionParams, error) {
	c.extensionReads++
	return &pb.ActorExtensionParams{Json: `{"inherited":true}`}, c.extensionErr
}
func setup(t *testing.T) (*Service, *storeStub, *controlStub, auth.Tenant) {
	t.Helper()
	store := &storeStub{}
	control := &controlStub{template: &pb.ActorTemplate{Metadata: &pb.ResourceMetadata{Atespace: "space", Name: "base", Uid: "template-uid"}, Containers: []*pb.Container{{Name: "vm", Image: "image@sha256:abc", Env: []*pb.EnvVar{{Name: "BASE", Value: "yes"}}}}, Resources: &pb.Resources{Limits: []*pb.Limits{{Name: "cpu", Quantity: "1"}, {Name: "memory", Quantity: "128Mi"}}}}}
	tenant := auth.Tenant{ID: "tenant", Atespace: "space"}
	return &Service{Store: store, Control: control, Templates: []Template{{Tenant: "tenant", ID: "template", Name: "base", EnvdPort: 49983, EnvdVersion: "template-envd", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024}}, Tenants: map[string]auth.Tenant{"tenant": tenant}}, store, control, tenant
}
func TestCreationReplaysFrozenLaunchAndCommitsOnlyAfterRunning(t *testing.T) {
	s, store, c, tnt := setup(t)
	c.fail = true
	in := Input{TemplateID: "template", EnvVars: map[string]string{"USER": "yes"}}
	b, _, err := s.Create(t.Context(), tnt, in, "key", true)
	if !errors.Is(err, context.DeadlineExceeded) || store.confirmed {
		t.Fatal("unknown activation was confirmed", err)
	}
	if b.ExternalID == "" || !store.access {
		t.Fatal("creation identity/options not retained")
	}
	var plan Plan
	if err = json.Unmarshal(store.i.Prepared, &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.Access.Secure || !plan.Access.AutoPause {
		t.Fatal("v2 defaults incorrect")
	}
	c.template.Containers[0].Env[0].Value = "changed"
	c.fail = false
	b2, conn, err := s.Create(t.Context(), tnt, in, "key", true)
	if err != nil || b2.ExternalID != b.ExternalID || !store.confirmed || conn.EnvdAccessToken != "runtime-secret" {
		t.Fatal("retry failed", err)
	}
	if c.launch.Containers[0].Env[0].Value != "yes" {
		t.Fatal("retry did not use original frozen template")
	}
	in.EnvVars["USER"] = "changed"
	if _, _, err = s.Create(t.Context(), tnt, in, "key", true); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("idempotency key replaced input", err)
	}
}
func TestCreationDefaultsAndResourceProfile(t *testing.T) {
	s, store, c, tnt := setup(t)
	if _, _, err := s.Create(t.Context(), tnt, Input{TemplateID: "template"}, "", false); err != nil {
		t.Fatal(err)
	}
	var plan Plan
	_ = json.Unmarshal(store.i.Prepared, &plan)
	if plan.Access.Secure || !plan.Access.AllowPublicTraffic || !plan.Access.AutoPause || store.i.Sandbox.TimeoutSeconds != 15 {
		t.Fatal("v1 defaults incorrect")
	}
	s, _, c, tnt = setup(t)
	c.template.Resources.Limits[0].Quantity = "2"
	if _, _, err := s.Create(t.Context(), tnt, Input{TemplateID: "template"}, "", true); err == nil {
		t.Fatal("profile mismatch accepted")
	}
}

func (s *storeStub) LockOperation(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (s *storeStub) Creation(context.Context, string, string) (metadata.Creation, error) {
	if s.i.Request.ID == "" {
		return metadata.Creation{}, metadata.ErrNotFound
	}
	return s.i, nil
}

type restoreControl struct {
	*controlStub
	source       *pb.ObjectRef
	templateName string
}

func (c *restoreControl) CreateFromSnapshot(_ context.Context, _ auth.Tenant, b metadata.Sandbox, template string, source *pb.ObjectRef, sourceUID string) (*pb.Actor, error) {
	c.creates++
	c.source = proto.CloneOf(source)
	c.templateName = template
	return c.actor(b), nil
}
func TestSnapshotChildPreservesTemplateAndIndependentConfirmation(t *testing.T) {
	s, store, base, tenant := setup(t)
	control := &restoreControl{controlStub: base}
	s.Control = control
	child := metadata.Sandbox{Tenant: tenant.ID, ExternalID: "child", ActorAtespace: tenant.Atespace, ActorName: "child-actor", TemplateAlias: "template", TimeoutSeconds: 300}
	tag := &pb.Tag{Metadata: &pb.ResourceMetadata{Atespace: tenant.Atespace, Name: "captured", Uid: "tag-uid"}, Status: &pb.TagStatus{ActorTemplateUid: base.template.Metadata.Uid, Snapshot: &pb.ExternalSnapshot{ContentScope: pb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_FULL, SnapshotUri: "s3://bucket/captured"}}}
	access := metadata.Access{EnvdPort: 49983, AutoPause: true, Secure: true}
	profile := metadata.Profile{TemplateID: "template", CPUCount: 1, MemoryMB: 128, DiskSizeMB: 1024, EnvdVersion: "envd"}
	base.fail = true
	if _, _, err := s.Restore(t.Context(), tenant, child, tag, base.template, access, profile, &pb.AgentENVNetworkPolicy{}); !errors.Is(err, context.DeadlineExceeded) || store.confirmed {
		t.Fatal("unconfirmed child committed", err)
	}
	if control.launch != nil || control.templateName != "base" || !proto.Equal(control.source, &pb.ObjectRef{Atespace: tenant.Atespace, Name: "captured"}) {
		t.Fatal("snapshot template cloned or source lost")
	}
	base.fail = false
	if _, _, err := s.Restore(t.Context(), tenant, child, tag, base.template, access, profile, &pb.AgentENVNetworkPolicy{}); err != nil || !store.confirmed {
		t.Fatal("child retry failed", err)
	}
	if control.launch != nil {
		t.Fatal("child replaced captured template")
	}
}

type cancellationStore struct {
	*storeStub
	deleted bool
}

func (s *cancellationStore) RejectSnapshotCreation(_ context.Context, b metadata.Sandbox, tagUID string) (metadata.Sandbox, error) {
	if tagUID != "tag-uid" || s.i.Sandbox.ActorUID == "" || s.i.Request.State == "completed" {
		return b, metadata.ErrConflict
	}
	s.i.Request.State = "rejected"
	b = s.i.Sandbox
	b.Deleted = s.deleted
	return b, nil
}
func (s *cancellationStore) ConfirmSnapshotCreationFailed(context.Context, metadata.Sandbox) error {
	s.deleted = true
	return nil
}

type cancellationControl struct {
	*controlStub
	failDelete bool
	deletedUID string
}

func (c *cancellationControl) Delete(_ context.Context, _ auth.Tenant, b metadata.Sandbox) error {
	c.deletedUID = b.ActorUID
	if c.failDelete {
		return context.DeadlineExceeded
	}
	return nil
}
func TestSnapshotCleanupRetainsUnknownDeletion(t *testing.T) {
	s, baseStore, baseControl, tenant := setup(t)
	store := &cancellationStore{storeStub: baseStore}
	control := &cancellationControl{controlStub: baseControl, failDelete: true}
	s.Store = store
	s.Control = control
	child := metadata.Sandbox{Tenant: tenant.ID, ExternalID: "child", ActorAtespace: tenant.Atespace, ActorName: "child", ActorUID: "child-uid"}
	baseStore.i = metadata.Creation{Sandbox: child, Request: metadata.Request{Tenant: tenant.ID, ExternalID: child.ExternalID, ID: "create-child", State: "pending", Digest: "digest"}}
	if err := s.Cancel(t.Context(), tenant, child, "tag-uid"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if store.deleted || baseStore.i.Request.State != "rejected" || control.deletedUID != "child-uid" {
		t.Fatal("unknown cleanup committed or unfenced")
	}
	if err := s.execute(t.Context(), tenant, baseStore.i); !errors.Is(err, metadata.ErrConflict) || baseControl.creates != 0 {
		t.Fatal("rejected child recreated", err)
	}
	control.failDelete = false
	if err := s.Cancel(t.Context(), tenant, child, "tag-uid"); err != nil || !store.deleted {
		t.Fatal("cleanup retry failed", err)
	}
}

func TestCreationObservesExtensionStateBeforeStartingTimer(t *testing.T) {
	s, st, c, tnt := setup(t)
	c.extensionErr = context.DeadlineExceeded
	_, _, err := s.Create(t.Context(), tnt, Input{TemplateID: "template"}, "extensions", true)
	if !errors.Is(err, context.DeadlineExceeded) || st.confirmed || c.extensionReads != 1 {
		t.Fatal("creation completed without approved runtime parameters", err)
	}
	c.extensionErr = nil
	if _, _, err = s.Create(t.Context(), tnt, Input{TemplateID: "template"}, "extensions", true); err != nil || !st.confirmed || c.extensionReads != 2 {
		t.Fatal("extension observation not retried", err)
	}
}

type registryStub struct {
	profile Template
	err     error
	calls   int
}

func (r *registryStub) Lookup(_ context.Context, tenant, ref string) (Template, error) {
	r.calls++
	return r.profile, r.err
}
func TestPersistedTemplateRejectsReplacedNativeArtifact(t *testing.T) {
	s, store, control, tenant := setup(t)
	p := s.Templates[0]
	p.NativeUID = control.template.Metadata.Uid
	var err error
	p.NativeDigest, err = NativeTemplateDigest(control.template)
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{profile: p}
	s.Registry = registry
	s.Templates = nil
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	control.template.Metadata.Uid = "replacement"
	if _, _, err := s.Create(t.Context(), tenant, Input{TemplateID: p.ID}, "key", false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("replacement accepted", err)
	}
	if store.i.Request.ID != "" {
		t.Fatal("creation reserved before native identity check")
	}
	control.template.Metadata.Uid = p.NativeUID
	control.template.Containers[0].Image = "changed@sha256:abc"
	if _, _, err := s.Create(t.Context(), tenant, Input{TemplateID: p.ID}, "key", false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("changed specification accepted", err)
	}
}
func TestPersistedTemplateCannotCrossTenant(t *testing.T) {
	s, _, _, tenant := setup(t)
	p := s.Templates[0]
	p.Tenant = "other"
	s.Registry = &registryStub{profile: p}
	if _, _, err := s.Create(t.Context(), tenant, Input{TemplateID: p.ID}, "key", false); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("foreign profile accepted", err)
	}
}
