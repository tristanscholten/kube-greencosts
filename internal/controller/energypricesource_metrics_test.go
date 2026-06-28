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

package controller

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

func TestEnergyPriceSourceMetricsCollector(t *testing.T) {
	const (
		namespaceA = "team-a"
		nameA      = "prices-a"
	)

	now := time.Date(2026, 6, 28, 8, 30, 0, 0, time.UTC)
	lastUpdated := metav1.NewTime(now.Add(-time.Hour))
	scheme := runtime.NewScheme()
	if err := greencostsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			energyPriceSourceForMetrics(nameA, namespaceA, providerEntsoe, "NL", lastUpdated, []greencostsv1alpha1.PricePoint{
				{At: metav1.NewTime(now.Add(-30 * time.Minute)), EurPerMWh: 42.5},
				{At: metav1.NewTime(now.Add(30 * time.Minute)), EurPerMWh: 13},
			}),
			energyPriceSourceForMetrics("prices-b", "team-b", providerCustomProvider, "DE-LU", lastUpdated, []greencostsv1alpha1.PricePoint{
				{At: metav1.NewTime(now.Add(-time.Hour)), EurPerMWh: -5},
			}),
		).
		Build()

	collector := NewEnergyPriceSourceMetricsCollector(client)
	collector.Now = func() time.Time { return now }
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("registering collector: %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	assertMetricValue(t, families, "kube_greencosts_energy_price_source_info", 1, map[string]string{
		metricsLabelNamespace:   namespaceA,
		metricsLabelName:        nameA,
		metricsLabelProvider:    providerEntsoe,
		metricsLabelBiddingZone: "NL",
	})
	assertMetricValue(t, families, "kube_greencosts_energy_price_source_price_points", 2, map[string]string{
		metricsLabelNamespace:   namespaceA,
		metricsLabelName:        nameA,
		metricsLabelProvider:    providerEntsoe,
		metricsLabelBiddingZone: "NL",
	})
	assertMetricValue(t, families, "kube_greencosts_energy_price_source_current_price_eur_per_mwh", 42.5, map[string]string{
		metricsLabelNamespace:   namespaceA,
		metricsLabelName:        nameA,
		metricsLabelProvider:    providerEntsoe,
		metricsLabelBiddingZone: "NL",
	})
	assertMetricValue(t, families, "kube_greencosts_energy_price_source_current_price_eur_per_mwh", -5, map[string]string{
		metricsLabelNamespace:   "team-b",
		metricsLabelName:        "prices-b",
		metricsLabelProvider:    providerCustomProvider,
		metricsLabelBiddingZone: "DE-LU",
	})
	assertMetricValue(t, families, "kube_greencosts_energy_price_source_last_updated_timestamp_seconds", float64(lastUpdated.Unix()), map[string]string{
		metricsLabelNamespace:   "team-b",
		metricsLabelName:        "prices-b",
		metricsLabelProvider:    providerCustomProvider,
		metricsLabelBiddingZone: "DE-LU",
	})
}

func TestCurrentPricePoint(t *testing.T) {
	now := time.Date(2026, 6, 28, 8, 30, 0, 0, time.UTC)
	prices := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(now.Add(-time.Hour)), EurPerMWh: 10},
		{At: metav1.NewTime(now), EurPerMWh: 20},
		{At: metav1.NewTime(now.Add(time.Hour)), EurPerMWh: 30},
	}

	got, ok := currentPricePoint(prices, now)
	if !ok {
		t.Fatal("currentPricePoint() returned no price")
	}
	if got.EurPerMWh != 20 {
		t.Fatalf("current price = %v, want 20", got.EurPerMWh)
	}

	_, ok = currentPricePoint(prices, now.Add(-2*time.Hour))
	if ok {
		t.Fatal("currentPricePoint() returned a price before first interval")
	}
}

func energyPriceSourceForMetrics(
	name string,
	namespace string,
	provider string,
	biddingZone string,
	lastUpdated metav1.Time,
	prices []greencostsv1alpha1.PricePoint,
) *greencostsv1alpha1.EnergyPriceSource {
	return &greencostsv1alpha1.EnergyPriceSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: greencostsv1alpha1.EnergyPriceSourceSpec{
			Provider:    provider,
			BiddingZone: biddingZone,
			Providers: greencostsv1alpha1.ProviderConfig{
				CustomProviderConfig: &greencostsv1alpha1.CustomProviderConfig{
					URL: "https://secret-token.example.invalid/prices?api_key=do-not-label",
					SecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "provider-token"},
						Key:                  "token",
					},
				},
			},
		},
		Status: greencostsv1alpha1.EnergyPriceSourceStatus{
			LastUpdated: &lastUpdated,
			Prices:      prices,
		},
	}
}

func assertMetricValue(
	t *testing.T,
	families []*dto.MetricFamily,
	name string,
	want float64,
	labels map[string]string,
) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !metricHasLabels(metric, labels) {
				continue
			}
			if metric.GetGauge().GetValue() != want {
				t.Fatalf("%s value = %v, want %v", name, metric.GetGauge().GetValue(), want)
			}
			return
		}
	}
	t.Fatalf("metric %s with labels %#v not found", name, labels)
}

func metricHasLabels(metric *dto.Metric, want map[string]string) bool {
	got := map[string]string{}
	for _, label := range metric.GetLabel() {
		got[label.GetName()] = label.GetValue()
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return len(got) == len(want)
}
