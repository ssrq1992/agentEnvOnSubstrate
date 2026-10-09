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
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveImagePinsRegistryManifest(t *testing.T) {
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":2},"layers":[]}`
	sum := sha256.Sum256([]byte(manifest))
	digest := fmt.Sprintf("sha256:%x", sum)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(200)
		case "/v2/demo/manifests/mutable":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", digest)
			fmt.Fprint(w, manifest)
		default:
			t.Errorf("unexpected registry request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/demo"
	pinned, err := resolveImage(context.Background(), repository+":mutable")
	if err != nil || pinned != repository+"@"+digest {
		t.Fatal(pinned, err)
	}
}
