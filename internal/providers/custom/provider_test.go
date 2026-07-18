package custom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFetchPricesSendsContextAndSortsResponseByStartTime(t *testing.T) {
	const token = "test-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		if got := r.URL.Query().Get("biddingZone"); got != "NL" {
			t.Fatalf("biddingZone query = %q, want NL", got)
		}
		if got := r.URL.Query().Get("date"); got != "2026-06-26" {
			t.Fatalf("date query = %q, want 2026-06-26", got)
		}

		_ = json.NewEncoder(w).Encode([]apiPriceInterval{
			{Start: "2026-06-26T02:00:00Z", EurPerMWh: 10},
			{Start: "2026-06-26T00:00:00Z", EurPerMWh: 30},
		})
	}))
	defer server.Close()

	got, err := New(server.URL, token).FetchPrices(context.Background(), providers.FetchPricesRequest{
		BiddingZone: "NL",
		Date:        time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("FetchPrices() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("FetchPrices() returned %d points, want 2", len(got))
	}

	wantStarts := []time.Time{
		time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 2, 0, 0, 0, time.UTC),
	}
	for i, want := range wantStarts {
		if !got[i].At.Time.Equal(want) {
			t.Fatalf("FetchPrices()[%d].At = %s, want %s", i, got[i].At.Time, want)
		}
	}
	if got[0].EurPerMWh != 30 || got[1].EurPerMWh != 10 {
		t.Fatalf("FetchPrices() prices = %v then %v, want prices attached to chronological timestamps", got[0].EurPerMWh, got[1].EurPerMWh)
	}
}

func TestFetchPricesReportsBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"start":`))
	}))
	defer server.Close()

	_, err := New(server.URL, "").FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err == nil {
		t.Fatal("FetchPrices() accepted invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing price response") {
		t.Fatalf("FetchPrices() error = %q, want parsing context", err)
	}
}

func TestFetchPricesReportsHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := New(server.URL, "").FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err == nil {
		t.Fatal("FetchPrices() accepted non-OK status")
	}
	if !strings.Contains(err.Error(), "provider returned HTTP 429") {
		t.Fatalf("FetchPrices() error = %q, want HTTP status context", err)
	}
}

func TestFetchPricesReportsInvalidURL(t *testing.T) {
	_, err := New("://bad-url", "").FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err == nil || !strings.Contains(err.Error(), "building request") {
		t.Fatalf("FetchPrices() error = %v, want request-build context", err)
	}
}

func TestFactoryValidatesConfig(t *testing.T) {
	factory := Factory()

	tests := []struct {
		name    string
		spec    greencostsv1alpha1.EnergyPriceSourceSpec
		wantErr string
	}{
		{
			name:    "missing custom config",
			wantErr: "customProviderConfig is required",
		},
		{
			name: "empty url",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{
					CustomProviderConfig: &greencostsv1alpha1.CustomProviderConfig{},
				},
			},
			wantErr: "customProviderConfig.url must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := factory(tt.spec, "token")
			if err == nil {
				t.Fatal("Factory() error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Factory() error = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestFactoryBuildsProvider(t *testing.T) {
	got, err := Factory()(greencostsv1alpha1.EnergyPriceSourceSpec{
		Providers: greencostsv1alpha1.ProviderConfig{
			CustomProviderConfig: &greencostsv1alpha1.CustomProviderConfig{URL: "https://prices.example.test"},
		},
	}, "token")
	if err != nil {
		t.Fatalf("Factory() error = %v", err)
	}

	provider, ok := got.(*Provider)
	if !ok {
		t.Fatalf("Factory() provider type = %T, want *Provider", got)
	}
	if provider.url != "https://prices.example.test" {
		t.Fatalf("Factory() url = %q", provider.url)
	}
	if provider.bearerToken != "token" {
		t.Fatalf("Factory() bearerToken = %q", provider.bearerToken)
	}
}

func TestConvertIntervalsRejectsBadTimestamp(t *testing.T) {
	_, err := convertIntervals([]apiPriceInterval{{Start: "not-time", EurPerMWh: 42}})
	if err == nil {
		t.Fatal("convertIntervals() accepted invalid timestamp")
	}
	if !strings.Contains(err.Error(), "interval 0: parsing start") {
		t.Fatalf("convertIntervals() error = %q, want interval context", err)
	}
}

func TestSortPricePointsByStartTimeKeepsEqualTimes(t *testing.T) {
	start := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)
	points := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(start), EurPerMWh: 20},
		{At: metav1.NewTime(start.Add(-time.Hour)), EurPerMWh: 10},
		{At: metav1.NewTime(start), EurPerMWh: 30},
	}

	sortPricePointsByStartTime(points)
	if !points[0].At.Time.Equal(start.Add(-time.Hour)) {
		t.Fatalf("first point time = %s, want earliest", points[0].At.Time)
	}
	if !points[1].At.Time.Equal(start) || !points[2].At.Time.Equal(start) {
		t.Fatalf("equal-time points sorted to %s and %s, want both %s", points[1].At.Time, points[2].At.Time, start)
	}
}
