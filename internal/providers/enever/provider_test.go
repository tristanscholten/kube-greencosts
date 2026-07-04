package enever

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
)

const (
	datumField           = "datum"
	sampleToken          = "token"
	sampleDutchTimestamp = "2026-06-26T00:00:00+02:00"
)

func TestFetchPricesFetchesTodayAndTomorrow(t *testing.T) {
	var gotPaths []string
	p := New(sampleToken, "")
	p.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPaths = append(gotPaths, req.URL.Path)
		if req.URL.Query().Get("token") != sampleToken {
			t.Fatalf("token query = %q, want sample token", req.URL.Query().Get("token"))
		}
		if req.URL.Query().Get("resolution") != "15" {
			t.Fatalf("resolution query = %q, want 15", req.URL.Query().Get("resolution"))
		}

		body := `{"status":"true","data":[{"datum":"2026-06-26T00:00:00+02:00","prijs":"0.10"}],"code":"ok"}`
		if strings.Contains(req.URL.Path, "morgen") {
			body = `{"status":"true","data":[{"datum":"2026-06-27T00:00:00+02:00","prijs":"0.20"}],"code":"ok"}`
		}
		return jsonResponse(http.StatusOK, body), nil
	})}

	got, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err != nil {
		t.Fatalf("FetchPrices() error = %v", err)
	}
	if len(gotPaths) != 2 || !strings.Contains(gotPaths[0], "vandaag") || !strings.Contains(gotPaths[1], "morgen") {
		t.Fatalf("requested paths = %v, want vandaag then morgen", gotPaths)
	}
	assertPricePoints(t, got,
		[]time.Time{
			time.Date(2026, 6, 26, 0, 0, 0, 0, time.FixedZone("", 2*60*60)),
			time.Date(2026, 6, 27, 0, 0, 0, 0, time.FixedZone("", 2*60*60)),
		},
		[]float64{100, 200},
	)
}

func TestFetchPricesContinuesWhenTomorrowUnavailable(t *testing.T) {
	p := New(sampleToken, "")
	p.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "morgen") {
			return jsonResponse(http.StatusServiceUnavailable, `{"status":"false","code":"not_ready"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"status":"true","data":[{"datum":"2026-06-26T00:00:00+02:00","prijs":"0.10"}],"code":"ok"}`), nil
	})}

	got, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err != nil {
		t.Fatalf("FetchPrices() error = %v", err)
	}
	assertPricePoints(t, got,
		[]time.Time{time.Date(2026, 6, 26, 0, 0, 0, 0, time.FixedZone("", 2*60*60))},
		[]float64{100},
	)
}

func TestFetchPricesFailsWhenTodayUnavailable(t *testing.T) {
	p := New(sampleToken, "")
	p.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"status":"false","code":"boom"}`), nil
	})}

	_, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{})
	if err == nil || !strings.Contains(err.Error(), "fetching today's enever prices") {
		t.Fatalf("FetchPrices() error = %v, want today's fetch context", err)
	}
}

func TestConvertDataUsesSpotPriceAndConvertsToMWh(t *testing.T) {
	got, err := New(sampleToken, "").convertData([]map[string]string{
		{datumField: sampleDutchTimestamp, priceField: "0.12345"},
	}, "vandaag")
	if err != nil {
		t.Fatalf("convertData() error = %v", err)
	}

	assertPricePoints(t, got,
		[]time.Time{time.Date(2026, 6, 26, 0, 0, 0, 0, time.FixedZone("", 2*60*60))},
		[]float64{123.45},
	)
}

func TestConvertDataUsesSupplierPrice(t *testing.T) {
	got, err := New(sampleToken, "anwb").convertData([]map[string]string{
		{datumField: "2026-06-26T00:15:00+02:00", priceField: "0.10", "prijsANWB": "0.22"},
	}, "vandaag")
	if err != nil {
		t.Fatalf("convertData() error = %v", err)
	}
	if got[0].EurPerMWh != 220 {
		t.Fatalf("convertData() price = %v, want supplier price converted to 220", got[0].EurPerMWh)
	}
}

func TestConvertDataReportsMalformedItems(t *testing.T) {
	tests := []struct {
		name string
		data []map[string]string
		want string
	}{
		{
			name: "missing datum",
			data: []map[string]string{{priceField: "0.1"}},
			want: "missing datum field",
		},
		{
			name: "bad datum",
			data: []map[string]string{{datumField: "not-time", priceField: "0.1"}},
			want: "parsing datum",
		},
		{
			name: "missing price",
			data: []map[string]string{{datumField: sampleDutchTimestamp}},
			want: `price key "prijs" not found`,
		},
		{
			name: "bad price",
			data: []map[string]string{{datumField: sampleDutchTimestamp, priceField: "NaN nope"}},
			want: "parsing price",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(sampleToken, "").convertData(tt.data, "vandaag")
			if err == nil {
				t.Fatal("convertData() accepted malformed item")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("convertData() error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestFactoryValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		spec    greencostsv1alpha1.EnergyPriceSourceSpec
		token   string
		wantErr string
	}{
		{
			name:    "missing enever config",
			wantErr: "eneverConfig is required",
		},
		{
			name: "empty token",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{}},
			},
			wantErr: "API token is empty",
		},
		{
			name: "unknown supplier",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{Supplier: "nope"}},
			},
			token:   sampleToken,
			wantErr: `unknown enever supplier "nope"`,
		},
		{
			name: "valid supplier lowercased",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Providers: greencostsv1alpha1.ProviderConfig{EneverConfig: &greencostsv1alpha1.EneverConfig{Supplier: "anwb"}},
			},
			token: sampleToken,
		},
	}

	factory := Factory()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := factory(tt.spec, tt.token)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Factory() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Factory() error = %v", err)
			}
			p, ok := provider.(*Provider)
			if !ok {
				t.Fatalf("Factory() provider = %T, want *Provider", provider)
			}
			if p.supplier != "ANWB" {
				t.Fatalf("Factory() supplier = %q, want ANWB", p.supplier)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
