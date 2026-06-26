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

// Package custom implements the "customProvider" EnergyProvider plugin.
//
// The provider makes a GET request to the URL configured in
// EnergyPriceSource.spec.customProviderConfig. The endpoint must return a
// JSON array of price point objects with the following shape:
//
//	[
//	  {"start":"2026-05-16T00:00:00+02:00","eurPerMWh":42.10},
//	  ...
//	]
//
// Each object's "start" is the timestamp at which that price takes effect.
// The price is valid until the start of the next object in the array.
//
// If authSecretRef is set, the Bearer token read from the referenced Secret is
// sent as the Authorization header.
package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ProviderName is the string used in EnergyPriceSource.spec.provider.
	ProviderName = "customProvider"

	requestTimeout = 30 * time.Second
	maxBodyBytes   = 1 << 20 // 1 MiB
)

// apiPriceInterval is the wire representation returned by the external API.
type apiPriceInterval struct {
	Start     string  `json:"start"`
	EurPerMWh float64 `json:"eurPerMWh"`
}

// Provider implements providers.EnergyProvider by calling a configurable
// HTTP endpoint.
type Provider struct {
	url         string
	bearerToken string
	httpClient  *http.Client
}

// New constructs a Provider from the given CustomProviderConfig.
// bearerToken must already be resolved from the referenced Secret before
// calling New (the controller is responsible for the Secret lookup).
func New(url, bearerToken string) *Provider {
	return &Provider{
		url:         url,
		bearerToken: bearerToken,
		// otelhttp.NewTransport wraps the default transport so every HTTP call
		// produces a child span and propagates W3C trace context headers.
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			Timeout:   requestTimeout,
		},
	}
}

// Factory returns a providers.ProviderFactory suitable for use with the
// Registry. The token is passed at call time by the controller (already
// resolved from the referenced Secret).
func Factory() providers.ProviderFactory {
	return func(spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (providers.EnergyProvider, error) {
		cfg := spec.Providers.CustomProviderConfig
		if cfg == nil {
			return nil, fmt.Errorf("customProviderConfig is required for provider %q", ProviderName)
		}
		if cfg.URL == "" {
			return nil, fmt.Errorf("customProviderConfig.url must not be empty")
		}
		return New(cfg.URL, token), nil
	}
}

// FetchPrices calls the remote API and returns the price points.
func (p *Provider) FetchPrices(ctx context.Context, req providers.FetchPricesRequest) (pts []greencostsv1alpha1.PricePoint, retErr error) {
	ctx, span := otel.Tracer("greencosts.hstr.nl/providers").Start(ctx, "custom.FetchPrices",
		trace.WithAttributes(
			attribute.String("provider", ProviderName),
			attribute.String("url", p.url),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %q: %w", p.url, err)
	}

	q := httpReq.URL.Query()
	q.Set("biddingZone", req.BiddingZone)
	q.Set("date", req.Date.Format(time.DateOnly))
	httpReq.URL.RawQuery = q.Encode()

	if p.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.bearerToken)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetching prices from %q: %w", p.url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("closing response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned HTTP %d for %q", resp.StatusCode, p.url)
	}

	body, err := providers.ReadLimitedBody(resp.Body, maxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("reading response body from %q: %w", p.url, err)
	}

	var raw []apiPriceInterval
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing price response from %q: %w", p.url, err)
	}

	intervals, err := convertIntervals(raw)
	if err != nil {
		return nil, fmt.Errorf("converting price intervals from %q: %w", p.url, err)
	}

	return intervals, nil
}

func convertIntervals(raw []apiPriceInterval) ([]greencostsv1alpha1.PricePoint, error) {
	intervals := make([]greencostsv1alpha1.PricePoint, 0, len(raw))

	for i, r := range raw {
		start, err := time.Parse(time.RFC3339, r.Start)
		if err != nil {
			return nil, fmt.Errorf("interval %d: parsing start %q: %w", i, r.Start, err)
		}

		intervals = append(intervals, greencostsv1alpha1.PricePoint{
			At:        metav1.NewTime(start),
			EurPerMWh: r.EurPerMWh,
		})
	}

	sortPricePoints(intervals)
	return intervals, nil
}

func sortPricePoints(intervals []greencostsv1alpha1.PricePoint) {
	slices.SortFunc(intervals, func(a, b greencostsv1alpha1.PricePoint) int {
		if a.At.Before(&b.At) {
			return -1
		}
		if b.At.Before(&a.At) {
			return 1
		}
		return 0
	})
}
