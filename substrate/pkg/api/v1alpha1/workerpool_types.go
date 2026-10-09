// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkerPoolLabelValue is a Kubernetes label value for generated worker
// workloads.
//
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`
type WorkerPoolLabelValue string

// WorkerPoolPodTemplate defines optional metadata, scheduling, and resource
// settings for worker workloads. NodeAffinity is mapped to
// spec.affinity.nodeAffinity on the pod.
type WorkerPoolPodTemplate struct {
	// Labels are added to the generated Deployment and worker pods. Keys in
	// the ate.dev domain and its subdomains are reserved for controllers.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(key, !key.startsWith('ate.dev/') && !key.contains('.ate.dev/'))",message="ate.dev and its subdomains are reserved"
	// +kubebuilder:validation:XValidation:rule="self.all(key, !format.qualifiedName().validate(key).hasValue())",message="label keys must be valid Kubernetes qualified names"
	Labels map[string]WorkerPoolLabelValue `json:"labels,omitempty"`

	// Annotations are added to the generated Deployment and worker pods. Keys
	// in the ate.dev domain and its subdomains are reserved for controllers.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(key, !key.startsWith('ate.dev/') && !key.contains('.ate.dev/'))",message="ate.dev and its subdomains are reserved"
	// +kubebuilder:validation:XValidation:rule="self.all(key, !format.qualifiedName().validate(key).hasValue())",message="annotation keys must be valid Kubernetes qualified names"
	Annotations map[string]string `json:"annotations,omitempty"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	//
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for the worker pods.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PriorityClassName for the worker pods.
	//
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// NodeAffinity scheduling rules for the worker pods. Mapped to
	// spec.affinity.nodeAffinity on the pod.
	//
	// +optional
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`

	// Resources are the compute resources allocated for each worker pod.
	//
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName for the worker pods. The ServiceAccount must exist
	// in the WorkerPool's namespace. When omitted, the pods use the
	// namespace's default ServiceAccount.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	ServiceAccountName *string `json:"serviceAccountName,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.agentenv) || self.sandboxClass == 'agentenv'",message="agentenv configuration requires sandboxClass agentenv"
type WorkerPoolSpec struct {
	// AgentENV configures the embedded backend. Other classes ignore no settings:
	// the admission rule requires the AgentENV class when this is specified.
	// +optional
	AgentENV *AgentENVWorkerConfig `json:"agentenv,omitempty"`

	// PodIdentityIssuer selects stable-API certificate issuance for this pool.
	// When absent, Kubernetes certificate projection is used.
	// +optional
	PodIdentityIssuer *PodIdentityIssuerConfig `json:"podIdentityIssuer,omitempty"`
	// Replicas is the number of worker pods to run.
	// +required
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// WorkerImage is the ateom container image to deploy as workers.
	// +kubebuilder:validation:MinLength=1
	// +required
	WorkerImage string `json:"workerImage"`

	// Template holds optional metadata, scheduling, and resource settings for worker workloads.
	//
	// +optional
	Template *WorkerPoolPodTemplate `json:"template,omitempty"`

	// SandboxClass selects the sandbox runtime family for this pool, which drives
	// the worker pod shape (KVM/vhost device mounts and node placement). The
	// concrete binary is still selected by WorkerImage. Defaults to gvisor.
	// The sandbox binaries themselves come from the SandboxConfig each
	// ActorTemplate names (required).
	//
	// See Also: TODOs in ActorTemplate SandboxClass
	//
	// +optional
	// +kubebuilder:validation:Enum=gvisor;microvm;agentenv
	// +kubebuilder:default=gvisor
	SandboxClass SandboxClass `json:"sandboxClass,omitempty"`
}

// AgentENVWorkerConfig sets bounded Actor and kernel device counts for one embedded Worker.
type AgentENVWorkerConfig struct {
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16384
	// +kubebuilder:default=1
	MaxActors int32 `json:"maxActors,omitempty"`

	// MaxDevices counts all owned ublk devices, including shared and idle devices.
	// Unknown creation/deletion outcomes continue consuming the budget.
	// +optional
	// +kubebuilder:validation:Minimum=4
	// +kubebuilder:validation:Maximum=65536
	// +kubebuilder:default=64
	MaxDevices int32 `json:"maxDevices,omitempty"`
}

// PodIdentityIssuerConfig configures init/renewal containers. The ConfigMap
// must be in the pool namespace and contain issuer-ca.crt, podidentity-ca.crt,
// and servicedns-ca.crt. It contains public roots only.
type PodIdentityIssuerConfig struct {
	// +required
	// +kubebuilder:validation:Pattern=`^https://[^/?#]+$`
	Endpoint string `json:"endpoint"`
	// +required
	// +kubebuilder:validation:MinLength=1
	AgentImage string `json:"agentImage"`
	// +required
	// +kubebuilder:validation:MinLength=1
	TrustConfigMap string `json:"trustConfigMap"`
}

type WorkerPoolStatus struct {
	// Replicas is the total number of worker pods.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of ready worker pods.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Selector is the label selector for the worker pods.
	// +optional
	Selector string `json:"selector,omitempty"`
}

// WorkerPool is the Schema for the workerpools API
// +genclient
// +kubebuilder:object:generate=true
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=workerpool
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type WorkerPool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of WorkerPool
	// +required
	Spec WorkerPoolSpec `json:"spec"`

	// status is the observed state of WorkerPool
	// +optional
	Status WorkerPoolStatus `json:"status,omitempty"`
}

// WorkerPoolList contains a list of WorkerPools.
// +kubebuilder:object:generate=true
// +kubebuilder:object:root=true
type WorkerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkerPool{}, &WorkerPoolList{})
}
