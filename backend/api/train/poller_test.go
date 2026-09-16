package train

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollNormalizesODPTAndRetainsLastSuccess(t *testing.T) {
	requests := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("odpt:railway"); got != "odpt.Railway:JR-East.Yamanote" {
			t.Errorf("railway = %q", got)
		}
		if r.URL.Query().Get("acl:consumerKey") != "test-key" {
			t.Error("consumer key missing")
		}
		if requests == 2 {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"@id":"urn:train:1","odpt:trainNumber":"1234G","odpt:railDirection":"odpt.RailDirection:JR-East.Yamanote.Inner","odpt:fromStation":"odpt.Station:JR-East.Yamanote.Tokyo","odpt:toStation":"odpt.Station:JR-East.Yamanote.Kanda","odpt:delay":90,"dc:date":"2025-01-02T03:04:05Z"}]`))
	}))
	defer source.Close()
	p, err := NewPoller(Config{ConsumerKey: "test-key", Endpoint: source.URL, Interval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := p.Snapshot()
	if len(first.Trains) != 1 || first.Trains[0].PositionKind != "section" || first.Trains[0].DelaySeconds != 90 {
		t.Fatalf("unexpected snapshot: %+v", first)
	}
	if got := first.Trains[0].ObservedAt.Format(time.RFC3339); got != "2025-01-02T03:04:05Z" {
		t.Fatalf("observed_at = %s", got)
	}
	if err := p.Poll(context.Background()); err == nil {
		t.Fatal("expected upstream error")
	}
	second := p.Snapshot()
	if len(second.Trains) != 1 || !second.Stale || second.LastError == "" {
		t.Fatalf("failure erased or hid data: %+v", second)
	}
}

func TestPollerUsesCanonicalIDAndDeterministicFallback(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"@id":"urn:resource","owl:sameAs":"odpt.Train:JR-East.Yamanote.1234G","odpt:trainNumber":"1234G","odpt:railDirection":"inner","odpt:fromStation":"station-a","odpt:toStation":"station-b"},
			{"@id":"urn:resource-only","odpt:trainNumber":"2345G","odpt:fromStation":"station-b","odpt:toStation":"station-c"},
			{"odpt:trainNumber":"3456G","odpt:railDirection":"outer","odpt:fromStation":"station-c","odpt:toStation":"station-d"}
		]`))
	}))
	defer source.Close()
	p, err := NewPoller(Config{ConsumerKey: "test-key", Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	trains := p.Snapshot().Trains
	if got := trains[0].ID; got != "odpt.Train:JR-East.Yamanote.1234G" {
		t.Fatalf("canonical ID = %q", got)
	}
	if got := trains[1].ID; got != "urn:resource-only" {
		t.Fatalf("resource ID = %q", got)
	}
	if got := trains[2].ID; !strings.HasPrefix(got, "odpt:Train:fallback:") {
		t.Fatalf("fallback ID = %q", got)
	}
}

func TestPollKeepsStationObservationsAndSkipsMissingOrigins(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"odpt:trainNumber":"at-station","odpt:fromStation":"station-a","odpt:toStation":null},
			{"odpt:trainNumber":"missing-origin","odpt:fromStation":null,"odpt:toStation":"station-b"},
			{"odpt:trainNumber":"between-stations","odpt:fromStation":"station-a","odpt:toStation":"station-b"}
		]`))
	}))
	defer source.Close()
	p, err := NewPoller(Config{ConsumerKey: "test-key", Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	trains := p.Snapshot().Trains
	if len(trains) != 2 || trains[0].TrainNumber != "at-station" || trains[0].PositionKind != "station" || trains[0].ToStation != "" {
		t.Fatalf("unexpected station observation: %+v", trains)
	}
	if trains[0].Progress != nil || trains[1].TrainNumber != "between-stations" || trains[1].PositionKind != "section" {
		t.Fatalf("unexpected trains: %+v", trains)
	}
}

func TestPollFailureDoesNotExposeConsumerKeyAndPublishesState(t *testing.T) {
	const key = "a-secret-consumer-key"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "request URL includes acl:consumerKey="+key, http.StatusBadGateway)
	}))
	defer source.Close()
	p, err := NewPoller(Config{ConsumerKey: key, Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, updates, cancel := p.Subscribe()
	defer cancel()
	if err := p.Poll(context.Background()); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked key: %v", err)
	}
	select {
	case snap := <-updates:
		if !snap.Stale || strings.Contains(snap.LastError, key) {
			t.Fatalf("unsafe failure snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("failure did not notify subscriber")
	}
	w := httptest.NewRecorder()
	NewServer(p, "*").Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/trains", nil))
	if strings.Contains(w.Body.String(), key) {
		t.Fatalf("API response leaked key: %s", w.Body.String())
	}
}

func TestDelayCapsWithoutOverflow(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "x", Interval: time.Duration(math.MaxInt64)})
	if err != nil {
		t.Fatal(err)
	}
	p.random = func() float64 { return 0.5 }
	if got := p.delay(math.MaxInt); got != 5*time.Minute {
		t.Fatalf("delay = %s", got)
	}
}

func TestPollDoesNotFollowRedirectWithConsumerKey(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer source.Close()
	p, err := NewPoller(Config{ConsumerKey: "secret", Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(context.Background()); err == nil || err.Error() != "ODPT returned HTTP 302" {
		t.Fatalf("unexpected redirect error: %v", err)
	}
	if reached.Load() {
		t.Fatal("client followed a redirect containing the consumer key")
	}
}

func TestNewPollerRequiresKey(t *testing.T) {
	if _, err := NewPoller(Config{}); err == nil {
		t.Fatal("expected missing key error")
	}
}

func TestNewPollerDefaultsToFiveSecondInterval(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if p.config.Interval != 5*time.Second {
		t.Fatalf("interval = %s", p.config.Interval)
	}
}
