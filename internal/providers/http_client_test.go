package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRedactedHTTPClientKeepsSecretsOutOfSpanAttributes(t *testing.T) {
	const token = "super-secret-token"

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		otel.SetTracerProvider(previous)
		_ = tp.Shutdown(context.Background())
	}()

	var receivedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewRedactedHTTPClient(time.Second, "token")

	resp, err := client.Get(server.URL + "/prices?token=" + token + "&resolution=15")
	if err != nil {
		t.Fatalf("GET with redacted client: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing response body: %v", err)
	}
	if receivedToken != token {
		t.Fatalf("server received token %q, want original token", receivedToken)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one exported client span")
	}
	for _, span := range spans {
		for _, attr := range span.Attributes {
			value := attr.Value.AsString()
			if strings.Contains(value, token) {
				t.Fatalf("span attribute %s leaked token in %q", attr.Key, value)
			}
		}
	}
}

func TestRedactedURLRedactsOnlyConfiguredQueryKeys(t *testing.T) {
	got := redactedURL(
		mustParseURL(t, "https://example.test/path?securityToken=secret&area=NL"),
		[]string{"securityToken"},
	)
	if strings.Contains(got, "secret") {
		t.Fatalf("redacted URL leaked secret: %q", got)
	}
	if !strings.Contains(got, "area=NL") {
		t.Fatalf("redacted URL removed non-secret query param: %q", got)
	}
}

func TestRedactedURLHandlesEmptyInputs(t *testing.T) {
	if got := redactedURL(nil, []string{"token"}); got != "" {
		t.Fatalf("redactedURL(nil) = %q, want empty string", got)
	}

	const raw = "https://example.test/path"
	if got := redactedURL(mustParseURL(t, raw), []string{"token"}); got != raw {
		t.Fatalf("redactedURL() = %q, want %q", got, raw)
	}
}

func TestReadLimitedBody(t *testing.T) {
	body, err := ReadLimitedBody(strings.NewReader("1234"), 4)
	if err != nil {
		t.Fatalf("ReadLimitedBody() exact limit error = %v", err)
	}
	if string(body) != "1234" {
		t.Fatalf("ReadLimitedBody() exact limit body = %q, want 1234", body)
	}

	_, err = ReadLimitedBody(strings.NewReader("12345"), 4)
	if err == nil {
		t.Fatal("ReadLimitedBody() accepted oversized body")
	}
	if !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("ReadLimitedBody() error = %q, want size limit", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing URL %q: %v", raw, err)
	}
	return u
}
