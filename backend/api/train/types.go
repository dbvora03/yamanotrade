// Package train fetches and normalizes ODPT train section data.
package train

import "time"

// Train is a section-level observation. It deliberately contains no GPS coordinates.
type Train struct {
	ID           string    `json:"id"`
	TrainNumber  string    `json:"train_number"`
	Direction    string    `json:"direction"`
	FromStation  string    `json:"from_station"`
	ToStation    string    `json:"to_station"`
	DelaySeconds int       `json:"delay_seconds"`
	ObservedAt   time.Time `json:"observed_at"`
	PositionKind string    `json:"position_kind"`
}

// Snapshot describes the freshest successful poll and its current health.
type Snapshot struct {
	Trains      []Train    `json:"trains"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	Stale       bool       `json:"stale"`
	AgeSeconds  int64      `json:"age_seconds"`
	LastError   string     `json:"last_error,omitempty"`
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
