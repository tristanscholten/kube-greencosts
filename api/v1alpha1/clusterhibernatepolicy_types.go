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

// ClusterHibernatePolicySpec defines the desired state of ClusterHibernatePolicy.
//
// A ClusterHibernatePolicy is not configured for specific workload types; instead
// it is attached to individual workloads or namespaces via the annotation
//
//	greencosts.hstr.nl/clusterhibernatepolicy: "<policy-name>"
//
// When the annotation is placed on a Namespace, all Deployments, StatefulSets,
// DaemonSets and ReplicaSets inside that namespace are governed by the policy.
// When placed on an individual workload resource, only that resource is governed.
type ClusterHibernatePolicySpec struct {
	// AvailabilityWindows lists recurring time windows during which hibernation
	// is suppressed. Hibernated workloads are restored at the start of each window.
	// +optional
	AvailabilityWindows []AvailabilityWindow `json:"availabilityWindows,omitempty"`

	// Action defines what the controller does when hibernating workloads.
	Action HibernateAction `json:"action"`
}

// ClusterHibernatePolicyStatus defines the observed state of ClusterHibernatePolicy.
type ClusterHibernatePolicyStatus struct {
	// HibernatedWorkloads lists the workloads (namespace/Kind/name) currently
	// scaled to zero by this policy.
	// +optional
	HibernatedWorkloads []string `json:"hibernatedWorkloads,omitempty"`

	// Conditions reflect the current state of the ClusterHibernatePolicy reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=chp
// +kubebuilder:printcolumn:name="Hibernated",type=integer,JSONPath=`.status.hibernatedWorkloads`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterHibernatePolicy is the Schema for the clusterhibernate policies API.
// It is cluster-scoped and applies to workloads that carry the annotation
// greencosts.hstr.nl/clusterhibernatepolicy=<name>.
type ClusterHibernatePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterHibernatePolicySpec   `json:"spec,omitempty"`
	Status ClusterHibernatePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterHibernatePolicyList contains a list of ClusterHibernatePolicy.
type ClusterHibernatePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterHibernatePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterHibernatePolicy{}, &ClusterHibernatePolicyList{})
}
