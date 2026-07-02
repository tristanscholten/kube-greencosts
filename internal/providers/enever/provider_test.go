package enever

import (
	"strings"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	datumField           = "datum"
	sampleDutchTimestamp = "2026-06-26T00:00:00+02:00"
)

func TestConvertDataUsesSpotPriceAndConvertsToMWh(t *testing.T) {
	got, err := New("token", "").convertData([]map[string]string{
		{datumField: sampleDutchTimestamp, priceField: "0.12345"},
	}, "vandaag")
	if err != nil {
		t.Fatalf("convertData() error = %v", err)
	}

	assertPricePoints(t, got,
		[]time.Time{time.Date(2026, 6, 26, 0, 0, 0, 0, time.FixedZone("", 2*60*60))},
		[]float64{123.45},
	)
}

func TestConvertDataUsesSupplierPrice(t *testing.T) {
	got, err := New("token", "anwb").convertData([]map[string]string{
		{datumField: "2026-06-26T00:15:00+02:00", priceField: "0.10", "prijsANWB": "0.22"},
	}, "vandaag")
	if err != nil {
		t.Fatalf("convertData() error = %v", err)
	}
	if got[0].EurPerMWh != 220 {
		t.Fatalf("convertData() price = %v, want supplier price converted to 220", got[0].EurPerMWh)
	}
}

func TestConvertDataReportsMalformedItems(t *testing.T) {
	tests := []struct {
		name string
		data []map[string]string
		want string
	}{
		{
			name: "missing datum",
			data: []map[string]string{{priceField: "0.1"}},
			want: "missing datum field",
		},
		{
			name: "bad datum",
			data: []map[string]string{{datumField: "not-time", priceField: "0.1"}},
			want: "parsing datum",
		},
		{
			name: "missing price",
			data: []map[string]string{{datumField: sampleDutchTimestamp}},
			want: `price key "prijs" not found`,
		},
		{
			name: "bad price",
			data: []map[string]string{{datumField: sampleDutchTimestamp, priceField: "NaN nope"}},
			want: "parsing price",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New("token", "").convertData(tt.data, "vandaag")
			if err == nil {
				t.Fatal("convertData() accepted malformed item")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("convertData() error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestFactoryValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		spec    greencostsv1alpha1.EnergyPriceSourceSpec
		token   string
		wantErr string
	}{
		{
			name:    "missing enever config",
			wantErr: "eneverConfig is required",
		},
		{
			name: "empty token",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{}},
			},
			wantErr: "API token is empty",
		},
		{
			name: "unknown supplier",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{Supplier: "nope"}},
			},
			token:   "token",
			wantErr: `unknown enever supplier "nope"`,
		},
		{
			name: "valid supplier lowercased",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{Supplier: "anwb"}},
			},
			token: "token",
		},
	}

	factory := Factory()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := factory(tt.spec, tt.token)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Factory() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Factory() error = %v", err)
			}
			p, ok := provider.(*Provider)
			if !ok {
				t.Fatalf("Factory() provider = %T, want *Provider", provider)
			}
			if p.supplier != "ANWB" {
				t.Fatalf("Factory() supplier = %q, want ANWB", p.supplier)
			}
		})
	}
}

func assertPricePoints(t *testing.T, got []greencostsv1alpha1.PricePoint, wantTimes []time.Time, wantPrices []float64) {
	t.Helper()
	if len(got) != len(wantTimes) {
		t.Fatalf("got %d price points, want %d", len(got), len(wantTimes))
	}
	for i := range got {
		if !got[i].At.Time.Equal(wantTimes[i]) {
			t.Fatalf("point %d time = %s, want %s", i, got[i].At.Time, wantTimes[i])
		}
		if got[i].EurPerMWh != wantPrices[i] {
			t.Fatalf("point %d price = %v, want %v", i, got[i].EurPerMWh, wantPrices[i])
		}
	}
}
