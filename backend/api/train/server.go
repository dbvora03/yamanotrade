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
	send := func(snap Snapshot) bool {
		body, err := json.Marshal(snap)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", body)
		flusher.Flush()
		return err == nil
	}
	initial, updates, cancel := s.poller.Subscribe()
	defer cancel()
	if !send(initial) {
		return
	}
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
			if !send(snap) {
				return
			}
			lastHealth = healthState(snap)
		case <-ticker.C:
			snap := s.poller.Snapshot()
			if healthState(snap) != lastHealth {
				if !send(snap) {
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
