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
	"k8s.io/apimachinery/pkg/api/resource"
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

// IgnorePeriod defines a recurring time window during which hibernation is suppressed.
type IgnorePeriod struct {
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

// HibernateSelector describes which namespaces are governed by this policy.
type HibernateSelector struct {
	// NamespaceSelector selects namespaces by label expressions.
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

// IdleDetection defines thresholds that determine when a namespace is considered idle.
type IdleDetection struct {
	// NoIngressRequestsFor is how long the namespace must have received zero
	// ingress HTTP requests before it is considered idle.
	// +optional
	NoIngressRequestsFor *metav1.Duration `json:"noIngressRequestsFor,omitempty"`

	// CPUBelow is the total CPU usage threshold below which the namespace is
	// considered idle (queried from the metrics-server).
	// +optional
	CPUBelow *resource.Quantity `json:"cpuBelow,omitempty"`

	// NetworkBelow is the total network throughput threshold below which the
	// namespace is considered idle (queried from Prometheus).
	// +optional
	NetworkBelow *resource.Quantity `json:"networkBelow,omitempty"`

	// IgnoreDuring lists time windows during which hibernation is suppressed.
	// Namespaces that were scaled down will be restored at the start of a window.
	// +optional
	IgnoreDuring []IgnorePeriod `json:"ignoreDuring,omitempty"`
}

// HibernateAction describes what happens when a namespace is determined to be idle.
type HibernateAction struct {
	// ScaleDeploymentsToZero scales all Deployments in matching namespaces to
	// zero replicas. Original replica counts are preserved in an annotation.
	// +optional
	ScaleDeploymentsToZero bool `json:"scaleDeploymentsToZero,omitempty"`

	// SnapshotPVCs creates a VolumeSnapshot for every PersistentVolumeClaim in
	// the namespace before scaling down.
	// +optional
	SnapshotPVCs bool `json:"snapshotPVCs,omitempty"`
}

// HibernatePolicySpec defines the desired state of HibernatePolicy.
type HibernatePolicySpec struct {
	// Selector identifies which namespaces this policy governs.
	Selector HibernateSelector `json:"selector"`

	// IdleDetection configures the conditions under which a namespace is
	// declared idle.
	IdleDetection IdleDetection `json:"idleDetection"`

	// Action defines what the controller does when idle conditions are met.
	Action HibernateAction `json:"action"`
}

// HibernatePolicyStatus defines the observed state of HibernatePolicy.
type HibernatePolicyStatus struct {
	// HibernatedNamespaces lists the namespaces currently scaled to zero by
	// this policy.
	// +optional
	HibernatedNamespaces []string `json:"hibernatedNamespaces,omitempty"`

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
// +kubebuilder:resource:scope=Cluster,shortName=hp
// +kubebuilder:printcolumn:name="Hibernated",type=integer,JSONPath=`.status.hibernatedNamespaces`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HibernatePolicy is the Schema for the hibernatepolicies API.
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
