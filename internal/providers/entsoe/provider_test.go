package entsoe

import (
	"strings"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const sampleToken = "token"

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
