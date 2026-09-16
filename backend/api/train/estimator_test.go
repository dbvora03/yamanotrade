package train

import (
	"testing"
	"time"
)

func TestSegmentEstimatorFallbackAndMedian(t *testing.T) {
	e := newSegmentEstimator(150*time.Second, 3)
	key := newSegmentKey("odpt.Station:A", "odpt.Station:B", "inner")
	duration, median, samples, method, confidence := e.estimate(key)
	if duration != 150*time.Second || median != nil || samples != 0 || method != "fallback_config" || confidence != "low" {
		t.Fatalf("fallback = %s %v %d %s %s", duration, median, samples, method, confidence)
	}
	for _, value := range []time.Duration{100 * time.Second, 500 * time.Second, 200 * time.Second, 300 * time.Second} {
		e.record(key, value)
	}
	duration, median, samples, method, confidence = e.estimate(key)
	if duration != 300*time.Second || median == nil || *median != 300*time.Second || samples != 3 || method != "observed_median" || confidence != "high" {
		t.Fatalf("median = %s %v %d %s %s", duration, median, samples, method, confidence)
	}
}

func TestProgressIsVisualAndClampedBeforeSectionChange(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "x", FallbackSegmentDuration: 100 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 1, 2, 3, 10, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	train := Train{ID: "train-1", FromStation: "odpt.Station:A", ToStation: "odpt.Station:B", Direction: "inner", ObservedAt: now.Add(-10 * time.Minute), PositionKind: "section"}
	p.latest = []Train{train}
	p.segmentEntered[train.ID] = trainObservation{segment: newSegmentKey(train.FromStation, train.ToStation, train.Direction), enteredAt: train.ObservedAt}
	snapshot := p.Snapshot()
	progress := snapshot.Trains[0].Progress
	if progress == nil || progress.EstimatedFraction != 0.99 || progress.Method != "fallback_config" || progress.Confidence != "low" {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestPollerRecordsContiguousTransitionUsingObservationTimeline(t *testing.T) {
	p, err := NewPoller(Config{ConsumerKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	p.observeTransitionsLocked([]Train{{ID: "train-1", FromStation: "odpt.Station:A", ToStation: "odpt.Station:B", Direction: "inner", ObservedAt: first}})
	p.observeTransitionsLocked([]Train{{ID: "train-1", FromStation: "odpt.Station:B", ToStation: "odpt.Station:C", Direction: "inner", ObservedAt: first.Add(135 * time.Second)}})
	got := p.SegmentEstimate("odpt.Station:A", "odpt.Station:B", "inner")
	if got.ExpectedDurationSeconds != 135 || got.ObservedMedianSeconds == nil || *got.ObservedMedianSeconds != 135 || got.SampleCount != 1 || got.Method != "observed_median" || got.Confidence != "medium" {
		t.Fatalf("estimate = %+v", got)
	}
}
