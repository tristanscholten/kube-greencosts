package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupWithoutExporterKeepsTracingNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup() shutdown = nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
	if got := otel.GetTextMapPropagator().Fields(); len(got) == 0 {
		t.Fatal("Setup() did not install trace context propagator")
	}
}

func TestSamplerFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		sampler  string
		arg      string
		wantDrop bool
	}{
		{name: "default samples", wantDrop: false},
		{name: "always off drops", sampler: "always_off", wantDrop: true},
		{name: "traceid ratio zero drops", sampler: samplerTraceIDRatio, arg: "0", wantDrop: true},
		{name: "traceid ratio one samples", sampler: samplerTraceIDRatio, arg: "1", wantDrop: false},
		{name: "invalid ratio falls back to default", sampler: samplerTraceIDRatio, arg: "not-a-number", wantDrop: false},
		{name: "parentbased always off drops root spans", sampler: "parentbased_always_off", wantDrop: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", tt.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tt.arg)

			result := samplerFromEnv().ShouldSample(sdktrace.SamplingParameters{
				ParentContext: context.Background(),
				TraceID:       trace.TraceID{1},
				Name:          "test-span",
			})
			gotDrop := result.Decision == sdktrace.Drop
			if gotDrop != tt.wantDrop {
				t.Fatalf("ShouldSample() drop = %v, want %v", gotDrop, tt.wantDrop)
			}
		})
	}
}

func TestParseRatio(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  float64
		want float64
	}{
		{name: "empty uses default", in: "", def: 0.25, want: 0.25},
		{name: "zero accepted", in: "0", def: 0.25, want: 0},
		{name: "one accepted", in: "1", def: 0.25, want: 1},
		{name: "fraction accepted", in: "0.5", def: 0.25, want: 0.5},
		{name: "negative rejected", in: "-0.1", def: 0.25, want: 0.25},
		{name: "above one rejected", in: "1.1", def: 0.25, want: 0.25},
		{name: "malformed rejected", in: "nope", def: 0.25, want: 0.25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRatio(tt.in, tt.def); got != tt.want {
				t.Fatalf("parseRatio(%q, %v) = %v, want %v", tt.in, tt.def, got, tt.want)
			}
		})
	}
}
