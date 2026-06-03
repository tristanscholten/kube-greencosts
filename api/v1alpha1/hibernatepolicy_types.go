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
type HibernateAction struct {
	// ScaleToZero scales all selected workloads to zero replicas.
	// For Deployments, StatefulSets and ReplicaSets the original replica count is
	// preserved in the annotation greencosts.hstr.nl/original-replicas.
	// For DaemonSets a non-schedulable nodeSelector is injected and the original
	// nodeSelector is preserved in the annotation greencosts.hstr.nl/original-nodeselector.
	// +optional
	ScaleToZero bool `json:"scaleToZero,omitempty"`

	// SnapshotPVCs creates a VolumeSnapshot for every PersistentVolumeClaim in
	// the namespace before scaling down.
	// +optional
	SnapshotPVCs bool `json:"snapshotPVCs,omitempty"`
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
