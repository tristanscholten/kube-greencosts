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

// Package telemetry initialises the global OpenTelemetry TracerProvider.
//
// Tracing is always enabled. The OTLP gRPC exporter targets
// OTEL_EXPORTER_OTLP_ENDPOINT (default: localhost:4317 per the OTel spec).
// If no collector is reachable the exporter retries silently in the background
// — the operator continues running normally and drops spans.
//
// Usage in main():
//
//	shutdown, err := telemetry.Setup(ctx)
//	if err != nil { ... }
//	defer func() {
//	    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	    defer cancel()
//	    _ = shutdown(shutdownCtx)
//	}()
//
// All standard OTEL_* env vars (endpoint, headers, TLS, sampler, resource
// attributes) are respected automatically. See the README Observability section
// for the full reference.
//
// To disable tracing entirely:
//
//	OTEL_TRACES_SAMPLER=always_off
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup initialises the global OpenTelemetry TracerProvider and returns a
// shutdown function that flushes and closes the exporter.
//
// The OTLP gRPC exporter is always started. When no collector is reachable
// it retries in the background and drops spans without affecting the operator.
// Set OTEL_TRACES_SAMPLER=always_off to disable span recording entirely.
func Setup(ctx context.Context) (shutdown func(context.Context) error, err error) {
	// Create OTLP gRPC exporter. All connection options (endpoint, headers,
	// TLS, timeout) are read automatically from OTEL_EXPORTER_OTLP_* env vars.
	// Default endpoint: localhost:4317.
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP gRPC trace exporter: %w", err)
	}

	res, err := sdkresource.New(ctx,
		sdkresource.WithTelemetrySDK(),
		sdkresource.WithProcess(),
		sdkresource.WithAttributes(attribute.String("service.name", "kube-greencosts")),
	)
	if err != nil {
		return nil, fmt.Errorf("building OTel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFromEnv()),
	)

	otel.SetTracerProvider(tp)

	// Use the W3C Trace Context and Baggage propagators so that trace context
	// is correctly propagated to/from HTTP headers when using otelhttp.
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return tp.Shutdown, nil
}

// samplerFromEnv reads OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG and
// returns the corresponding sdktrace.Sampler. The Go OTel SDK does not parse
// these env vars automatically, so we do it here.
//
// Supported values (matching the OTel spec):
//   - always_on (default)
//   - always_off
//   - traceidratio           — OTEL_TRACES_SAMPLER_ARG sets ratio (0.0–1.0)
//   - parentbased_always_on
//   - parentbased_always_off
//   - parentbased_traceidratio — OTEL_TRACES_SAMPLER_ARG sets ratio (0.0–1.0)
func samplerFromEnv() sdktrace.Sampler {
	name := os.Getenv("OTEL_TRACES_SAMPLER")
	arg := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	switch name {
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(parseRatio(arg, 1.0))
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(parseRatio(arg, 1.0)))
	default: // "always_on" or unset
		return sdktrace.AlwaysSample()
	}
}

func parseRatio(s string, def float64) float64 {
	if s == "" {
		return def
	}
	r, err := strconv.ParseFloat(s, 64)
	if err != nil || r < 0 || r > 1 {
		return def
	}
	return r
}
