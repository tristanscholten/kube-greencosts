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

// Package energyzero implements the public "energyzero" EnergyProvider plugin.
//
// It queries EnergyZero's no-token public prices endpoint for quarter-hourly
// electricity spot prices. EnergyZero returns EUR/kWh decimal strings; this
// provider converts them to EUR/MWh to match the PricePoint convention.
package energyzero

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
)

const (
	// ProviderName is the string used in EnergyPriceSource.spec.provider.
	ProviderName = "energyzero"

	defaultBaseURL = "https://public.api.energyzero.nl/public/v1/prices"
	dateLayout     = "02-01-2006"
	interval       = "INTERVAL_QUARTER"
	energyType     = "ENERGY_TYPE_ELECTRICITY"
	requestTimeout = 30 * time.Second
	maxBodyBytes   = 4 << 20 // 4 MiB — three days of quarter-hour prices plus taxes
)

type apiResponse struct {
	Interval string      `json:"interval"`
	Base     []priceItem `json:"base"`
}

type priceItem struct {
	Start string `json:"start"`
	Price struct {
		Value string `json:"value"`
	} `json:"price"`
}

// Provider implements providers.EnergyProvider for EnergyZero's public API.
type Provider struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs an EnergyZero provider. The API is public and needs no token.
func New() *Provider {
	return &Provider{
		baseURL:    defaultBaseURL,
		httpClient: providers.NewRedactedHTTPClient(requestTimeout),
	}
}

// Factory returns a providers.ProviderFactory that builds an EnergyZero Provider.
func Factory() providers.ProviderFactory {
	return func(spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (providers.EnergyProvider, error) {
		if spec.BiddingZone != "NL" {
			return nil, fmt.Errorf("EnergyZero only supports biddingZone \"NL\", got %q", spec.BiddingZone)
		}
		if token != "" {
			return nil, fmt.Errorf("EnergyZero is a public provider and must not be configured with a token")
		}
		return New(), nil
	}
}

// FetchPrices fetches quarter-hour electricity prices around req.Date.
func (p *Provider) FetchPrices(ctx context.Context, req providers.FetchPricesRequest) (pts []greencostsv1alpha1.PricePoint, retErr error) {
	ctx, span := otel.Tracer("greencosts.hstr.nl/providers").Start(ctx, "energyzero.FetchPrices",
		trace.WithAttributes(attribute.String("provider", ProviderName)))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	httpReq, err := p.newRequest(ctx, req.Date)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling EnergyZero API: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("closing EnergyZero response body", "error", cerr)
		}
	}()

	body, err := providers.ReadLimitedBody(resp.Body, maxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("reading EnergyZero response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EnergyZero API returned HTTP %d", resp.StatusCode)
	}

	return parseResponse(body)
}

func (p *Provider) newRequest(ctx context.Context, date time.Time) (*http.Request, error) {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing EnergyZero base URL: %w", err)
	}

	q := u.Query()
	q.Set("date", date.Format(dateLayout))
	q.Set("interval", interval)
	q.Set("energyType", energyType)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building EnergyZero request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func parseResponse(body []byte) ([]greencostsv1alpha1.PricePoint, error) {
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("parsing EnergyZero JSON: %w", err)
	}
	if ar.Interval != "RESPONSE_INTERVAL_QUARTER" {
		return nil, fmt.Errorf("EnergyZero returned interval %q, want RESPONSE_INTERVAL_QUARTER", ar.Interval)
	}
	if len(ar.Base) == 0 {
		return nil, fmt.Errorf("EnergyZero response contains no base prices")
	}

	points := make([]greencostsv1alpha1.PricePoint, 0, len(ar.Base))
	for i, item := range ar.Base {
		start, err := time.Parse(time.RFC3339, item.Start)
		if err != nil {
			return nil, fmt.Errorf("base item %d: parsing start %q: %w", i, item.Start, err)
		}
		eurPerKWh, err := strconv.ParseFloat(item.Price.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("base item %d: parsing price %q: %w", i, item.Price.Value, err)
		}
		if math.IsNaN(eurPerKWh) || math.IsInf(eurPerKWh, 0) {
			return nil, fmt.Errorf("base item %d: parsing price %q: not a finite decimal", i, item.Price.Value)
		}
		points = append(points, greencostsv1alpha1.PricePoint{
			At:        metav1.NewTime(start),
			EurPerMWh: eurPerKWh * 1000,
		})
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].At.Time.Before(points[j].At.Time)
	})
	return points, nil
}
