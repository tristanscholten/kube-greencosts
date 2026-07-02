package providers

import (
	"context"
	"errors"
	"strings"
	"testing"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

type stubProvider struct{}

func (stubProvider) FetchPrices(context.Context, FetchPricesRequest) ([]greencostsv1alpha1.PricePoint, error) {
	return nil, nil
}

func TestRegistryGet(t *testing.T) {
	factoryErr := errors.New("bad provider config")
	tests := []struct {
		name      string
		register  func(*Registry)
		wantErr   string
		wantFound bool
	}{
		{
			name: "registered provider",
			register: func(r *Registry) {
				r.Register("stub", func(greencostsv1alpha1.EnergyPriceSourceSpec, string) (EnergyProvider, error) {
					return stubProvider{}, nil
				})
			},
			wantFound: true,
		},
		{
			name:    "unknown provider",
			wantErr: `unknown energy provider "stub"`,
		},
		{
			name: "factory error is wrapped with provider name",
			register: func(r *Registry) {
				r.Register("stub", func(greencostsv1alpha1.EnergyPriceSourceSpec, string) (EnergyProvider, error) {
					return nil, factoryErr
				})
			},
			wantErr: `initialising provider "stub": bad provider config`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry()
			if tt.register != nil {
				tt.register(registry)
			}

			provider, err := registry.Get("stub", greencostsv1alpha1.EnergyPriceSourceSpec{}, "token")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Get() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if (provider != nil) != tt.wantFound {
				t.Fatalf("Get() provider nil = %v, want found %v", provider == nil, tt.wantFound)
			}
		})
	}
}
