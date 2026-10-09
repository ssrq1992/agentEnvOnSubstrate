package creation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"agentenv/services/aenv-api-bridge/internal/auth"
	"agentenv/services/aenv-api-bridge/internal/metadata"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type AttachedDrive struct {
	ID     string `json:"driveID"`
	Source struct {
		Image string `json:"image"`
	} `json:"source"`
	MountPath  *string `json:"mountPath,omitempty"`
	ReadOnly   *bool   `json:"readOnly,omitempty"`
	DiskSizeMB *uint32 `json:"diskSizeMB,omitempty"`
	SubPath    *string `json:"subPath,omitempty"`
}

type ColdInput struct {
	Image      string `json:"image"`
	Timeout    *int   `json:"timeout,omitempty"`
	AutoPause  *bool  `json:"autoPause,omitempty"`
	AutoResume *struct {
		Enabled bool `json:"enabled"`
	} `json:"autoResume,omitempty"`
	Secure   *bool `json:"secure,omitempty"`
	Internet *bool `json:"allowInternetAccess,omitempty"`
	Network  *struct {
		AllowPublicTraffic *bool    `json:"allowPublicTraffic,omitempty"`
		AllowOut           []string `json:"allowOut,omitempty"`
		DenyOut            []string `json:"denyOut,omitempty"`
	} `json:"network,omitempty"`
	Metadata              map[string]string `json:"metadata,omitempty"`
	EnvVars               map[string]string `json:"envVars,omitempty"`
	CustomExtensionParams json.RawMessage   `json:"customExtensionParams,omitempty"`
	VolumeMounts          json.RawMessage   `json:"volumeMounts,omitempty"`
	CPUCount              *uint32           `json:"cpuCount,omitempty"`
	MemoryMB              *uint32           `json:"memoryMB,omitempty"`
	DiskSizeMB            *uint32           `json:"diskSizeMB,omitempty"`
	AttachedDrives        []AttachedDrive   `json:"attachedDrives,omitempty"`
	ExtraBootArgs         string            `json:"extraBootArgs,omitempty"`
}

type ImageResolver interface {
	Pin(context.Context, string) (string, error)
}
type coldPreparation struct {
	Profile Template
	Base    *pb.ActorTemplate
	Input   ColdInput
}

func creationIdentity(tenant, key, kind string) (string, string, error) {
	var bytes [16]byte
	if key == "" {
		if _, err := rand.Read(bytes[:]); err != nil {
			return "", "", err
		}
	} else {
		wire, _ := json.Marshal([]string{tenant, kind, key})
		sum := sha256.Sum256(wire)
		copy(bytes[:], sum[:16])
	}
	bytes[6] = (bytes[6] & 15) | 64
	bytes[8] = (bytes[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), "aenv-" + hex.EncodeToString(bytes[:]), nil
}

func creationPayload(tenant string, in Input, v2 bool) []byte {
	var cold *ColdInput
	if in.cold != nil {
		cold = &in.cold.Input
	}
	wire, _ := json.Marshal(struct {
		Tenant string
		V2     bool
		Input  Input
		Cold   *ColdInput `json:",omitempty"`
	}{tenant, v2, in, cold})
	return wire
}

func coldOptions(in ColdInput) Input {
	return Input{TemplateID: in.Image, Timeout: in.Timeout, AutoPause: in.AutoPause, AutoResume: in.AutoResume, Secure: in.Secure, Internet: in.Internet, Network: in.Network, Metadata: in.Metadata, EnvVars: in.EnvVars, CustomExtensionParams: in.CustomExtensionParams, VolumeMounts: in.VolumeMounts}
}

func (s *Service) CreateCold(ctx context.Context, t auth.Tenant, in ColdInput, key string) (metadata.Sandbox, *pb.ConnectActorResponse, error) {
	if len(key) > 200 || in.Image == "" || len(in.Image) > 512 || len(in.ExtraBootArgs) > 4096 || len(in.AttachedDrives) > 32 {
		return metadata.Sandbox{}, nil, status.Error(codes.InvalidArgument, "invalid cold launch")
	}
	options := coldOptions(in)
	options.cold = &coldPreparation{Input: in}
	if key != "" {
		id, _, err := creationIdentity(t.ID, key, "cold")
		if err != nil {
			return metadata.Sandbox{}, nil, err
		}
		existing, err := s.Store.Creation(ctx, t.ID, id)
		if err == nil {
			digest := sha256.Sum256(creationPayload(t.ID, options, false))
			if existing.Request.Digest != hex.EncodeToString(digest[:]) {
				return metadata.Sandbox{}, nil, metadata.ErrConflict
			}
			if existing.Sandbox.ActorAtespace != t.Atespace {
				return metadata.Sandbox{}, nil, metadata.ErrNotFound
			}
			if err = s.execute(ctx, t, existing); err != nil {
				return existing.Sandbox, nil, err
			}
			b, err := s.Store.Get(ctx, t.ID, id)
			if err != nil {
				return b, nil, err
			}
			connection, err := s.Control.Connect(ctx, t, b)
			return b, connection, err
		}
		if !errors.Is(err, metadata.ErrNotFound) {
			return metadata.Sandbox{}, nil, err
		}
	}
	reference := s.ColdTemplates[t.ID]
	if reference == "" || s.Images == nil || s.Registry == nil {
		return metadata.Sandbox{}, nil, status.Error(codes.Unavailable, "cold launch profile required")
	}
	profile, err := s.Registry.Lookup(ctx, t.ID, reference)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	if profile.Tenant != t.ID {
		return metadata.Sandbox{}, nil, metadata.ErrNotFound
	}
	base, err := s.Control.Template(ctx, t, profile.Name)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	actualDigest, err := NativeTemplateDigest(base)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	if profile.NativeUID != "" && (base.GetMetadata().GetUid() != profile.NativeUID || actualDigest != profile.NativeDigest) {
		return metadata.Sandbox{}, nil, metadata.ErrConflict
	}
	if err = ValidateTemplateResources(profile, base); err != nil {
		return metadata.Sandbox{}, nil, err
	}
	if in.CPUCount != nil {
		profile.CPUCount = int(*in.CPUCount)
	}
	if in.MemoryMB != nil {
		profile.MemoryMB = int(*in.MemoryMB)
	}
	if profile.CPUCount < 1 || profile.CPUCount >= 1000 || profile.MemoryMB < 128 {
		return metadata.Sandbox{}, nil, status.Error(codes.InvalidArgument, "invalid cold machine resources")
	}
	if in.DiskSizeMB != nil && (*in.DiskSizeMB < 1024 || *in.DiskSizeMB%1024 != 0) {
		return metadata.Sandbox{}, nil, status.Error(codes.InvalidArgument, "diskSizeMB must be at least 1024 and divisible by 1024")
	}
	// Validate all attached declarations before resolving any image.
	drives, err := coldDrives(in.AttachedDrives)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	image, err := s.Images.Pin(ctx, in.Image)
	if err != nil {
		return metadata.Sandbox{}, nil, err
	}
	for i := range drives {
		drives[i].Image, err = s.Images.Pin(ctx, drives[i].Image)
		if err != nil {
			return metadata.Sandbox{}, nil, err
		}
	}
	base = proto.CloneOf(base)
	base.Status = nil
	base.Volumes = nil
	base.Resources = &pb.Resources{Limits: []*pb.Limits{{Name: "cpu", Quantity: fmt.Sprint(profile.CPUCount)}, {Name: "memory", Quantity: fmt.Sprintf("%dMi", profile.MemoryMB)}}}
	container := base.Containers[0]
	container.Image = image
	container.Command = nil
	container.Args = nil
	container.Env = nil
	container.Resources = proto.CloneOf(base.Resources)
	container.VolumeMounts = nil
	container.AgentenvLaunch = &pb.AgentENVLaunchConfig{ExtraBootArgs: in.ExtraBootArgs, Drives: drives}
	if in.DiskSizeMB != nil {
		container.AgentenvLaunch.RootfsBytes = uint64(*in.DiskSizeMB) * 1024 * 1024
		profile.DiskSizeMB = int(*in.DiskSizeMB)
	}
	profile.ID = in.Image
	profile.Alias = ""
	profile.NativeUID = ""
	profile.NativeDigest = ""
	options.cold = &coldPreparation{Input: in, Profile: profile, Base: base}
	return s.Create(ctx, t, options, key, false)
}

func coldDrives(input []AttachedDrive) ([]*pb.AgentENVAttachedDrive, error) {
	drives := make([]*pb.AgentENVAttachedDrive, 0, len(input))
	ids := map[string]bool{}
	mounts := map[string]bool{}
	for _, drive := range input {
		id := strings.TrimSpace(drive.ID)
		valid := id != "" && len(id) <= 128 && id != "rootfs" && id != "user_rootfs" && !strings.HasPrefix(id, "agentenv_volume_slot_")
		for _, c := range id {
			valid = valid && ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_')
		}
		mount := "/mnt/" + id
		if drive.MountPath != nil && strings.TrimSpace(*drive.MountPath) != "" {
			mount = strings.TrimSpace(*drive.MountPath)
		}
		if !valid || ids[id] || !validDrivePath(mount, true) || mounts[mount] || strings.TrimSpace(drive.Source.Image) == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid or duplicate attached drive")
		}
		sub := ""
		if drive.SubPath != nil {
			sub = *drive.SubPath
			if sub == "" || !validDrivePath(sub, false) {
				return nil, status.Error(codes.InvalidArgument, "invalid attached drive subPath")
			}
		}
		size := uint64(0)
		if drive.DiskSizeMB != nil {
			if *drive.DiskSizeMB < 1024 || *drive.DiskSizeMB%1024 != 0 {
				return nil, status.Error(codes.InvalidArgument, "invalid attached drive diskSizeMB")
			}
			size = uint64(*drive.DiskSizeMB) * 1024 * 1024
		}
		ro := true
		if drive.ReadOnly != nil {
			ro = *drive.ReadOnly
		}
		ids[id] = true
		mounts[mount] = true
		drives = append(drives, &pb.AgentENVAttachedDrive{Id: id, Image: strings.TrimSpace(drive.Source.Image), MountPath: mount, ReadOnly: ro, VirtualSize: size, SubPath: sub})
	}
	return drives, nil
}
func validDrivePath(value string, absolute bool) bool {
	if value == "" || len(value) > 4096 || strings.HasPrefix(value, "/") != absolute || value == "/" || strings.ContainsAny(value, " \t\r\n,:\x00") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(value) != "."
}
