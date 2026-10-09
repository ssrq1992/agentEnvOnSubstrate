package creation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RegistryImages uses the release-coupled Substrate resolver binary. Arguments
// are passed directly, never via a shell. Layer conversion stays on Workers.
type RegistryImages struct{ Command string }

func (r RegistryImages) Pin(ctx context.Context, image string) (string, error) {
	if image == "" || len(image) > 512 || strings.ContainsAny(image, " \t\r\n\x00") {
		return "", status.Error(codes.InvalidArgument, "invalid OCI image reference")
	}
	command := r.Command
	if command == "" {
		command = "/ate-image-resolve"
	}
	output, err := exec.CommandContext(ctx, command, "--image", image).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return "", status.Error(codes.InvalidArgument, "invalid OCI image reference")
		}
		return "", fmt.Errorf("resolve OCI image: %w", err)
	}
	pinned := strings.TrimSpace(string(output))
	name, digest, ok := strings.Cut(pinned, "@sha256:")
	if !ok || name == "" || strings.ContainsAny(name, " \t\r\n\x00@") || len(digest) != 64 || digest != strings.ToLower(digest) {
		return "", fmt.Errorf("invalid immutable image resolver result")
	}
	if _, err = hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("invalid immutable image digest")
	}
	return pinned, nil
}
