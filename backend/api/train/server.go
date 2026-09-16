package train

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	poller    *Poller
	origins   map[string]bool
	heartbeat time.Duration
}

func NewServer(p *Poller, allowedOrigins string) *Server {
	origins := map[string]bool{}
	for _, v := range strings.Split(allowedOrigins, ",") {
		if v = strings.TrimSpace(v); v != "" {
			origins[v] = true
		}
	}
	if len(origins) == 0 {
		origins["*"] = true
	}
	return &Server{poller: p, origins: origins, heartbeat: 15 * time.Second}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/trains", s.snapshot)
	mux.HandleFunc("/api/v1/service-status", s.serviceStatus)
	mux.HandleFunc("/api/v1/segment-estimates", s.segmentEstimate)
	mux.HandleFunc("/api/v1/trains/stream", s.stream)
	return s.cors(mux)
}
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.origins["*"] {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if s.origins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snap := s.poller.Snapshot()
	health := Health{GeneratedAt: snap.GeneratedAt, Stale: snap.Stale, AgeSeconds: snap.AgeSeconds, LastError: snap.LastError}
	if snap.GeneratedAt == nil {
		health.Status = "unavailable"
		writeJSON(w, http.StatusServiceUnavailable, health)
		return
	}
	health.Status = "ok"
	code := http.StatusOK
	if snap.Stale {
		health.Status = "stale"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, health)
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snap := s.poller.Snapshot()
	code := http.StatusOK
	if snap.GeneratedAt == nil {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, snap)
}

func (s *Server) serviceStatus(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.poller.ServiceStatus())
}

func (s *Server) segmentEstimate(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	from, ok := requiredStationParam(r, "from_station")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from_station must be one canonical ODPT station identifier"})
		return
	}
	to, ok := requiredStationParam(r, "to_station")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to_station must be one canonical ODPT station identifier"})
		return
	}
	direction, ok := requiredExactParam(r, "direction")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "direction is required and must be unambiguous"})
		return
	}
	writeJSON(w, http.StatusOK, s.poller.SegmentEstimate(from, to, direction))
}

func requiredStationParam(r *http.Request, name string) (string, bool) {
	v, ok := requiredExactParam(r, name)
	if !ok || !strings.HasPrefix(v, "odpt.Station:") {
		return "", false
	}
	return v, true
}

func requiredExactParam(r *http.Request, name string) (string, bool) {
	values, ok := r.URL.Query()[name]
	if !ok || len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", false
	}
	return values[0], true
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		flusher.Flush()
		return err == nil
	}
	initial, updates, cancel := s.poller.Subscribe()
	defer cancel()
	if !send("snapshot", initial) {
		return
	}
	previous := sectionByTrain(initial)
	lastHealth := healthState(initial)
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-updates:
			if !ok {
				return
			}
			if !send("snapshot", snap) {
				return
			}
			if !sendTransitionEvents(send, previous, snap) {
				return
			}
			previous = sectionByTrain(snap)
			lastHealth = healthState(snap)
		case <-ticker.C:
			snap := s.poller.Snapshot()
			if healthState(snap) != lastHealth {
				if !send("snapshot", snap) {
					return
				}
				lastHealth = healthState(snap)
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type trainSection struct {
	fromStation string
	toStation   string
	direction   string
	observedAt  time.Time
}

func sectionByTrain(snap Snapshot) map[string]trainSection {
	sections := make(map[string]trainSection, len(snap.Trains))
	for _, train := range snap.Trains {
		sections[train.ID] = trainSection{fromStation: train.FromStation, toStation: train.ToStation, direction: train.Direction, observedAt: train.ObservedAt}
	}
	return sections
}

type sectionRef struct {
	FromStation string `json:"from_station"`
	ToStation   string `json:"to_station"`
	Direction   string `json:"direction"`
}

// sendTransitionEvents emits only between consecutive snapshots seen by this
// client. station.confirmed is a bounded inference: the train was observed in
// the prior section, then observed in the next one; it is not an exact arrival.
func sendTransitionEvents(send func(string, any) bool, previous map[string]trainSection, current Snapshot) bool {
	for _, train := range current.Trains {
		before, ok := previous[train.ID]
		if !ok || (before.fromStation == train.FromStation && before.toStation == train.ToStation && before.direction == train.Direction) {
			continue
		}
		prior := sectionRef{FromStation: before.fromStation, ToStation: before.toStation, Direction: before.direction}
		next := sectionRef{FromStation: train.FromStation, ToStation: train.ToStation, Direction: train.Direction}
		if !send("train.section_changed", struct {
			TrainID         string     `json:"train_id"`
			PreviousSegment sectionRef `json:"previous_segment"`
			CurrentSegment  sectionRef `json:"current_segment"`
			ObservedAt      time.Time  `json:"observed_at"`
		}{train.ID, prior, next, train.ObservedAt}) {
			return false
		}
		if before.toStation != train.FromStation || before.direction != train.Direction {
			continue
		}
		start, end := before.observedAt, train.ObservedAt
		if end.Before(start) {
			start, end = end, start
		}
		if !send("station.confirmed", struct {
			TrainID            string    `json:"train_id"`
			Station            string    `json:"station"`
			ArrivalWindowStart time.Time `json:"arrival_window_start"`
			ArrivalWindowEnd   time.Time `json:"arrival_window_end"`
			ConfirmedAt        time.Time `json:"confirmed_at"`
			Confidence         string    `json:"confidence"`
		}{train.ID, before.toStation, start, end, train.ObservedAt, "provider_confirmed_transition"}) {
			return false
		}
	}
	return true
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", "GET, OPTIONS")
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	return false
}

// Age changes continuously and is not an SSE state transition; stale/error and
// generation changes are. This lets heartbeats announce time-based staleness.
func healthState(s Snapshot) string {
	generated := ""
	if s.GeneratedAt != nil {
		generated = s.GeneratedAt.UTC().Format(time.RFC3339Nano)
	}
	return generated + "\x00" + fmt.Sprintf("%t", s.Stale) + "\x00" + s.LastError
}
