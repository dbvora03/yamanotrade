package train

import (
	"bufio"
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
func TestUnavailableSnapshot(t *testing.T) {
	p, _ := NewPoller(Config{ConsumerKey: "x"})
	w := httptest.NewRecorder()
	NewServer(p, "*").Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/trains", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d", w.Code)
	}
}

func TestMethodsAndHealthSchema(t *testing.T) {
	p, _ := NewPoller(Config{ConsumerKey: "x"})
	h := NewServer(p, "http://example.test").Handler()
	for _, path := range []string{"/healthz", "/api/v1/trains", "/api/v1/trains/stream"} {
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
