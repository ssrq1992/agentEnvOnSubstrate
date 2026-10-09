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

// Command ate-generic-manifests replaces experimental Pod certificates in an
// installation manifest with stable Kubernetes token projections and agents.
// It only renders documents; it never contacts or modifies a cluster.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type settings struct{ issuer, image, roots string }

func (c settings) validate() error {
	u, e := url.Parse(c.issuer)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("HTTPS issuer authority required")
	}
	if c.image == "" || strings.ContainsAny(c.image, " \t\n") || len(validation.IsDNS1123Subdomain(c.roots)) != 0 {
		return fmt.Errorf("agent image and DNS ConfigMap name required")
	}
	return nil
}

var identityPaths = strings.NewReplacer(
	"/run/servicedns.podcert.ate.dev/credential-bundle.pem", "/run/ate-identity/credential-bundle.pem",
	"/run/podidentity.podcert.ate.dev/credential-bundle.pem", "/run/ate-identity/credential-bundle.pem",
	"/run/podidentity.podcert.ate.dev/trust-bundle.pem", "/run/ate-roots/podidentity-ca.crt",
	"/run/servicedns.podcert.ate.dev/trust-bundle.pem", "/run/ate-roots/servicedns-ca.crt",
	"/run/servicedns-ca/trust-bundle.pem", "/run/ate-roots/servicedns-ca.crt",
)

func rewrite(v any) any {
	switch x := v.(type) {
	case string:
		return identityPaths.Replace(x)
	case map[string]any:
		for k, v := range x {
			x[k] = rewrite(v)
		}
	case []any:
		for i, v := range x {
			x[i] = rewrite(v)
		}
	}
	return v
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func mount(name, path string, ro bool) any {
	return map[string]any{"name": name, "mountPath": path, "readOnly": ro}
}
func removeMounts(c map[string]any, removed map[string]bool) {
	var kept []any
	for _, m := range array(c["volumeMounts"]) {
		if !removed[fmt.Sprint(object(m)["name"])] {
			kept = append(kept, m)
		}
	}
	c["volumeMounts"] = append(kept, mount("ate-identity", "/run/ate-identity", true), mount("ate-roots", "/run/ate-roots", true))
}
func setArg(c map[string]any, key, value string) {
	args := []any{}
	for _, a := range array(c["args"]) {
		if !strings.HasPrefix(fmt.Sprint(a), key+"=") {
			args = append(args, a)
		}
	}
	c["args"] = append(args, key+"="+value)
}
func agent(c settings, once bool) any {
	name := "podidentity-renew"
	args := []any{"--issuer=" + c.issuer, "--roots=/run/ate-roots/issuer-ca.crt", "--bundle=/run/ate-identity/credential-bundle.pem", "--bundle-mode=0640"}
	if once {
		name = "podidentity-init"
		args = append(args, "--once")
	}
	return map[string]any{"name": name, "image": c.image, "args": args,
		"volumeMounts":    []any{mount("ate-identity", "/run/ate-identity", false), mount("ate-roots", "/run/ate-roots", true), mount("podidentity-token", "/run/podidentity-token", true)},
		"securityContext": map[string]any{"runAsUser": 65532, "runAsGroup": 65532, "runAsNonRoot": true, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		"resources":       map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]any{"memory": "128Mi"}}}
}
func transform(doc map[string]any, c settings) error {
	if doc["kind"] == "List" {
		for _, v := range array(doc["items"]) {
			if err := transform(object(v), c); err != nil {
				return err
			}
		}
		return nil
	}
	// ConfigMaps can contain Envoy file references. Keep those in sync with the
	// Pod's directory mounts without making certificate keys public.
	rewrite(doc)
	var pod map[string]any
	switch doc["kind"] {
	case "Deployment", "DaemonSet", "StatefulSet":
		pod = object(object(object(doc["spec"])["template"])["spec"])
	case "Pod":
		pod = object(doc["spec"])
	default:
		return nil
	}
	removed := map[string]bool{}
	volumes := []any{}
	hasCertificate := false
	for _, v := range array(pod["volumes"]) {
		vol := object(v)
		name := fmt.Sprint(vol["name"])
		replace := false
		for _, p := range array(object(vol["projected"])["sources"]) {
			for _, key := range []string{"podCertificate", "clusterTrustBundle"} {
				spec := object(object(p)[key])
				if spec == nil {
					continue
				}
				signer := fmt.Sprint(spec["signerName"])
				if signer != "podidentity.podcert.ate.dev/identity" && signer != "servicedns.podcert.ate.dev/identity" {
					return fmt.Errorf("unrecognized certificate signer %q", signer)
				}
				if key == "podCertificate" {
					hasCertificate = true
				}
				replace = true
			}
		}
		if replace {
			for _, source := range array(object(vol["projected"])["sources"]) {
				for key := range object(source) {
					if key != "podCertificate" && key != "clusterTrustBundle" {
						return fmt.Errorf("certificate volume %q also contains %q; split it before conversion", name, key)
					}
				}
			}
			removed[name] = true
		} else {
			volumes = append(volumes, v)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	if !hasCertificate {
		return fmt.Errorf("trust-only workload requires an explicit identity configuration")
	}
	for _, v := range volumes {
		switch object(v)["name"] {
		case "ate-identity", "ate-roots", "podidentity-token":
			return fmt.Errorf("identity volume name already in use")
		}
	}
	sc := object(pod["securityContext"])
	if sc == nil {
		sc = map[string]any{}
	}
	if g, ok := sc["fsGroup"]; ok && fmt.Sprint(g) != "65532" {
		return fmt.Errorf("existing fsGroup must be reconciled explicitly")
	}
	sc["fsGroup"] = 65532
	pod["securityContext"] = sc
	// No subPath: kubelet ConfigMap projection swaps must remain visible to
	// certificate and trust readers throughout rotation.
	volumes = append(volumes, map[string]any{"name": "ate-identity", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "4Mi"}}, map[string]any{"name": "ate-roots", "configMap": map[string]any{"name": c.roots}}, map[string]any{"name": "podidentity-token", "projected": map[string]any{"sources": []any{map[string]any{"serviceAccountToken": map[string]any{"audience": "podidentity.ate.dev", "expirationSeconds": 3600, "path": "token"}}}}})
	for _, v := range append(append([]any{}, array(pod["containers"])...), array(pod["initContainers"])...) {
		container := object(v)
		if container["name"] == "podidentity-init" || container["name"] == "podidentity-renew" {
			return fmt.Errorf("identity agent already present")
		}
		removeMounts(container, removed)
		switch container["name"] {
		case "ate-controller":
			setArg(container, "--configmap-trust-provider", "true")
		case "ate-api-server":
			setArg(container, "--experimental-enable-authz", "true")
		case "atelet":
			setArg(container, "--egress-trust-bundle-file", "/run/egress-mitm-trust/ca.crt")
			container["volumeMounts"] = append(array(container["volumeMounts"]), mount("egress-mitm-trust", "/run/egress-mitm-trust", true))
			volumes = append(volumes, map[string]any{"name": "egress-mitm-trust", "configMap": map[string]any{"name": "egress-mitm-trust"}})
		}
	}
	pod["volumes"] = volumes
	pod["initContainers"] = append([]any{agent(c, true)}, array(pod["initContainers"])...)
	pod["containers"] = append(array(pod["containers"]), agent(c, false))
	return nil
}

// Reject experimental resources anywhere, including unsupported workload kinds.
// A render that silently leaves a Job's certificate projection is not portable.
func checkStable(v any) error {
	switch x := v.(type) {
	case map[string]any:
		if x["kind"] == "ClusterTrustBundle" || x["kind"] == "PodCertificateRequest" {
			return fmt.Errorf("experimental certificate resource remains")
		}
		for key, value := range x {
			if key == "podCertificate" || key == "clusterTrustBundle" {
				return fmt.Errorf("experimental certificate projection remains")
			}
			if err := checkStable(value); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range x {
			if err := checkStable(item); err != nil {
				return err
			}
		}
	}
	return nil
}
func render(in io.Reader, out io.Writer, c settings) error {
	if err := c.validate(); err != nil {
		return err
	}
	dec := yaml.NewYAMLOrJSONDecoder(in, 4096)
	docs := []any{}
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(doc) == 0 {
			continue
		}
		if err = transform(doc, c); err != nil {
			return fmt.Errorf("%v/%v: %w", doc["kind"], object(doc["metadata"])["name"], err)
		}
		if err = checkStable(doc); err != nil {
			return err
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return fmt.Errorf("installation manifest is empty")
	}
	// Buffer all transformation before emitting anything: a rejected document
	// must not leave a partially rendered installation in a shell pipeline.
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"apiVersion": "v1", "kind": "List", "items": docs})
}
func main() {
	var c settings
	flag.StringVar(&c.issuer, "issuer", "", "HTTPS issuer endpoint")
	flag.StringVar(&c.image, "agent-image", "", "podidentityagent image")
	flag.StringVar(&c.roots, "trust-configmap", "ate-identity-roots", "ConfigMap with issuer-ca.crt, podidentity-ca.crt and servicedns-ca.crt")
	flag.Parse()
	if err := render(os.Stdin, os.Stdout, c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
