package train

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testPoller(t *testing.T) *Poller {
	t.Helper()
	p, err := NewPoller(Config{ConsumerKey: "x", Interval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	p.latest = []Train{{ID: "1", PositionKind: "section"}}
	p.updated = time.Now().UTC()
	return p
}

func TestSegmentEstimateEndpointValidationAndResponse(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "x", FallbackSegmentDuration: 123 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := NewServer(p, "*").Handler()
	for _, path := range []string{
		"/api/v1/segment-estimates",
		"/api/v1/segment-estimates?from_station=Tokyo&to_station=odpt.Station:B&direction=inner",
		"/api/v1/segment-estimates?from_station=odpt.Station:A&to_station=odpt.Station:B&direction=inner&direction=outer",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s: code=%d headers=%v body=%s", path, w.Code, w.Header(), w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/segment-estimates?from_station=odpt.Station:A&to_station=odpt.Station:B&direction=inner", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var estimate SegmentEstimate
	if err := json.NewDecoder(w.Body).Decode(&estimate); err != nil {
		t.Fatal(err)
	}
	if estimate.ExpectedDurationSeconds != 123 || estimate.Method != "fallback_config" || estimate.Confidence != "low" || estimate.SampleCount != 0 || estimate.ObservedMedianSeconds != nil || estimate.GeneratedAt.IsZero() {
		t.Fatalf("estimate = %+v", estimate)
	}
}
func TestSnapshotAndCORS(t *testing.T) {
	s := NewServer(testPoller(t), "http://localhost:5173")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/trains", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(w.Body.String(), `"position_kind":"section"`) {
		t.Fatalf("code=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
}
func TestSSESendsInitialAndUpdate(t *testing.T) {
	p := testPoller(t)
	s := NewServer(p, "*")
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/trains/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "event: snapshot\n" {
		t.Fatalf("first SSE line=%q", line)
	}
	if _, err := reader.ReadString('\n'); err != nil { // initial data line
		t.Fatal(err)
	}
	if _, err := reader.ReadString('\n'); err != nil { // event separator
		t.Fatal(err)
	}
	// Wait until the handler's subscription has been installed, then publish an update.
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.RLock()
		n := len(p.watchers)
		p.mu.RUnlock()
		if n > 0 || time.Now().After(deadline) {
			if n == 0 {
				t.Fatal("SSE handler did not subscribe")
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	p.latest = []Train{{ID: "2", PositionKind: "section"}}
	p.updated = time.Now().UTC()
	snap := p.snapshotLocked()
	for watcher := range p.watchers {
		select {
		case watcher <- snap:
		default:
		}
	}
	p.mu.Unlock()
	line, err = reader.ReadString('\n')
	if err != nil || line != "event: snapshot\n" {
		t.Fatalf("update SSE line=%q err=%v", line, err)
	}
	data, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(data, `"id":"2"`) {
		t.Fatalf("update SSE data=%q err=%v", data, err)
	}
}

func TestSSEEmitsTransitionEvents(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	p.latest = []Train{{ID: "train-1", FromStation: "odpt.Station:A", ToStation: "odpt.Station:B", Direction: "inner", ObservedAt: firstAt, PositionKind: "section"}}
	p.updated = firstAt
	server := httptest.NewServer(NewServer(p, "*").Handler())
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/v1/trains/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	readEvent := func() (string, string) {
		t.Helper()
		event, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		data, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		return event, data
	}
	if event, _ := readEvent(); event != "event: snapshot\n" {
		t.Fatalf("initial event %q", event)
	}
	p.mu.Lock()
	p.latest = []Train{{ID: "train-1", FromStation: "odpt.Station:B", ToStation: "odpt.Station:C", Direction: "inner", ObservedAt: firstAt.Add(2 * time.Minute), PositionKind: "section"}}
	p.updated = firstAt.Add(2 * time.Minute)
	p.publishLocked(p.snapshotLocked())
	p.mu.Unlock()
	if event, data := readEvent(); event != "event: train.section_changed\n" || !strings.Contains(data, `"previous_segment":{"from_station":"odpt.Station:A"`) || !strings.Contains(data, `"current_segment":{"from_station":"odpt.Station:B"`) {
		t.Fatalf("section event=%q data=%q", event, data)
	}
	if event, data := readEvent(); event != "event: station.confirmed\n" || !strings.Contains(data, `"station":"odpt.Station:B"`) || !strings.Contains(data, `"confidence":"provider_confirmed_transition"`) {
		t.Fatalf("station event=%q data=%q", event, data)
	}
	if event, _ := readEvent(); event != "event: snapshot\n" {
		t.Fatalf("snapshot event %q", event)
	}
}
func TestUnavailableSnapshot(t *testing.T) {
	p, _ := NewPoller(Config{ConsumerKey: "x"})
	w := httptest.NewRecorder()
	NewServer(p, "*").Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/trains", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d", w.Code)
	}
}

func TestServiceStatusIsConservativeAboutNoObservations(t *testing.T) {
	p, _ := NewPoller(Config{ConsumerKey: "status-test-secret", Interval: time.Minute})
	p.now = func() time.Time { return time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC) } // 14:00 JST
	h := NewServer(p, "*").Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/service-status", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var status ServiceStatus
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "degraded" || status.Reason != "live_feed_unavailable_or_stale" || status.Confidence != "none" || status.ObservedAt != nil || status.GeneratedAt.IsZero() {
		t.Fatalf("status = %+v", status)
	}
	if strings.Contains(w.Body.String(), "status-test-secret") {
		t.Fatal("service status exposed the ODPT consumer key")
	}

	p.latest = []Train{{ID: "train-1", ObservedAt: time.Date(2026, 1, 5, 4, 59, 0, 0, time.UTC)}}
	p.updated = p.now()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/service-status", nil))
	status = ServiceStatus{}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.Status != "active" || status.Reason != "live_train_observations" || status.Confidence != "high" || status.ObservedAt == nil {
		t.Fatalf("active status = %+v", status)
	}

	p.latest = nil
	p.updated = p.now()
	p.now = func() time.Time { return time.Date(2026, 1, 5, 17, 30, 0, 0, time.UTC) } // 02:30 JST, following day
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/service-status", nil))
	status = ServiceStatus{}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "scheduled_off_hours" || status.Source != "jst_schedule_heuristic" || status.Confidence != "medium" || status.ResumesAt == nil {
		t.Fatalf("off-hours status = %+v", status)
	}
	if got := status.ResumesAt.In(jstLocation); got.Hour() != 4 || got.Minute() != 30 || got.Format("-07:00") != "+09:00" {
		t.Fatalf("off-hours resumption = %s", got)
	}

	p.now = func() time.Time { return time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC) }
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/service-status", nil))
	status = ServiceStatus{}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "unknown" || status.Reason != "no_live_train_observations" || status.Confidence != "low" || status.ResumesAt != nil {
		t.Fatalf("unknown status = %+v", status)
	}
}

func TestMethodsAndHealthSchema(t *testing.T) {
	p, _ := NewPoller(Config{ConsumerKey: "x"})
	h := NewServer(p, "http://example.test").Handler()
	for _, path := range []string{"/healthz", "/api/v1/trains", "/api/v1/service-status", "/api/v1/segment-estimates", "/api/v1/trains/stream"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, OPTIONS" {
			t.Fatalf("%s: code=%d allow=%q", path, w.Code, w.Header().Get("Allow"))
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"status":"unavailable"`) || !strings.Contains(w.Body.String(), `"stale":true`) || !strings.Contains(w.Body.String(), `"age_seconds":0`) || strings.Contains(w.Body.String(), `"error":`) {
		t.Fatalf("unexpected health response: %s", w.Body.String())
	}
}

func TestSubscribeInstallsBeforeInitialSnapshotIsSent(t *testing.T) {
	p := testPoller(t)
	initial, updates, cancel := p.Subscribe()
	defer cancel()
	if initial.Trains[0].ID != "1" {
		t.Fatalf("initial = %+v", initial)
	}
	p.mu.Lock()
	p.latest = []Train{{ID: "2", PositionKind: "section"}}
	p.updated = time.Now().UTC()
	p.publishLocked(p.snapshotLocked())
	p.mu.Unlock()
	select {
	case got := <-updates:
		if got.Trains[0].ID != "2" {
			t.Fatalf("update = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent update was lost")
	}
}
