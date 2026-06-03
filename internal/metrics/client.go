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

// Package metrics provides a unified interface for querying CPU usage (via the
// Kubernetes metrics-server) and network/ingress traffic (via Prometheus).
package metrics

import (
	"context"
	"fmt"
	"time"

	prometheusapi "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Client is the interface controllers use to query live runtime metrics.
type Client interface {
	// QueryNamespaceCPU returns the total CPU usage across all pods in the
	// given namespace, as reported by the metrics-server.
	QueryNamespaceCPU(ctx context.Context, namespace string) (resource.Quantity, error)

	// QueryNamespaceNetwork returns the average inbound+outbound network
	// throughput for pods in the namespace over the given look-back window.
	// The value is expressed in bytes per second.
	QueryNamespaceNetwork(ctx context.Context, namespace string, window time.Duration) (resource.Quantity, error)

	// QueryNamespaceIngressRPS returns the average HTTP requests-per-second
	// observed at the ingress layer for the namespace over the look-back window.
	QueryNamespaceIngressRPS(ctx context.Context, namespace string, window time.Duration) (float64, error)
}

// compositeClient satisfies Client using the metrics-server for CPU and
// Prometheus for network/ingress data.
type compositeClient struct {
	metricsClient metricsclientset.Interface
	promAPI       prometheusv1.API
}

// NewClient constructs a Client. Pass a nil prometheusClient to disable
// Prometheus-backed queries (network/ingress checks will return zero).
func NewClient(metricsClient metricsclientset.Interface, prometheusURL string) (Client, error) {
	c := &compositeClient{metricsClient: metricsClient}

	if prometheusURL != "" {
		promClient, err := prometheusapi.NewClient(prometheusapi.Config{Address: prometheusURL})
		if err != nil {
			return nil, fmt.Errorf("creating prometheus client for %q: %w", prometheusURL, err)
		}
		c.promAPI = prometheusv1.NewAPI(promClient)
	}

	return c, nil
}

// QueryNamespaceCPU sums CPU usage across all pods in the namespace.
func (c *compositeClient) QueryNamespaceCPU(ctx context.Context, namespace string) (qty resource.Quantity, retErr error) {
	_, span := otel.Tracer("greencosts.hstr.nl/metrics").Start(ctx, "metrics.QueryNamespaceCPU",
		trace.WithAttributes(attribute.String("k8s.namespace.name", namespace)))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	podMetricsList, err := c.metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("listing pod metrics for namespace %q: %w", namespace, err)
	}

	total := resource.NewMilliQuantity(0, resource.DecimalSI)
	for i := range podMetricsList.Items {
		addPodCPU(total, &podMetricsList.Items[i])
	}

	return *total, nil
}

func addPodCPU(total *resource.Quantity, pm *metricsv1beta1.PodMetrics) {
	for _, container := range pm.Containers {
		cpu := container.Usage.Cpu()
		if cpu != nil {
			total.Add(*cpu)
		}
	}
}

// QueryNamespaceNetwork queries Prometheus for the average bytes/s for the
// namespace over the given window.
//
// PromQL used:
//
//	sum(rate(container_network_transmit_bytes_total{namespace="<ns>"}[<window>])
//	  + rate(container_network_receive_bytes_total{namespace="<ns>"}[<window>]))
func (c *compositeClient) QueryNamespaceNetwork(ctx context.Context, namespace string, window time.Duration) (qty resource.Quantity, retErr error) {
	_, span := otel.Tracer("greencosts.hstr.nl/metrics").Start(ctx, "metrics.QueryNamespaceNetwork",
		trace.WithAttributes(
			attribute.String("k8s.namespace.name", namespace),
			attribute.String("window", window.String()),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	if c.promAPI == nil {
		return resource.Quantity{}, nil
	}

	promQL := fmt.Sprintf(
		`sum(rate(container_network_transmit_bytes_total{namespace=%q}[%s]) + rate(container_network_receive_bytes_total{namespace=%q}[%s]))`,
		namespace, promDuration(window),
		namespace, promDuration(window),
	)

	val, _, err := c.promAPI.Query(ctx, promQL, time.Now())
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("querying network bytes for namespace %q: %w", namespace, err)
	}

	bytesPerSec := scalarValue(val)
	q := resource.NewMilliQuantity(int64(bytesPerSec*1000), resource.BinarySI)

	return *q, nil
}

// QueryNamespaceIngressRPS queries Prometheus for the average HTTP
// requests-per-second observed at the ingress layer for the namespace over
// the given window.
//
// PromQL used (nginx-ingress compatible):
//
//	sum(rate(nginx_ingress_controller_requests{namespace="<ns>"}[<window>]))
func (c *compositeClient) QueryNamespaceIngressRPS(ctx context.Context, namespace string, window time.Duration) (rps float64, retErr error) {
	_, span := otel.Tracer("greencosts.hstr.nl/metrics").Start(ctx, "metrics.QueryNamespaceIngressRPS",
		trace.WithAttributes(
			attribute.String("k8s.namespace.name", namespace),
			attribute.String("window", window.String()),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	if c.promAPI == nil {
		return 0, nil
	}

	promQL := fmt.Sprintf(
		`sum(rate(nginx_ingress_controller_requests{namespace=%q}[%s]))`,
		namespace, promDuration(window),
	)

	val, _, err := c.promAPI.Query(ctx, promQL, time.Now())
	if err != nil {
		return 0, fmt.Errorf("querying ingress RPS for namespace %q: %w", namespace, err)
	}

	return scalarValue(val), nil
}

// promDuration formats a time.Duration as a Prometheus duration string (e.g. "30m0s").
func promDuration(d time.Duration) string {
	return d.String()
}

// scalarValue extracts the numeric value from a Prometheus scalar or
// single-element vector result. Returns 0 for empty or unexpected types.
func scalarValue(val model.Value) float64 {
	switch v := val.(type) {
	case *model.Scalar:
		return float64(v.Value)
	case model.Vector:
		if len(v) == 1 {
			return float64(v[0].Value)
		}
	}
	return 0
}
