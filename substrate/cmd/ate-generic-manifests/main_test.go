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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func config() settings {
	return settings{"https://podidentity-issuer.ate-system.svc", "registry.invalid/agent@sha256:" + strings.Repeat("a", 64), "ate-identity-roots"}
}
func TestConvertControlAndDataPlaneManifests(t *testing.T) {
	for _, file := range []string{"ate-api-server.yaml", "ate-controller.yaml", "atelet.yaml", "atenet-router.yaml", "atenet-egress.yaml"} {
		t.Run(file, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("../../manifests/ate-install", file))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err = render(bytes.NewReader(input), &out, config()); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"\"podCertificate\"", "\"clusterTrustBundle\"", "/run/podidentity.podcert.ate.dev/", "/run/servicedns.podcert.ate.dev/", "/run/servicedns-ca/"} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("experimental dependency remains: %s", forbidden)
				}
			}
			var list map[string]any
			if err = json.Unmarshal(out.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			workloads := 0
			for _, d := range array(list["items"]) {
				doc := object(d)
				kind := doc["kind"]
				if kind != "Deployment" && kind != "DaemonSet" {
					continue
				}
				workloads++
				pod := object(object(object(doc["spec"])["template"])["spec"])
				init := array(pod["initContainers"])
				if len(init) == 0 || object(init[0])["name"] != "podidentity-init" {
					t.Fatal("initial certificate is not first init")
				}
				containers := array(pod["containers"])
				if object(containers[len(containers)-1])["name"] != "podidentity-renew" {
					t.Fatal("missing ordinary renewal sidecar")
				}
				volumes := map[string]bool{}
				for _, v := range array(pod["volumes"]) {
					name := object(v)["name"].(string)
					if volumes[name] {
						t.Fatal("duplicate volume", name)
					}
					volumes[name] = true
				}
				for _, v := range append(containers, init...) {
					for _, m := range array(object(v)["volumeMounts"]) {
						mount := object(m)
						if !volumes[mount["name"].(string)] {
							t.Fatal("dangling mount", mount)
						}
						if (mount["name"] == "ate-roots" || mount["name"] == "ate-identity") && mount["subPath"] != nil {
							t.Fatal("rotation hidden by subPath")
						}
					}
				}
			}
			if workloads == 0 {
				t.Fatal("test did not convert a workload")
			}
		})
	}
}
func TestRenderRejectsPartialAndUnsafeConversion(t *testing.T) {
	base := `apiVersion: v1
kind: Pod
metadata: {name: test}
spec:
  containers: [{name: app, image: test}]
  volumes:
  - name: identity
    projected:
      sources:
      - podCertificate: {signerName: podidentity.podcert.ate.dev/identity}
`
	for _, input := range []string{"", strings.ReplaceAll(base, "podidentity.podcert.ate.dev/identity", "unknown/ca"), base + "      - secret: {name: must-not-drop}\n", strings.Replace(base, "  containers:", "  securityContext: {fsGroup: 1000}\n  containers:", 1)} {
		var out bytes.Buffer
		if err := render(strings.NewReader(input), &out, config()); err == nil || out.Len() != 0 {
			t.Fatalf("unsafe conversion accepted: %s", input)
		}
	}
	var out bytes.Buffer
	c := config()
	c.issuer = "http://issuer"
	if err := render(strings.NewReader(base), &out, c); err == nil {
		t.Fatal("insecure issuer accepted")
	}
}
func TestInitOrderingAndSharedGroup(t *testing.T) {
	doc := map[string]any{"kind": "Pod", "spec": map[string]any{"initContainers": []any{map[string]any{"name": "needs-cert"}}, "containers": []any{map[string]any{"name": "app"}}, "volumes": []any{map[string]any{"name": "old", "projected": map[string]any{"sources": []any{map[string]any{"podCertificate": map[string]any{"signerName": "podidentity.podcert.ate.dev/identity"}}}}}}}}
	if err := transform(doc, config()); err != nil {
		t.Fatal(err)
	}
	pod := object(doc["spec"])
	init := array(pod["initContainers"])
	if object(init[0])["name"] != "podidentity-init" || object(init[1])["name"] != "needs-cert" {
		t.Fatal("initialization deadlock")
	}
	if object(pod["securityContext"])["fsGroup"] != 65532 {
		t.Fatal("main container cannot read rotated key")
	}
	encoded, _ := json.Marshal(init[0])
	if !strings.Contains(string(encoded), "--bundle-mode=0640") {
		t.Fatal("group permission missing")
	}
}
