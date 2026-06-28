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
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	metricsLabelNamespace   = "namespace"
	metricsLabelName        = "name"
	metricsLabelProvider    = "provider"
	metricsLabelBiddingZone = "bidding_zone"
)

var energyPriceSourceMetricLabels = []string{
	metricsLabelNamespace,
	metricsLabelName,
	metricsLabelProvider,
	metricsLabelBiddingZone,
}

// EnergyPriceSourceMetricsCollector exports configured EnergyPriceSource objects
// and their current status values with low-cardinality source identity labels.
type EnergyPriceSourceMetricsCollector struct {
	Reader client.Reader
	Now    func() time.Time

	infoDesc         *prometheus.Desc
	pricePointsDesc  *prometheus.Desc
	lastUpdatedDesc  *prometheus.Desc
	currentPriceDesc *prometheus.Desc
}

// NewEnergyPriceSourceMetricsCollector returns a collector backed by Kubernetes reads.
func NewEnergyPriceSourceMetricsCollector(reader client.Reader) *EnergyPriceSourceMetricsCollector {
	return &EnergyPriceSourceMetricsCollector{
		Reader: reader,
		Now:    time.Now,
		infoDesc: prometheus.NewDesc(
			"kube_greencosts_energy_price_source_info",
			"Configured EnergyPriceSource objects labeled by stable source identity.",
			energyPriceSourceMetricLabels,
			nil,
		),
		pricePointsDesc: prometheus.NewDesc(
			"kube_greencosts_energy_price_source_price_points",
			"Number of price points currently stored on the EnergyPriceSource status.",
			energyPriceSourceMetricLabels,
			nil,
		),
		lastUpdatedDesc: prometheus.NewDesc(
			"kube_greencosts_energy_price_source_last_updated_timestamp_seconds",
			"Unix timestamp of the most recent successful EnergyPriceSource refresh.",
			energyPriceSourceMetricLabels,
			nil,
		),
		currentPriceDesc: prometheus.NewDesc(
			"kube_greencosts_energy_price_source_current_price_eur_per_mwh",
			"Current EnergyPriceSource price in EUR per MWh for the active price interval.",
			energyPriceSourceMetricLabels,
			nil,
		),
	}
}

// RegisterEnergyPriceSourceMetrics registers EnergyPriceSource metrics with registry.
func RegisterEnergyPriceSourceMetrics(registry prometheus.Registerer, reader client.Reader) error {
	if err := registry.Register(NewEnergyPriceSourceMetricsCollector(reader)); err != nil {
		return fmt.Errorf("registering EnergyPriceSource metrics: %w", err)
	}
	return nil
}

// Describe sends metric descriptors to Prometheus.
func (c *EnergyPriceSourceMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.infoDesc
	ch <- c.pricePointsDesc
	ch <- c.lastUpdatedDesc
	ch <- c.currentPriceDesc
}

// Collect lists EnergyPriceSource objects and emits one series per source.
func (c *EnergyPriceSourceMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	if c.Reader == nil {
		ch <- prometheus.NewInvalidMetric(c.infoDesc, fmt.Errorf("reader is nil"))
		return
	}

	var list greencostsv1alpha1.EnergyPriceSourceList
	if err := c.Reader.List(context.Background(), &list); err != nil {
		ch <- prometheus.NewInvalidMetric(c.infoDesc, fmt.Errorf("listing EnergyPriceSources: %w", err))
		return
	}

	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	for i := range list.Items {
		eps := &list.Items[i]
		labels := []string{eps.Namespace, eps.Name, eps.Spec.Provider, eps.Spec.BiddingZone}

		ch <- prometheus.MustNewConstMetric(c.infoDesc, prometheus.GaugeValue, 1, labels...)
		ch <- prometheus.MustNewConstMetric(c.pricePointsDesc, prometheus.GaugeValue, float64(len(eps.Status.Prices)), labels...)
		if eps.Status.LastUpdated != nil {
			ch <- prometheus.MustNewConstMetric(
				c.lastUpdatedDesc,
				prometheus.GaugeValue,
				float64(eps.Status.LastUpdated.Unix()),
				labels...,
			)
		}
		if price, ok := currentPricePoint(eps.Status.Prices, now); ok {
			ch <- prometheus.MustNewConstMetric(c.currentPriceDesc, prometheus.GaugeValue, price.EurPerMWh, labels...)
		}
	}
}

func currentPricePoint(prices []greencostsv1alpha1.PricePoint, now time.Time) (greencostsv1alpha1.PricePoint, bool) {
	var current greencostsv1alpha1.PricePoint
	found := false
	for _, price := range prices {
		if price.At.After(now) {
			break
		}
		current = price
		found = true
	}
	if !found {
		return greencostsv1alpha1.PricePoint{}, false
	}

	return current, true
}
