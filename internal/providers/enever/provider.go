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

// Package enever implements the "enever" EnergyProvider plugin.
//
// It queries the enever.nl API for today's and tomorrow's hourly electricity
// prices. Prices are returned in EUR/kWh by the API and converted to EUR/MWh
// (× 1000) to match the PriceInterval convention used by this operator.
//
// The enever.nl API requires the token as a URL query parameter (token=...).
// This is mandated by the API design.
//
// Tomorrow's prices are typically published around 13:00 CET. When they are
// not yet available the fetch logs a warning and continues with today's data
// only — this is not treated as an error.
package enever

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ProviderName is the string used in EnergyPriceSource.spec.provider.
	ProviderName = "enever"

	baseURL        = "https://enever.nl/apiv3"
	requestTimeout = 30 * time.Second
	maxBodyBytes   = 1 << 20 // 1 MiB
)

// knownSuppliers is the set of valid supplier codes accepted by the v3 API.
var knownSuppliers = map[string]struct{}{
	"ANWB": {}, "BE": {}, "CB": {}, "ED": {}, "EE": {}, "EG": {},
	"EN": {}, "ES": {}, "EVO": {}, "EZ": {}, "FR": {}, "GSL": {},
	"HE": {}, "IN": {}, "MDE": {}, "NE": {}, "PE": {}, "QU": {},
	"SS": {}, "TI": {}, "VDB": {}, "VF": {}, "VON": {}, "WE": {}, "ZP": {},
}

// apiResponse is the top-level JSON response returned by enever.nl.
type apiResponse struct {
	Status string              `json:"status"`
	Data   []map[string]string `json:"data"`
	Code   string              `json:"code"`
}

// ── Provider ─────────────────────────────────────────────────────────────────

// Provider implements providers.EnergyProvider for the enever.nl API.
type Provider struct {
	token      string
	supplier   string // upper-cased; empty = raw spot price ("prijs")
	httpClient *http.Client
}

// New constructs a Provider with the given token and optional supplier code.
// supplier is case-insensitive; pass an empty string for raw spot prices.
func New(token, supplier string) *Provider {
	return &Provider{
		token:      token,
		supplier:   strings.ToUpper(supplier),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Factory returns a providers.ProviderFactory that builds an enever Provider
// from the EnergyPriceSourceSpec.
func Factory() providers.ProviderFactory {
	return func(spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (providers.EnergyProvider, error) {
		cfg := spec.Providers.EneverConfig
		if cfg == nil {
			return nil, fmt.Errorf("eneverConfig is required for provider %q", ProviderName)
		}

		if token == "" {
			return nil, fmt.Errorf("API token is empty; check eneverConfig.secretRef")
		}

		supplier := strings.ToUpper(cfg.Supplier)
		if supplier != "" {
			if _, ok := knownSuppliers[supplier]; !ok {
				return nil, fmt.Errorf("unknown enever supplier %q", cfg.Supplier)
			}
		}

		return New(token, supplier), nil
	}
}

// FetchPrices fetches today's and tomorrow's hourly prices from enever.nl.
// Tomorrow's data may not be available yet (published ~14:00 CET); in that
// case a warning is logged and only today's data is returned.
func (p *Provider) FetchPrices(ctx context.Context, _ providers.FetchPricesRequest) ([]greencostsv1alpha1.PriceInterval, error) {
	today, err := p.fetchDay(ctx, "vandaag")
	if err != nil {
		return nil, fmt.Errorf("fetching today's enever prices: %w", err)
	}

	tomorrow, err := p.fetchDay(ctx, "morgen")
	if err != nil {
		// Tomorrow's prices are not yet available — log and continue.
		slog.Warn("enever: tomorrow's prices not yet available", "error", err)
	} else if len(tomorrow) == 0 {
		// API returned success but empty data — prices not published yet.
		slog.Info("enever: tomorrow's prices not yet published (empty response). This is expected if you fetch before ~14:00 CET. Check the enever.nl API directly to confirm.")
	}

	all := append(today, tomorrow...)
	slog.Info("enever: fetched price intervals", "today", len(today), "tomorrow", len(tomorrow), "total", len(all))
	return all, nil
}

// fetchDay fetches prices for a single day. day must be "vandaag" or "morgen".
func (p *Provider) fetchDay(ctx context.Context, day string) ([]greencostsv1alpha1.PriceInterval, error) {
	// The enever.nl API requires the token in the URL query string.
	url := fmt.Sprintf("%s/stroomprijs_%s.php?token=%s&resolution=15", baseURL, day, p.token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", day, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s prices: %w", day, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("closing enever response body", "day", day, "error", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enever API returned HTTP %d for %s", resp.StatusCode, day)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading enever response for %s: %w", day, err)
	}

	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("parsing enever JSON for %s: %w", day, err)
	}

	if ar.Status != "true" {
		return nil, fmt.Errorf("enever API returned status %q for %s (code %s)", ar.Status, day, ar.Code)
	}

	return p.convertData(ar.Data, day)
}

// convertData converts the raw data slice to PriceIntervals.
func (p *Provider) convertData(data []map[string]string, day string) ([]greencostsv1alpha1.PriceInterval, error) {
	priceKey := "prijs"
	if p.supplier != "" {
		priceKey = "prijs" + p.supplier
	}

	intervals := make([]greencostsv1alpha1.PriceInterval, 0, len(data))

	for i, item := range data {
		datumStr, ok := item["datum"]
		if !ok {
			return nil, fmt.Errorf("%s item %d: missing datum field", day, i)
		}

		// v3 API returns times as RFC3339 with Dutch local offset, e.g.
		// "2025-10-21T16:00:00+02:00". Parse the offset as-is; it already
		// represents the correct clock time.
		start, err := time.Parse(time.RFC3339, datumStr)
		if err != nil {
			return nil, fmt.Errorf("%s item %d: parsing datum %q: %w", day, i, datumStr, err)
		}

		priceStr, ok := item[priceKey]
		if !ok {
			return nil, fmt.Errorf("%s item %d: price key %q not found in response", day, i, priceKey)
		}

		eurPerKWh, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			return nil, fmt.Errorf("%s item %d: parsing price %q: %w", day, i, priceStr, err)
		}

		intervals = append(intervals, greencostsv1alpha1.PriceInterval{
			Start:     metav1.NewTime(start),
			End:       metav1.NewTime(start.Add(time.Hour)),
			EurPerMWh: eurPerKWh * 1000, // convert EUR/kWh → EUR/MWh
		})
	}

	return intervals, nil
}
