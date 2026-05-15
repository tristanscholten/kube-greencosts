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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchedulePolicy controls how the optimal price window is selected.
type SchedulePolicy struct {
	// PriceWeight is a 0–1 multiplier applied to the price score.
	// Higher values make the controller more aggressive about picking cheap slots.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +kubebuilder:default=0.5
	PriceWeight float64 `json:"priceWeight"`

	// AvoidPeakHours adds a scoring penalty for slots between 07:00 and 22:00.
	// +optional
	AvoidPeakHours bool `json:"avoidPeakHours,omitempty"`

	// PreferNegativePrices biases selection toward slots where eurPerMWh < 0,
	// where consumers are effectively paid to consume energy.
	// +optional
	PreferNegativePrices bool `json:"preferNegativePrices,omitempty"`
}

// FallbackPolicy describes what happens when price data is unavailable.
type FallbackPolicy struct {
	// RunAt is the HH:MM time (in spec.timeZone) at which the job runs when no
	// price data is available and WhenPriceDataMissing is true.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	RunAt string `json:"runAt"`

	// WhenPriceDataMissing enables the fallback when the referenced
	// EnergyPriceSource has no price data for the target date.
	// +optional
	WhenPriceDataMissing bool `json:"whenPriceDataMissing,omitempty"`
}

// EnergyAwareCronJobSpec defines the desired state of EnergyAwareCronJob.
type EnergyAwareCronJobSpec struct {
	// EnergyPriceSource is the name of the EnergyPriceSource resource in the same
	// namespace that provides price data.
	EnergyPriceSource corev1.LocalObjectReference `json:"energyPriceSource"`

	// Deadline is the hard wall-clock time (HH:MM, spec.timeZone) by which the
	// job must have started. Used only as a last-resort guard.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	Deadline string `json:"deadline"`

	// EarliestStart is the earliest HH:MM (spec.timeZone) at which the job may start.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	EarliestStart string `json:"earliestStart"`

	// LatestStart is the latest HH:MM (spec.timeZone) at which the job may start.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	LatestStart string `json:"latestStart"`

	// TimeZone is the IANA timezone name used when parsing HH:MM fields
	// (e.g. "Europe/Amsterdam").
	// +kubebuilder:default="UTC"
	TimeZone string `json:"timeZone"`

	// SchedulePolicy configures how the cheapest energy slot is selected.
	SchedulePolicy SchedulePolicy `json:"schedulePolicy"`

	// Fallback defines behaviour when price data is unavailable.
	Fallback FallbackPolicy `json:"fallback"`

	// JobTemplate is the pod/job specification that is launched at the scheduled time.
	// This mirrors the standard Kubernetes CronJob jobTemplate field.
	JobTemplate batchv1.JobTemplateSpec `json:"jobTemplate"`
}

// EnergyAwareCronJobStatus defines the observed state of EnergyAwareCronJob.
type EnergyAwareCronJobStatus struct {
	// NextScheduledTime is the next time a Job will be created.
	// +optional
	NextScheduledTime *metav1.Time `json:"nextScheduledTime,omitempty"`

	// LastJobRef is a reference to the most recently created Job.
	// +optional
	LastJobRef *corev1.ObjectReference `json:"lastJobRef,omitempty"`

	// Conditions reflect the current state of the EnergyAwareCronJob reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=eacj
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.energyPriceSource.name`
// +kubebuilder:printcolumn:name="Window",type=string,JSONPath=`.spec.earliestStart`,priority=1
// +kubebuilder:printcolumn:name="NextRun",type=date,JSONPath=`.status.nextScheduledTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// EnergyAwareCronJob is the Schema for the energyawarecronjobs API.
type EnergyAwareCronJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnergyAwareCronJobSpec   `json:"spec,omitempty"`
	Status EnergyAwareCronJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EnergyAwareCronJobList contains a list of EnergyAwareCronJob.
type EnergyAwareCronJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EnergyAwareCronJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EnergyAwareCronJob{}, &EnergyAwareCronJobList{})
}
