package entsoe

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

const sampleToken = "token"

func TestFetchPricesBuildsRequestAndParsesPublication(t *testing.T) {
	p := New(AreaCodes["NL"], sampleToken)
	p.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		wantQuery := map[string]string{
			"documentType":  documentType,
			"in_Domain":     AreaCodes["NL"],
			"out_Domain":    AreaCodes["NL"],
			"periodStart":   "202606260000",
			"periodEnd":     "202606280000",
			"securityToken": sampleToken,
		}
		for key, want := range wantQuery {
			if got := query.Get(key); got != want {
				t.Fatalf("query %s = %q, want %q", key, got, want)
			}
		}

		return xmlResponse(http.StatusOK, `<Publication_MarketDocument>
			<TimeSeries><Period>
				<timeInterval><start>2026-06-26T00:00Z</start></timeInterval>
				<resolution>PT15M</resolution>
				<Point><position>1</position><price.amount>10.5</price.amount></Point>
				<Point><position>2</position><price.amount>20.5</price.amount></Point>
			</Period></TimeSeries>
		</Publication_MarketDocument>`), nil
	})}

	got, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{Date: time.Date(2026, 6, 26, 15, 30, 0, 0, time.FixedZone("CEST", 2*60*60))})
	if err != nil {
		t.Fatalf("FetchPrices() error = %v", err)
	}
	assertPricePoints(t, got,
		[]time.Time{
			time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 6, 26, 0, 15, 0, 0, time.UTC),
		},
		[]float64{10.5, 20.5},
	)
}

func TestFetchPricesReportsAcknowledgements(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "non-200 acknowledgement", status: http.StatusBadRequest, want: "ENTSO-E API error (HTTP 400): code 999: bad token"},
		{name: "200 acknowledgement", status: http.StatusOK, want: "ENTSO-E API error: code 999: bad token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(AreaCodes["NL"], sampleToken)
			p.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return xmlResponse(tt.status, `<Acknowledgement_MarketDocument>
					<Reason><code>999</code><text>bad token</text></Reason>
				</Acknowledgement_MarketDocument>`), nil
			})}

			_, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{Date: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FetchPrices() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestFetchPricesRejectsMalformedPublication(t *testing.T) {
	p := New(AreaCodes["NL"], sampleToken)
	p.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return xmlResponse(http.StatusOK, `<Publication_MarketDocument><TimeSeries>`), nil
	})}

	_, err := p.FetchPrices(context.Background(), providers.FetchPricesRequest{Date: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)})
	if err == nil || !strings.Contains(err.Error(), "parsing ENTSO-E XML") {
		t.Fatalf("FetchPrices() error = %v, want XML parse context", err)
	}
}

func TestParsePublicationBuildsChronologicalPricePoints(t *testing.T) {
	body := []byte(`<Publication_MarketDocument>
		<TimeSeries>
			<Period>
				<timeInterval>
					<start>2026-06-26T00:00Z</start>
					<end>2026-06-26T02:00Z</end>
				</timeInterval>
				<resolution>PT60M</resolution>
				<Point><position>1</position><price.amount>42.5</price.amount></Point>
				<Point><position>2</position><price.amount>-1.25</price.amount></Point>
			</Period>
		</TimeSeries>
	</Publication_MarketDocument>`)

	got, err := parsePublication(body)
	if err != nil {
		t.Fatalf("parsePublication() error = %v", err)
	}
	wantTimes := []time.Time{
		time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	}
	wantPrices := []float64{42.5, -1.25}
	assertPricePoints(t, got, wantTimes, wantPrices)
}

func TestParsePublicationRejectsUnsupportedResolution(t *testing.T) {
	body := []byte(`<Publication_MarketDocument>
		<TimeSeries><Period>
			<timeInterval><start>2026-06-26T00:00Z</start></timeInterval>
			<resolution>PT5M</resolution>
		</Period></TimeSeries>
	</Publication_MarketDocument>`)

	_, err := parsePublication(body)
	if err == nil {
		t.Fatal("parsePublication() accepted unsupported resolution")
	}
	if !strings.Contains(err.Error(), `unsupported resolution "PT5M"`) {
		t.Fatalf("parsePublication() error = %q, want unsupported resolution context", err)
	}
}

func TestParseAcknowledgement(t *testing.T) {
	got := parseAcknowledgement([]byte(`<Acknowledgement_MarketDocument>
		<Reason><code>999</code><text>bad token</text></Reason>
	</Acknowledgement_MarketDocument>`))
	if got != "code 999: bad token" {
		t.Fatalf("parseAcknowledgement() = %q", got)
	}

	if got := parseAcknowledgement([]byte(`<Publication_MarketDocument/>`)); got != "" {
		t.Fatalf("parseAcknowledgement(non-error) = %q, want empty string", got)
	}
}

func TestFactoryValidatesConfig(t *testing.T) {
	tests := []struct {
		name         string
		spec         greencostsv1alpha1.EnergyPriceSourceSpec
		token        string
		wantAreaCode string
		wantErr      string
	}{
		{
			name:    "missing entsoe config",
			wantErr: "entsoeConfig is required",
		},
		{
			name: "unknown bidding zone without explicit area code",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				BiddingZone: "XX",
				Providers:   greencostsv1alpha1.ProviderConfig{EntsoeConfig: &greencostsv1alpha1.EntsoeConfig{}},
			},
			token:   sampleToken,
			wantErr: `no built-in ENTSO-E area code for biddingZone "XX"`,
		},
		{
			name: "empty token",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				BiddingZone: "NL",
				Providers:   greencostsv1alpha1.ProviderConfig{EntsoeConfig: &greencostsv1alpha1.EntsoeConfig{}},
			},
			wantErr: "security token is empty",
		},
		{
			name: "built-in area code",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				BiddingZone: "NL",
				Providers:   greencostsv1alpha1.ProviderConfig{EntsoeConfig: &greencostsv1alpha1.EntsoeConfig{}},
			},
			token:        sampleToken,
			wantAreaCode: AreaCodes["NL"],
		},
		{
			name: "explicit area code overrides zone lookup",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				BiddingZone: "XX",
				Providers: greencostsv1alpha1.ProviderConfig{EntsoeConfig: &greencostsv1alpha1.EntsoeConfig{
					AreaCode: "10YTEST-------X",
				}},
			},
			token:        sampleToken,
			wantAreaCode: "10YTEST-------X",
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
			if p.areaCode != tt.wantAreaCode {
				t.Fatalf("Factory() areaCode = %q, want %q", p.areaCode, tt.wantAreaCode)
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

func xmlResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
