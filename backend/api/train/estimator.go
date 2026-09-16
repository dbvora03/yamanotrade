package train

import (
	"sort"
	"strings"
	"time"
)

// segmentKey uses normalized (trimmed) canonical ODPT identifiers. It is an
// internal key only; API requests must still provide unambiguous identifiers.
type segmentKey struct {
	from, to, direction string
}

func newSegmentKey(from, to, direction string) segmentKey {
	return segmentKey{strings.TrimSpace(from), strings.TrimSpace(to), strings.TrimSpace(direction)}
}

type segmentEstimator struct {
	fallback time.Duration
	limit    int
	history  map[segmentKey][]time.Duration
}

func newSegmentEstimator(fallback time.Duration, limit int) *segmentEstimator {
	return &segmentEstimator{fallback: fallback, limit: limit, history: make(map[segmentKey][]time.Duration)}
}

func (e *segmentEstimator) record(key segmentKey, duration time.Duration) {
	if duration <= 0 {
		return
	}
	history := append(e.history[key], duration)
	if len(history) > e.limit {
		history = history[len(history)-e.limit:]
	}
	e.history[key] = history
}

func (e *segmentEstimator) estimate(key segmentKey) (duration time.Duration, median *time.Duration, samples int, method, confidence string) {
	history := e.history[key]
	samples = len(history)
	if samples == 0 {
		return e.fallback, nil, 0, "fallback_config", "low"
	}
	values := append([]time.Duration(nil), history...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	// For an even sample size, use the midpoint of the two central observations.
	m := values[samples/2]
	if samples%2 == 0 {
		m = values[samples/2-1]/2 + values[samples/2]/2
	}
	confidence = "medium"
	if samples >= 3 {
		confidence = "high"
	}
	return m, &m, samples, "observed_median", confidence
}
