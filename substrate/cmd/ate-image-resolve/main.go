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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func resolveImage(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	resolved, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
	if err != nil {
		return "", err
	}
	digest, err := resolved.Digest()
	if err != nil {
		return "", err
	}
	if digest.Algorithm != "sha256" {
		return "", fmt.Errorf("SHA256 manifest required")
	}
	return ref.Context().Digest(digest.String()).Name(), nil
}

func main() {
	image := flag.String("image", "", "OCI image to pin for Linux amd64")
	flag.Parse()
	if *image == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--image is required")
		os.Exit(2)
	}
	if _, err := name.ParseReference(*image); err != nil {
		fmt.Fprintln(os.Stderr, "invalid OCI image reference")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, time.Minute)
	defer deadline()
	pinned, err := resolveImage(ctx, *image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OCI image resolution failed:", err)
		os.Exit(1)
	}
	fmt.Println(pinned)
}
