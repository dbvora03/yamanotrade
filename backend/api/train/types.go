// Package train fetches and normalizes ODPT train section data.
package train

import "time"

// VisualProgress is an inferred, display-only estimate for a train section.
// It is deliberately not a geographic position or GPS measurement.
type VisualProgress struct {
	EstimatedFraction       float64   `json:"estimated_fraction"`
	SegmentEnteredAt        time.Time `json:"segment_entered_at"`
	ExpectedDurationSeconds int64     `json:"expected_duration_seconds"`
	Method                  string    `json:"method"`
	Confidence              string    `json:"confidence"`
}

// Train is a provider observation at a station or on a section. It deliberately
// contains no GPS coordinates.
type Train struct {
	ID           string    `json:"id"`
	TrainNumber  string    `json:"train_number"`
	Direction    string    `json:"direction"`
	FromStation  string    `json:"from_station"`
	ToStation    string    `json:"to_station"`
	DelaySeconds int       `json:"delay_seconds"`
	ObservedAt   time.Time `json:"observed_at"`
	PositionKind string    `json:"position_kind"`
	// Progress is a visual inference from successive provider observations, never GPS.
	Progress *VisualProgress `json:"progress,omitempty"`
}

// Snapshot describes the freshest successful poll and its current health.
type Snapshot struct {
	Trains      []Train    `json:"trains"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	Stale       bool       `json:"stale"`
	AgeSeconds  int64      `json:"age_seconds"`
	LastError   string     `json:"last_error,omitempty"`
}

// SegmentEstimate describes an observed or configured duration for one exact,
// normalized ODPT station-to-station section and rail direction.
type SegmentEstimate struct {
	FromStation             string    `json:"from_station"`
	ToStation               string    `json:"to_station"`
	Direction               string    `json:"direction"`
	ExpectedDurationSeconds int64     `json:"expected_duration_seconds"`
	ObservedMedianSeconds   *int64    `json:"observed_median_seconds,omitempty"`
	SampleCount             int       `json:"sample_count"`
	Method                  string    `json:"method"`
	Confidence              string    `json:"confidence"`
	GeneratedAt             time.Time `json:"generated_at"`
}

// Health is the response returned by /healthz. Its freshness fields mirror
// Snapshot so health and data clients use the same diagnostics.
type Health struct {
	Status      string     `json:"status"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	Stale       bool       `json:"stale"`
	AgeSeconds  int64      `json:"age_seconds"`
	LastError   string     `json:"last_error,omitempty"`
}

// StationConfirmation is the internal domain event for consumers such as a
// market coordinator. It deliberately preserves the provider-observation
// window rather than inventing an exact arrival timestamp.
type StationConfirmation struct {
	TrainID            string    `json:"train_id"`
	Station            string    `json:"station"`
	Direction          string    `json:"direction"`
	ArrivalWindowStart time.Time `json:"arrival_window_start"`
	ArrivalWindowEnd   time.Time `json:"arrival_window_end"`
	ConfirmedAt        time.Time `json:"confirmed_at"`
	Confidence         string    `json:"confidence"`
}

// ServiceStatus is a conservative answer to whether the Yamanote Line is
// running. running is false for scheduled_off_hours, unknown, and degraded;
// clients must use Status and Reason to distinguish those cases. In
// particular, unknown or degraded never confirms a service disruption.
//
// ObservedAt is the newest ODPT train observation used to establish active
// service. ResumesAt is present only for the conservative scheduled_off_hours
// heuristic; it is an RFC3339 JST heuristic, not a line-wide timetable promise.
// GeneratedAt is when this response was evaluated by this API.
type ServiceStatus struct {
	Running     bool       `json:"running"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	ObservedAt  *time.Time `json:"observed_at,omitempty"`
	ResumesAt   *time.Time `json:"resumes_at,omitempty"`
	GeneratedAt time.Time  `json:"generated_at"`
	Source      string     `json:"source"`
	Confidence  string     `json:"confidence"`
}
