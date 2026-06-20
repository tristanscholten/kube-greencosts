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

	// SecretRef optionally points to a Secret key whose value is sent as
	// "Authorization: Bearer <token>" on every request.
	// +optional
	SecretRef *corev1.SecretKeySelector `json:"secretRef,omitempty"`
}

// EntsoeConfig holds the connection details for the ENTSO-E Transparency Platform.
type EntsoeConfig struct {
	// SecretRef points to the Secret key that holds the ENTSO-E security token.
	SecretRef corev1.SecretKeySelector `json:"secretRef"`

	// AreaCode is the ENTSO-E EIC domain code for the bidding zone
	// (e.g. "10YNL----------L" for the Netherlands).
	// When empty the controller looks up spec.biddingZone in the built-in table.
	// +optional
	AreaCode string `json:"areaCode,omitempty"`
}

// EneverConfig holds the connection details for the enever.nl price API.
type EneverConfig struct {
	// SecretRef points to the Secret key that holds the enever.nl API token.
	SecretRef corev1.SecretKeySelector `json:"secretRef"`

	// Supplier selects which supplier's all-in retail tariff to use.
	// When empty the raw EPEX spot price is used (field "prijs" in the API response).
	// +optional
	// +kubebuilder:validation:Enum=ANWB;BE;CB;ED;EE;EG;EN;ES;EVO;EZ;FR;GSL;HE;IN;MDE;NE;PE;QU;SS;TI;VDB;VF;VON;WE;ZP
	Supplier string `json:"supplier,omitempty"`
}

// ProviderConfig groups the provider-specific configurations for an EnergyPriceSource.
// Set exactly the sub-field that matches spec.provider.
type ProviderConfig struct {
	// CustomProviderConfig holds the endpoint and auth configuration used when
	// Provider is "customProvider".
	// +optional
	CustomProviderConfig *CustomProviderConfig `json:"customProviderConfig,omitempty"`

	// EntsoeConfig holds the ENTSO-E Transparency Platform configuration used
	// when Provider is "entsoe".
	// +optional
	EntsoeConfig *EntsoeConfig `json:"entsoeConfig,omitempty"`

	// EneverConfig holds the enever.nl configuration used when Provider is "enever".
	// +optional
	EneverConfig *EneverConfig `json:"eneverConfig,omitempty"`
}

// EnergyPriceSourceSpec defines the desired state of EnergyPriceSource.
// +kubebuilder:validation:XValidation:rule="self.provider == 'entsoe' ? (has(self.providers) && has(self.providers.entsoeConfig) && !has(self.providers.eneverConfig) && !has(self.providers.customProviderConfig)) : true",message="provider entsoe requires exactly providers.entsoeConfig"
// +kubebuilder:validation:XValidation:rule="self.provider == 'enever' ? (has(self.providers) && has(self.providers.eneverConfig) && !has(self.providers.entsoeConfig) && !has(self.providers.customProviderConfig)) : true",message="provider enever requires exactly providers.eneverConfig"
// +kubebuilder:validation:XValidation:rule="self.provider == 'customProvider' ? (has(self.providers) && has(self.providers.customProviderConfig) && !has(self.providers.entsoeConfig) && !has(self.providers.eneverConfig)) : true",message="provider customProvider requires exactly providers.customProviderConfig"
type EnergyPriceSourceSpec struct {
	// Provider identifies the energy data provider plugin.
	// +kubebuilder:validation:Enum=entsoe;enever;customProvider
	Provider string `json:"provider"`

	// BiddingZone is the market bidding zone (e.g. "NL", "DE-LU").
	// +kubebuilder:validation:MinLength=1
	BiddingZone string `json:"biddingZone"`

	// RefreshSchedule is a standard five-field cron expression that controls
	// when the controller fetches fresh price data.
	// Defaults to "0 0,6,12,18 * * *" (every 6 hours at 00:00, 06:00, 12:00, 18:00 UTC).
	// +optional
	// +kubebuilder:default="0 0,6,12,18 * * *"
	RefreshSchedule string `json:"refreshSchedule,omitempty"`

	// CacheTTL is how long fetched prices remain valid before a forced refresh.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?([0-9]+s)?$`
	CacheTTL metav1.Duration `json:"cacheTTL"`

	// Providers groups the provider-specific configurations.
	// Set exactly the sub-field that matches the value of Provider.
	// +optional
	Providers ProviderConfig `json:"providers,omitempty"`
}

// PricePoint represents the energy price at the start of a single time slot.
// The slot ends at the start of the next PricePoint in the slice.
type PricePoint struct {
	// At is the timestamp at which this price takes effect.
	At metav1.Time `json:"at"`

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
	Prices []PricePoint `json:"prices,omitempty"`

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
