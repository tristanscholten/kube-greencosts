package providers

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	redactedValue = "REDACTED"
)

func NewRedactedHTTPClient(timeout time.Duration, redactQueryKeys ...string) *http.Client {
	return &http.Client{
		Transport: redactingTransport{
			base:            http.DefaultTransport,
			redactQueryKeys: redactQueryKeys,
		},
		Timeout: timeout,
	}
}

type redactingTransport struct {
	base            http.RoundTripper
	redactQueryKeys []string
}

func (t redactingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	ctx, span := otel.Tracer("greencosts.hstr.nl/providers/http").Start(
		req.Context(),
		"HTTP "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(t.requestAttributes(req)...),
	)
	defer span.End()

	out := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(out.Header))

	resp, err := base.RoundTrip(out)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}
	if resp != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
		span.SetStatus(statusCode(resp.StatusCode), "")
	}
	return resp, nil
}

func (t redactingTransport) requestAttributes(req *http.Request) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("http.request.method", req.Method),
		attribute.String("server.address", req.URL.Hostname()),
		attribute.String("url.scheme", req.URL.Scheme),
		attribute.String("url.full", redactedURL(req.URL, t.redactQueryKeys)),
	}
}

func redactedURL(u *url.URL, redactKeys []string) string {
	if u == nil {
		return ""
	}
	clone := *u
	query := clone.Query()
	for key := range query {
		if slices.Contains(redactKeys, key) {
			query.Set(key, redactedValue)
		}
	}
	clone.RawQuery = query.Encode()
	return clone.String()
}

func statusCode(code int) codes.Code {
	if code >= 400 {
		return codes.Error
	}
	return codes.Unset
}

func ReadLimitedBody(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return body, nil
}
