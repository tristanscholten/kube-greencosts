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

// Package entsoe implements the "entsoe" EnergyProvider plugin.
//
// It queries the ENTSO-E Transparency Platform REST API for day-ahead prices
// (documentType A44) and returns them as hourly PriceIntervals. Each
// reconcile fetches a 48-hour window (today + tomorrow UTC) so that
// EnergyAwareCronJob always has next-day data available.
//
// The ENTSO-E API requires the security token as a URL query parameter
// (securityToken=...). This is mandated by the API design.
package entsoe

import (
	"context"
	"encoding/xml"
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
	ProviderName = "entsoe"

	baseURL        = "https://web-api.tp.entsoe.eu/api"
	documentType   = "A44"
	periodLayout   = "200601021504" // ENTSO-E period format (UTC)
	requestTimeout = 30 * time.Second
	maxBodyBytes   = 4 << 20 // 4 MiB — ENTSO-E XML responses can be large
)

// AreaCodes maps common bidding zone short names to ENTSO-E EIC domain codes.
var AreaCodes = map[string]string{
	"NL":    "10YNL----------L",
	"BE":    "10YBE----------2",
	"DE":    "10Y1001A1001A83F",
	"DE-LU": "10Y1001A1001A82H",
	"FR":    "10YFR-RTE------C",
	"DK1":   "10YDK-1--------W",
	"DK2":   "10YDK-2--------M",
	"NO1":   "10YNO-1--------2",
	"NO2":   "10YNO-2--------T",
	"NO3":   "10YNO-3--------J",
	"NO4":   "10YNO-4--------9",
	"NO5":   "10Y1001A1001A48H",
	"SE1":   "10Y1001A1001A44P",
	"SE2":   "10Y1001A1001A45N",
	"SE3":   "10Y1001A1001A46L",
	"SE4":   "10Y1001A1001A47J",
	"FI":    "10YFI-1--------U",
	"AT":    "10YAT-APG------L",
	"CH":    "10YCH-SWISSGRIDZ",
	"ES":    "10YES-REE------0",
	"PT":    "10YPT-REN------W",
	"CZ":    "10YCZ-CEPS-----N",
	"PL":    "10YPL-AREA-----S",
}

// ── XML wire types ────────────────────────────────────────────────────────────

type publicationDocument struct {
	XMLName    xml.Name     `xml:"Publication_MarketDocument"`
	TimeSeries []timeSeries `xml:"TimeSeries"`
}

type timeSeries struct {
	Periods []period `xml:"Period"`
}

type period struct {
	TimeInterval timeInterval `xml:"timeInterval"`
	Resolution   string       `xml:"resolution"`
	Points       []point      `xml:"Point"`
}

type timeInterval struct {
	Start string `xml:"start"`
	End   string `xml:"end"`
}

// point.PriceAmount uses the literal XML element name "price.amount" which
// contains a dot — Go's encoding/xml treats the full string as the element name.
type point struct {
	Position    int     `xml:"position"`
	PriceAmount float64 `xml:"price.amount"`
}

// acknowledgementDocument is returned by ENTSO-E when the request is invalid.
type acknowledgementDocument struct {
	XMLName xml.Name `xml:"Acknowledgement_MarketDocument"`
	Reason  struct {
		Code string `xml:"code"`
		Text string `xml:"text"`
	} `xml:"Reason"`
}

// ── Provider ─────────────────────────────────────────────────────────────────

// Provider implements providers.EnergyProvider for the ENTSO-E Transparency
// Platform.
type Provider struct {
	areaCode   string
	token      string
	httpClient *http.Client
}

// New constructs a Provider with the given EIC area code and security token.
func New(areaCode, token string) *Provider {
	return &Provider{
		areaCode:   areaCode,
		token:      token,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Factory returns a providers.ProviderFactory that builds an ENTSO-E Provider
// from the EnergyPriceSourceSpec.
func Factory() providers.ProviderFactory {
	return func(spec greencostsv1alpha1.EnergyPriceSourceSpec, token string) (providers.EnergyProvider, error) {
		cfg := spec.EntsoeConfig
		if cfg == nil {
			return nil, fmt.Errorf("entsoeConfig is required for provider %q", ProviderName)
		}

		areaCode := cfg.AreaCode
		if areaCode == "" {
			var ok bool
			areaCode, ok = AreaCodes[spec.BiddingZone]
			if !ok {
				return nil, fmt.Errorf("no built-in ENTSO-E area code for biddingZone %q; set entsoeConfig.areaCode explicitly", spec.BiddingZone)
			}
		}

		if token == "" {
			return nil, fmt.Errorf("security token is empty; check entsoeConfig.securityTokenRef")
		}

		return New(areaCode, token), nil
	}
}

// FetchPrices fetches day-ahead prices for a 48-hour window starting at
// midnight UTC of the day in req.Date. This ensures next-day prices are
// available when queried after ~13:00 CET.
func (p *Provider) FetchPrices(ctx context.Context, req providers.FetchPricesRequest) ([]greencostsv1alpha1.PriceInterval, error) {
	dayStart := req.Date.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(48 * time.Hour)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building ENTSO-E request: %w", err)
	}

	q := httpReq.URL.Query()
	q.Set("documentType", documentType)
	q.Set("in_Domain", p.areaCode)
	q.Set("out_Domain", p.areaCode)
	q.Set("periodStart", dayStart.Format(periodLayout))
	q.Set("periodEnd", dayEnd.Format(periodLayout))
	// The ENTSO-E API requires the security token as a query parameter.
	q.Set("securityToken", p.token)
	httpReq.URL.RawQuery = q.Encode()

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling ENTSO-E API: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("closing ENTSO-E response body", "error", cerr)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading ENTSO-E response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Attempt to extract the structured error message.
		if apiErr := parseAcknowledgement(body); apiErr != "" {
			return nil, fmt.Errorf("ENTSO-E API error (HTTP %d): %s", resp.StatusCode, apiErr)
		}
		return nil, fmt.Errorf("ENTSO-E API returned HTTP %d", resp.StatusCode)
	}

	// A 200 response can still be an Acknowledgement on some ENTSO-E endpoints.
	if apiErr := parseAcknowledgement(body); apiErr != "" {
		return nil, fmt.Errorf("ENTSO-E API error: %s", apiErr)
	}

	return parsePublication(body)
}

// parseAcknowledgement tries to unmarshal body as an Acknowledgement_MarketDocument
// and returns a human-readable error string when successful (empty string = not an error doc).
func parseAcknowledgement(body []byte) string {
	var ack acknowledgementDocument
	if err := xml.Unmarshal(body, &ack); err != nil {
		return ""
	}
	if ack.Reason.Code == "" {
		return ""
	}
	return fmt.Sprintf("code %s: %s", ack.Reason.Code, ack.Reason.Text)
}

// parsePublication parses a Publication_MarketDocument XML body and converts
// all Points to PriceIntervals.
func parsePublication(body []byte) ([]greencostsv1alpha1.PriceInterval, error) {
	var doc publicationDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parsing ENTSO-E XML: %w", err)
	}

	var intervals []greencostsv1alpha1.PriceInterval

	for _, ts := range doc.TimeSeries {
		for _, per := range ts.Periods {
			resolution, err := parseResolution(per.Resolution)
			if err != nil {
				return nil, fmt.Errorf("unsupported resolution %q: %w", per.Resolution, err)
			}

			periodStart, err := parseTime(per.TimeInterval.Start)
			if err != nil {
				return nil, fmt.Errorf("parsing period start %q: %w", per.TimeInterval.Start, err)
			}

			for _, pt := range per.Points {
				intervalStart := periodStart.Add(time.Duration(pt.Position-1) * resolution)
				intervalEnd := intervalStart.Add(resolution)

				intervals = append(intervals, greencostsv1alpha1.PriceInterval{
					Start:     metav1.NewTime(intervalStart),
					End:       metav1.NewTime(intervalEnd),
					EurPerMWh: pt.PriceAmount,
				})
			}
		}
	}

	return intervals, nil
}

// parseResolution converts an ISO 8601 duration string to a time.Duration.
func parseResolution(s string) (time.Duration, error) {
	switch s {
	case "PT15M":
		return 15 * time.Minute, nil
	case "PT30M":
		return 30 * time.Minute, nil
	case "PT60M", "PT1H":
		return 60 * time.Minute, nil
	default:
		return 0, fmt.Errorf("unknown resolution %q", s)
	}
}

// parseTime parses ENTSO-E time strings. The platform uses "2006-01-02T15:04Z"
// as primary format; RFC3339 is the fallback.
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02T15:04Z", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
