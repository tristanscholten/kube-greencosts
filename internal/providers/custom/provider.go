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
// JSON array of 30-minute price objects with the following shape:
//
//	[
//	  {"start":"2026-05-16T00:00:00+02:00","end":"2026-05-16T00:30:00+02:00","eurPerMWh":42.10},
//	  ...
//	]
//
// If authSecretRef is set, the Bearer token read from the referenced Secret is
// sent as the Authorization header.
package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

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
	End       string  `json:"end"`
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
		httpClient:  &http.Client{Timeout: requestTimeout},
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

// FetchPrices calls the remote API and returns the 30-minute price intervals.
func (p *Provider) FetchPrices(ctx context.Context, req providers.FetchPricesRequest) ([]greencostsv1alpha1.PriceInterval, error) {
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
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

func convertIntervals(raw []apiPriceInterval) ([]greencostsv1alpha1.PriceInterval, error) {
	intervals := make([]greencostsv1alpha1.PriceInterval, 0, len(raw))

	for i, r := range raw {
		start, err := time.Parse(time.RFC3339, r.Start)
		if err != nil {
			return nil, fmt.Errorf("interval %d: parsing start %q: %w", i, r.Start, err)
		}

		end, err := time.Parse(time.RFC3339, r.End)
		if err != nil {
			return nil, fmt.Errorf("interval %d: parsing end %q: %w", i, r.End, err)
		}

		intervals = append(intervals, greencostsv1alpha1.PriceInterval{
			Start:     metav1.NewTime(start),
			End:       metav1.NewTime(end),
			EurPerMWh: r.EurPerMWh,
		})
	}

	return intervals, nil
}
