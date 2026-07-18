package energyzero

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
)

func TestNewRequestBuildsQuarterHourElectricityURL(t *testing.T) {
	p := New()
	p.baseURL = "https://example.test/prices"

	req, err := p.newRequest(context.Background(), time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("newRequest() error = %v", err)
	}

	q := req.URL.Query()
	if req.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", req.Method)
	}
	if got := q.Get("date"); got != "28-06-2026" {
		t.Fatalf("date = %q, want dd-mm-yyyy", got)
	}
	if got := q.Get("interval"); got != interval {
		t.Fatalf("interval = %q, want %q", got, interval)
	}
	if got := q.Get("energyType"); got != energyType {
		t.Fatalf("energyType = %q, want %q", got, energyType)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
}

func TestNewRequestReportsInvalidBaseURL(t *testing.T) {
	p := New()
	p.baseURL = "://bad-url"

	_, err := p.newRequest(context.Background(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "parsing EnergyZero base URL") {
		t.Fatalf("newRequest() error = %v, want base URL context", err)
	}
}

func TestFetchPricesCallsPublicEndpointWithoutAuth(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if got := r.URL.Query().Get("interval"); got != interval {
			t.Fatalf("interval = %q, want %q", got, interval)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"interval":"RESPONSE_INTERVAL_QUARTER",
			"base":[{"start":"2026-06-28T00:15:00Z","price":{"value":"0.20"}}]
		}`))
	}))
	defer server.Close()

	p := New()
	p.baseURL = server.URL
	got, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{Date: time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("FetchPrices() error = %v", err)
	}
	if authHeader != "" {
		t.Fatalf("Authorization header = %q, want empty", authHeader)
	}
	assertPricePoints(t, got,
		[]time.Time{time.Date(2026, 6, 28, 0, 15, 0, 0, time.UTC)},
		[]float64{200},
	)
}

func TestParseResponseConvertsBasePricesAndSortsChronologically(t *testing.T) {
	got, err := parseResponse([]byte(`{
		"interval":"RESPONSE_INTERVAL_QUARTER",
		"base":[
			{"start":"2026-06-28T00:30:00Z","price":{"value":"-0.00207"}},
			{"start":"2026-06-28T00:00:00Z","price":{"value":"0.14556"}},
			{"start":"2026-06-28T00:15:00Z","price":{"value":"1.2e-1"}}
		]
	}`))
	if err != nil {
		t.Fatalf("parseResponse() error = %v", err)
	}

	assertPricePoints(t, got,
		[]time.Time{
			time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 6, 28, 0, 15, 0, 0, time.UTC),
			time.Date(2026, 6, 28, 0, 30, 0, 0, time.UTC),
		},
		[]float64{145.56, 120, -2.07},
	)
}

func TestFactoryRejectsUnsupportedConfig(t *testing.T) {
	tests := []struct {
		name       string
		spec       greencostsv1alpha1.EnergyPriceSourceSpec
		token      string
		wantErrSub string
	}{
		{
			name:       "non NL bidding zone",
			spec:       greencostsv1alpha1.EnergyPriceSourceSpec{BiddingZone: "DE-LU"},
			wantErrSub: `only supports biddingZone "NL"`,
		},
		{
			name:       "token not accepted",
			spec:       greencostsv1alpha1.EnergyPriceSourceSpec{BiddingZone: "NL"},
			token:      "secret",
			wantErrSub: "must not be configured with a token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Factory()(tt.spec, tt.token)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("Factory() error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestFactoryBuildsPublicProvider(t *testing.T) {
	got, err := Factory()(greencostsv1alpha1.EnergyPriceSourceSpec{BiddingZone: "NL"}, "")
	if err != nil {
		t.Fatalf("Factory() error = %v", err)
	}
	if _, ok := got.(*Provider); !ok {
		t.Fatalf("Factory() provider = %T, want *Provider", got)
	}
}

func TestFetchPricesReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	p := New()
	p.baseURL = server.URL
	_, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{Date: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("FetchPrices() error = %v, want HTTP 502", err)
	}
}

func TestParseResponseReportsMalformedData(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "bad json", body: `{`, want: "parsing EnergyZero JSON"},
		{name: "hour interval", body: `{"interval":"RESPONSE_INTERVAL_HOUR","base":[{"start":"2026-06-28T00:00:00Z","price":{"value":"0.1"}}]}`, want: "want RESPONSE_INTERVAL_QUARTER"},
		{name: "empty base", body: `{"interval":"RESPONSE_INTERVAL_QUARTER","base":[]}`, want: "no base prices"},
		{name: "bad start", body: `{"interval":"RESPONSE_INTERVAL_QUARTER","base":[{"start":"bad","price":{"value":"0.1"}}]}`, want: "parsing start"},
		{name: "bad price", body: `{"interval":"RESPONSE_INTERVAL_QUARTER","base":[{"start":"2026-06-28T00:00:00Z","price":{"value":"NaN nope"}}]}`, want: "parsing price"},
		{name: "nan price", body: `{"interval":"RESPONSE_INTERVAL_QUARTER","base":[{"start":"2026-06-28T00:00:00Z","price":{"value":"NaN"}}]}`, want: "not a finite decimal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseResponse([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseResponse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func assertPricePoints(t *testing.T, got []greencostsv1alpha1.PricePoint, wantTimes []time.Time, wantPrices []float64) {
	t.Helper()
	if len(got) != len(wantTimes) {
		t.Fatalf("got %d price points, want %d", len(got), len(wantTimes))
	}
	for i := range got {
		if !got[i].At.Time.Equal(wantTimes[i]) {
			t.Fatalf("point %d time = %s, want %s", i, got[i].At.Time, wantTimes[i])
		}
		if got[i].EurPerMWh != wantPrices[i] {
			t.Fatalf("point %d price = %v, want %v", i, got[i].EurPerMWh, wantPrices[i])
		}
	}
}

func TestLiveResponseShape(t *testing.T) {
	body := []byte(`{
		"interval":"RESPONSE_INTERVAL_QUARTER",
		"range":{"start":"2026-06-26T22:00:00Z","end":"2026-06-29T22:00:00Z"},
		"base":[{"start":"2026-06-26T22:00:00Z","end":"2026-06-26T22:15:00Z","price":{"value":"0.14556"}}],
		"base_with_vat":[{"start":"2026-06-26T22:00:00Z","end":"2026-06-26T22:15:00Z","price":{"value":"0.1761276"}}],
		"all_in":[{"start":"2026-06-26T22:00:00Z","end":"2026-06-26T22:15:00Z","price":{"value":"0.23717"}}],
		"all_in_with_vat":[{"start":"2026-06-26T22:00:00Z","end":"2026-06-26T22:15:00Z","price":{"value":"0.2869757"}}]
	}`)

	got, err := parseResponse(body)
	if err != nil {
		t.Fatalf("parseResponse() error = %v", err)
	}
	assertPricePoints(t, got,
		[]time.Time{time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)},
		[]float64{145.56},
	)
}
