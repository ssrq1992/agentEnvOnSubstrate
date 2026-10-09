package creation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agentenv/services/aenv-api-bridge/internal/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type coldRegistry struct{ profile Template }

func (r coldRegistry) Lookup(context.Context, string, string) (Template, error) {
	return r.profile, nil
}

type pinFixture struct {
	calls int
	value string
	fail  bool
}

func (p *pinFixture) Pin(context.Context, string) (string, error) {
	p.calls++
	if p.fail {
		return "", errors.New("unavailable registry")
	}
	return p.value, nil
}
func coldSetup(t *testing.T) (*Service, *storeStub, *controlStub, *pinFixture) {
	s, store, control, tnt := setup(t)
	s.ColdTemplates = map[string]string{tnt.ID: "template"}
	s.Registry = coldRegistry{s.Templates[0]}
	images := &pinFixture{value: "registry/image@sha256:" + strings.Repeat("a", 64)}
	s.Images = images
	return s, store, control, images
}
func TestColdCreateFreezesImageAndResourcesAcrossRetry(t *testing.T) {
	s, store, c, images := coldSetup(t)
	tnt := s.Tenants["tenant"]
	cpu, mem, disk := uint32(2), uint32(256), uint32(4096)
	sub := "data"
	ro := false
	in := ColdInput{Image: "registry/image:mutable", CPUCount: &cpu, MemoryMB: &mem, DiskSizeMB: &disk, EnvVars: map[string]string{"USER": "yes"}, ExtraBootArgs: "demo=ok", AttachedDrives: []AttachedDrive{{ID: "data", SubPath: &sub, ReadOnly: &ro}}}
	in.AttachedDrives[0].Source.Image = "registry/drive:mutable"
	c.fail = true
	b, _, err := s.CreateCold(t.Context(), tnt, in, "stable")
	if !errors.Is(err, context.DeadlineExceeded) || store.confirmed || images.calls != 2 {
		t.Fatal(b, err, images.calls)
	}
	launch := c.launch.GetContainers()[0]
	drive := launch.GetAgentenvLaunch().GetDrives()[0]
	if launch.Image != images.value || drive.Image != images.value || drive.SubPath != sub || drive.ReadOnly || launch.AgentenvLaunch.RootfsBytes != 4096*1024*1024 {
		t.Fatal("cold options lost", launch)
	}
	images.fail = true
	c.fail = false
	again, _, err := s.CreateCold(t.Context(), tnt, in, "stable")
	if err != nil || again.ExternalID != b.ExternalID || !store.confirmed || images.calls != 2 {
		t.Fatal("retry resolved mutable images", again, err, images.calls)
	}
	var plan Plan
	if err = json.Unmarshal(store.i.Prepared, &plan); err != nil || !plan.Cold || plan.Profile.CPUCount != 2 || plan.Profile.MemoryMB != 256 {
		t.Fatal(plan, err)
	}
	changed := in
	changed.Image = "another:tag"
	if _, _, err = s.CreateCold(t.Context(), tnt, changed, "stable"); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("retry changed frozen input", err)
	}
}
func TestColdDriveValidationBeforeImageResolution(t *testing.T) {
	s, _, _, images := coldSetup(t)
	tnt := s.Tenants["tenant"]
	for _, id := range []string{"rootfs", "user_rootfs", "agentenv_volume_slot_0", "bad-id", ""} {
		in := ColdInput{Image: "registry/image:tag", AttachedDrives: []AttachedDrive{{ID: id}}}
		in.AttachedDrives[0].Source.Image = "image"
		if _, _, err := s.CreateCold(t.Context(), tnt, in, ""); status.Code(err) != codes.InvalidArgument {
			t.Fatal(id, err)
		}
	}
	if images.calls != 0 {
		t.Fatal("invalid drives resolved images")
	}
	in := ColdInput{Image: "image", AttachedDrives: []AttachedDrive{{ID: "data"}}}
	in.AttachedDrives[0].Source.Image = "image"
	drives, err := coldDrives(in.AttachedDrives)
	if err != nil || drives[0].MountPath != "/mnt/data" || !drives[0].ReadOnly || drives[0].VirtualSize != 0 {
		t.Fatal(drives, err)
	}
	for _, sub := range []string{"", "/absolute", "a/../b", "with space"} {
		in.AttachedDrives[0].SubPath = &sub
		if _, err = coldDrives(in.AttachedDrives); status.Code(err) != codes.InvalidArgument {
			t.Fatal(sub, err)
		}
	}
}
