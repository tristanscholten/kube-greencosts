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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CustomProviderConfig holds the connection details for a custom HTTP energy price API.
type CustomProviderConfig struct {
	// URL is the full HTTP(S) endpoint that returns price data.
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`

	// AuthSecretRef optionally points to a Secret key whose value is sent as
	// "Authorization: Bearer <token>" on every request.
	// +optional
	AuthSecretRef *corev1.SecretKeySelector `json:"authSecretRef,omitempty"`
}

// EnergyPriceSourceSpec defines the desired state of EnergyPriceSource.
type EnergyPriceSourceSpec struct {
	// Provider is the name of the energy data provider plugin (e.g. "customProvider").
	// +kubebuilder:validation:MinLength=1
	Provider string `json:"provider"`

	// BiddingZone is the market bidding zone (e.g. "NL", "DE-LU").
	// +kubebuilder:validation:MinLength=1
	BiddingZone string `json:"biddingZone"`

	// Currency is the ISO 4217 currency code for prices (e.g. "EUR").
	// +kubebuilder:validation:Pattern=`^[A-Z]{3}$`
	Currency string `json:"currency"`

	// RefreshSchedule is a standard five-field cron expression that controls
	// when the controller fetches fresh price data.
	// +kubebuilder:validation:MinLength=1
	RefreshSchedule string `json:"refreshSchedule"`

	// CacheTTL is how long fetched prices remain valid before a forced refresh.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?([0-9]+s)?$`
	CacheTTL metav1.Duration `json:"cacheTTL"`

	// CustomProviderConfig holds the endpoint and auth configuration used when
	// Provider is "customProvider".
	// +optional
	CustomProviderConfig *CustomProviderConfig `json:"customProviderConfig,omitempty"`
}

// PriceInterval represents the energy price for a single 30-minute slot.
type PriceInterval struct {
	// Start is the inclusive beginning of the price interval.
	Start metav1.Time `json:"start"`

	// End is the exclusive end of the price interval.
	End metav1.Time `json:"end"`

	// EurPerMWh is the price in EUR per megawatt-hour. Negative values indicate
	// surplus generation (market pays consumers to consume).
	EurPerMWh float64 `json:"eurPerMWh"`
}

// EnergyPriceSourceStatus defines the observed state of EnergyPriceSource.
type EnergyPriceSourceStatus struct {
	// LastUpdated is the timestamp of the most recent successful price fetch.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// Prices holds the fetched 30-minute price intervals, ordered chronologically.
	// +optional
	Prices []PriceInterval `json:"prices,omitempty"`

	// Conditions reflect the current state of the EnergyPriceSource reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=eps
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.biddingZone`
// +kubebuilder:printcolumn:name="LastUpdated",type=date,JSONPath=`.status.lastUpdated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// EnergyPriceSource is the Schema for the energypricesources API.
type EnergyPriceSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnergyPriceSourceSpec   `json:"spec,omitempty"`
	Status EnergyPriceSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EnergyPriceSourceList contains a list of EnergyPriceSource.
type EnergyPriceSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EnergyPriceSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EnergyPriceSource{}, &EnergyPriceSourceList{})
}
