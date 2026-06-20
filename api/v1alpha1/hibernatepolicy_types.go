/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Weekday is a three-letter abbreviation for a day of the week.
// +kubebuilder:validation:Enum=Mon;Tue;Wed;Thu;Fri;Sat;Sun
type Weekday string

const (
	Monday    Weekday = "Mon"
	Tuesday   Weekday = "Tue"
	Wednesday Weekday = "Wed"
	Thursday  Weekday = "Thu"
	Friday    Weekday = "Fri"
	Saturday  Weekday = "Sat"
	Sunday    Weekday = "Sun"
)

// WorkloadType identifies a supported Kubernetes workload kind.
// +kubebuilder:validation:Enum=Deployment;StatefulSet;DaemonSet;ReplicaSet
type WorkloadType string

const (
	WorkloadTypeDeployment  WorkloadType = "Deployment"
	WorkloadTypeStatefulSet WorkloadType = "StatefulSet"
	WorkloadTypeDaemonSet   WorkloadType = "DaemonSet"
	WorkloadTypeReplicaSet  WorkloadType = "ReplicaSet"
)

// AvailabilityWindow defines a recurring time window during which hibernation is
// suppressed and any hibernated workloads are restored.
type AvailabilityWindow struct {
	// Weekdays lists the days of the week this window applies to.
	// +kubebuilder:validation:MinItems=1
	Weekdays []Weekday `json:"weekdays"`

	// From is the wall-clock start time (HH:MM) in Timezone.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	From string `json:"from"`

	// Until is the wall-clock end time (HH:MM) in Timezone.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	Until string `json:"until"`

	// Timezone is the IANA timezone name for this window (e.g. "Europe/Amsterdam").
	// +kubebuilder:default="UTC"
	Timezone string `json:"timezone"`
}

// HibernateAction describes what the controller does when hibernating workloads.
//
// SleepDaemonSet and MaxReplicas target completely different workload types and
// can therefore be combined freely: SleepDaemonSet hibernates DaemonSets while
// MaxReplicas caps Deployments, StatefulSets and ReplicaSets.
type HibernateAction struct {
	// SleepDaemonSet hibernates DaemonSets by injecting a non-schedulable
	// nodeSelector (greencosts.hstr.nl/hibernate: "true") into every DaemonSet
	// podTemplate, causing all existing pods to be evicted and no new pods to be
	// scheduled. The original nodeSelector is preserved in the annotation
	// greencosts.hstr.nl/original-nodeselector and restored on wake.
	//
	// This field has NO effect on Deployments, StatefulSets or ReplicaSets.
	// Use MaxReplicas (set to 0 or any cap) to hibernate those workload types.
	// +optional
	// +kubebuilder:default=false
	SleepDaemonSet bool `json:"sleepDaemonSet,omitempty"`

	// MaxReplicas caps each Deployment, StatefulSet and ReplicaSet to the given
	// replica count during hibernation. Workloads already at or below this value
	// are left unchanged (no-op). Set to 0 to scale them completely to zero.
	// When set, any HPA targeting an affected workload is suspended: positive
	// caps clamp minReplicas and maxReplicas to MaxReplicas, while a zero cap
	// temporarily detaches the HPA scaleTargetRef using a short deterministic
	// placeholder name. Original HPA bounds and targets are stored in annotations
	// for restoration on wake.
	//
	// This field has NO effect on DaemonSets. Use SleepDaemonSet to hibernate
	// DaemonSets.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`
}

// HibernatePolicySpec defines the desired state of HibernatePolicy.
type HibernatePolicySpec struct {
	// WorkloadTypes lists the workload kinds this policy will hibernate.
	// At least one type must be specified.
	// +kubebuilder:validation:MinItems=1
	WorkloadTypes []WorkloadType `json:"workloadTypes"`

	// AvailabilityWindows lists recurring time windows during which hibernation
	// is suppressed. Hibernated workloads are restored at the start of each window.
	// +optional
	AvailabilityWindows []AvailabilityWindow `json:"availabilityWindows,omitempty"`

	// Action defines what the controller does when hibernating workloads.
	Action HibernateAction `json:"action"`
}

// HibernatePolicyStatus defines the observed state of HibernatePolicy.
type HibernatePolicyStatus struct {
	// HibernatedWorkloads lists the workloads (Kind/name) currently scaled to
	// zero by this policy.
	// +optional
	HibernatedWorkloads []string `json:"hibernatedWorkloads,omitempty"`

	// Conditions reflect the current state of the HibernatePolicy reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=hp
// +kubebuilder:printcolumn:name="Hibernated",type=integer,JSONPath=`.status.hibernatedWorkloads`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HibernatePolicy is the Schema for the hibernatepolicies API.
// It is namespace-scoped and hibernates all workloads of the configured types
// within its own namespace outside of the configured availability windows.
type HibernatePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HibernatePolicySpec   `json:"spec,omitempty"`
	Status HibernatePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HibernatePolicyList contains a list of HibernatePolicy.
type HibernatePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HibernatePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HibernatePolicy{}, &HibernatePolicyList{})
}
