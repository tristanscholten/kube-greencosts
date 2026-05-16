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

// Package providers defines the interface that energy price provider plugins
// must implement, and the registry used to look them up by name.
package providers

import (
	"context"
	"fmt"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

// FetchPricesRequest carries the parameters for a price fetch operation.
type FetchPricesRequest struct {
	BiddingZone string
	// Date is the calendar day for which prices are requested.
	// Providers should return all price points covering that day.
	Date time.Time
}

// EnergyProvider is the interface that all provider plugins must implement.
type EnergyProvider interface {
	// FetchPrices returns the price points for the given request.
	// The returned slice must be ordered chronologically.
	FetchPrices(ctx context.Context, req FetchPricesRequest) ([]greencostsv1alpha1.PricePoint, error)
}

// ProviderFactory is a constructor function that builds an EnergyProvider.
// It receives the full EnergyPriceSourceSpec so each provider can extract
// its own config section, and a pre-resolved token string from the controller.
type ProviderFactory func(spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (EnergyProvider, error)

// Registry maps provider names to their factory functions.
type Registry struct {
	factories map[string]ProviderFactory
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]ProviderFactory{}}
}

// Register adds a provider factory under the given name.
func (r *Registry) Register(name string, factory ProviderFactory) {
	r.factories[name] = factory
}

// Get looks up the provider factory for name and constructs the provider.
// Returns an error when the name is unknown or the factory itself fails.
func (r *Registry) Get(name string, spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (EnergyProvider, error) {
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("unknown energy provider %q: register it before use", name)
	}

	provider, err := factory(spec, token)
	if err != nil {
		return nil, fmt.Errorf("initialising provider %q: %w", name, err)
	}

	return provider, nil
}
