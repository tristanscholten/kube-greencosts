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

// Strategy defines how the optimal run time is selected within the
// scheduling window.
// +kubebuilder:validation:Enum=LowestPrice
type Strategy string

const (
	// LowestPrice selects the price point with the lowest energy cost in the window.
	LowestPrice Strategy = "LowestPrice"
	// HighestPrice selects the price point with the highest energy cost in the window.
	HighestPrice Strategy = "HighestPrice"
)

// EnergyStrategySpec configures the energy-price optimisation behaviour.
type EnergyStrategySpec struct {
	// Strategy defines how the optimal time slot is selected within the window.
	// +kubebuilder:default=LowestPrice
	Strategy Strategy `json:"strategy"`

	// EstimatedDuration is the expected run time of the job (e.g. "2h", "30m").
	// needed to find the cheapest price point within the time window.
	EstimatedDuration metav1.Duration `json:"estimatedDuration"`

	// ScheduleWindow defines how long after the cron occurrence the Job may run.
	// Example: schedule "0 0 * * *" with scheduleWindow "6h" means the controller
	// may start the Job between 00:00 and 06:00, as long as the estimated duration
	// fits inside the window.
	ScheduleWindow metav1.Duration `json:"scheduleWindow"`
}

// EnergyAwareCronJobSpec defines the desired state of EnergyAwareCronJob.
type EnergyAwareCronJobSpec struct {
	// EnergyPriceSource is the name of the EnergyPriceSource resource in the same
	// namespace that provides electricity price data.
	EnergyPriceSource corev1.LocalObjectReference `json:"energyPriceSource"`

	// EnergyStrategy configures the energy-price optimisation strategy and window.
	EnergyStrategy EnergyStrategySpec `json:"energyStrategy"`

	// CronJob is a standard Kubernetes CronJobSpec.
	// All fields — schedule, timeZone, concurrencyPolicy, suspend, jobTemplate,
	// successfulJobsHistoryLimit, failedJobsHistoryLimit, startingDeadlineSeconds —
	// are fully honoured by the controller.
	// See https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/cron-job-v1/
	// +kubebuilder:pruning:PreserveUnknownFields
	CronJob batchv1.CronJobSpec `json:"cronJob"`
}

// EnergyAwareCronJobStatus defines the observed state of EnergyAwareCronJob.
type EnergyAwareCronJobStatus struct {
	// Active holds references to the currently running Jobs.
	// +optional
	Active []corev1.ObjectReference `json:"active,omitempty"`

	// LastScheduleTime is the last time a Job was created.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// NextCronWindow is the time at which the next scheduling window opens (the
	// raw cron occurrence). This is set as soon as the controller knows the next
	// window, even before price data has been fetched.
	// +optional
	NextCronWindow *metav1.Time `json:"nextCronWindow,omitempty"`

	// NextScheduledTime is the energy-optimised time at which the next Job will fire.
	// Populated once price data has been evaluated for the current window.
	// +optional
	NextScheduledTime *metav1.Time `json:"nextScheduledTime,omitempty"`

	// LastSuccessfulTime is the last time a Job completed successfully.
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`

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
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.cronJob.schedule`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.energyPriceSource.name`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="NextWindow",type=date,JSONPath=`.status.nextCronWindow`
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
