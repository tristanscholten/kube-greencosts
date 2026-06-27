package custom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tristanscholten/kube-greencosts/internal/providers"
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

func TestConvertIntervalsRejectsBadTimestamp(t *testing.T) {
	_, err := convertIntervals([]apiPriceInterval{{Start: "not-time", EurPerMWh: 42}})
	if err == nil {
		t.Fatal("convertIntervals() accepted invalid timestamp")
	}
	if !strings.Contains(err.Error(), "interval 0: parsing start") {
		t.Fatalf("convertIntervals() error = %q, want interval context", err)
	}
}
