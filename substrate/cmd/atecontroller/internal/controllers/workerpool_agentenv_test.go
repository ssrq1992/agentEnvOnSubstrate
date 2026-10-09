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

package controllers

import (
	"encoding/json"
	atev1 "github.com/agent-substrate/substrate/pkg/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"testing"
)

func TestAgentENVPodShapeAndStableCertificates(t *testing.T) {
	wp := &atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "workers"}, Spec: atev1.WorkerPoolSpec{
		WorkerImage: "worker-image", SandboxClass: atev1.SandboxClassAgentENV,
		PodIdentityIssuer: &atev1.PodIdentityIssuerConfig{Endpoint: "https://issuer.ate-system.svc", AgentImage: "agent-image", TrustConfigMap: "roots"},
	}}
	deployment := buildDeploymentApplyConfig(wp, ateomOTelSettings{}, "ate-system", "atelet", "router")
	pod := deployment.Spec.Template.Spec
	wire, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "podCertificate") || strings.Contains(string(wire), "clusterTrustBundle") {
		t.Fatal("experimental projection remained in Pod")
	}
	if pod.NodeSelector["ate.dev/agentenv-capable"] != "true" {
		t.Fatal("missing dedicated node placement")
	}
	if len(pod.InitContainers) != 1 || len(pod.Containers) != 2 {
		t.Fatal("missing init/renewal containers")
	}
	found := false
	for _, c := range pod.Containers {
		if c.Name != nil && *c.Name == "ateom" {
			found = true
			if c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
				t.Fatal("AgentENV requires explicit privileged profile")
			}
			for _, arg := range c.Args {
				if arg == "--atunnel-trust-bundle=/run/podidentity-roots/podidentity-ca.crt" {
					return
				}
			}
			t.Fatal("Worker does not consume rotated ConfigMap roots")
		}
	}
	if !found {
		t.Fatal("missing Worker container")
	}
}
func TestExistingBackendsRemainUnprivileged(t *testing.T) {
	for _, class := range []atev1.SandboxClass{atev1.SandboxClassGvisor, atev1.SandboxClassMicroVM} {
		sc := ateomSecurityContext(class)
		if sc.Privileged == nil || *sc.Privileged {
			t.Fatalf("changed privilege of %s", class)
		}
	}
}

func TestAgentENVActorLimitIsExplicit(t *testing.T) {
	for _, limit := range []int32{0, 4} {
		wp := &atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "workers"}, Spec: atev1.WorkerPoolSpec{WorkerImage: "worker-image", SandboxClass: atev1.SandboxClassAgentENV}}
		expected := "--max-actors=1"
		if limit > 0 {
			wp.Spec.AgentENV = &atev1.AgentENVWorkerConfig{MaxActors: limit}
			expected = "--max-actors=4"
		}
		pod := buildDeploymentApplyConfig(wp, ateomOTelSettings{}, "ate-system", "atelet", "router").Spec.Template.Spec
		found := false
		for _, c := range pod.Containers {
			if c.Name != nil && *c.Name == "ateom" {
				for _, arg := range c.Args {
					if arg == expected {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing explicit Actor limit %s", expected)
		}
	}
}

func TestAgentENVDeviceLimitIsExplicit(t *testing.T) {
	for _, limit := range []int32{0, 128} {
		wp := &atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "workers"}, Spec: atev1.WorkerPoolSpec{WorkerImage: "worker-image", SandboxClass: atev1.SandboxClassAgentENV}}
		expected := "--max-devices=64"
		if limit > 0 {
			wp.Spec.AgentENV = &atev1.AgentENVWorkerConfig{MaxDevices: limit}
			expected = "--max-devices=128"
		}
		pod := buildDeploymentApplyConfig(wp, ateomOTelSettings{}, "ate-system", "atelet", "router").Spec.Template.Spec
		found := false
		for _, c := range pod.Containers {
			if c.Name != nil && *c.Name == "ateom" {
				for _, arg := range c.Args {
					if arg == expected {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing explicit device limit %s", expected)
		}
	}
}
