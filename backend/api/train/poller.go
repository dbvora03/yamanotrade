package train

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const DefaultEndpoint = "https://api.odpt.org/api/v4/odpt:Train"

type Config struct {
	ConsumerKey             string
	Endpoint                string
	Interval                time.Duration
	HTTPTimeout             time.Duration
	FallbackSegmentDuration time.Duration
	SegmentHistorySize      int
}

type odptTrain struct {
	ID        string `json:"@id"`
	SameAs    string `json:"owl:sameAs"`
	Number    string `json:"odpt:trainNumber"`
	Direction string `json:"odpt:railDirection"`
	From      string `json:"odpt:fromStation"`
	To        string `json:"odpt:toStation"`
	Delay     int    `json:"odpt:delay"`
	Date      string `json:"dc:date"`
}

// Poller owns one fetch loop and keeps the last valid result when a request fails.
type Poller struct {
	config Config
	client *http.Client
	now    func() time.Time
	random func() float64

	mu              sync.RWMutex
	latest          []Train
	updated         time.Time
	lastErr         error
	watchers        map[chan Snapshot]struct{}
	stationWatchers map[chan StationConfirmation]struct{}

	// These are inference state from the provider observation timeline, not
	// physical arrival data or coordinates. They are protected by mu.
	estimator      *segmentEstimator
	previous       map[string]trainObservation
	segmentEntered map[string]trainObservation
}

type trainObservation struct {
	segment    segmentKey
	observedAt time.Time
	enteredAt  time.Time
}

func NewPoller(c Config) (*Poller, error) {
	if c.ConsumerKey == "" {
		return nil, errors.New("ODPT_CONSUMER_KEY is required")
	}
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 10 * time.Second
	}
	if c.FallbackSegmentDuration <= 0 {
		c.FallbackSegmentDuration = 150 * time.Second
	}
	if c.SegmentHistorySize <= 0 {
		c.SegmentHistorySize = 32
	}
	return &Poller{
		config: c,
		client: &http.Client{
			Timeout: c.HTTPTimeout,
			// The consumer key is part of the query string. Do not follow a
			// redirect that could forward it to a different origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now, random: rand.Float64, watchers: make(map[chan Snapshot]struct{}), stationWatchers: make(map[chan StationConfirmation]struct{}),
		estimator: newSegmentEstimator(c.FallbackSegmentDuration, c.SegmentHistorySize),
		previous:  make(map[string]trainObservation), segmentEntered: make(map[string]trainObservation),
	}, nil
}

func (p *Poller) fetch(ctx context.Context) ([]Train, error) {
	u, err := url.Parse(p.config.Endpoint)
	if err != nil {
		return nil, errors.New("invalid ODPT endpoint")
	}
	q := u.Query()
	q.Set("odpt:operator", "odpt.Operator:JR-East")
	q.Set("odpt:railway", "odpt.Railway:JR-East.Yamanote")
	q.Set("acl:consumerKey", p.config.ConsumerKey)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("create ODPT request failed")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// URL errors commonly include the complete request URL. That URL contains
		// acl:consumerKey, so never retain it in an error that can reach a client.
		return nil, errors.New("ODPT request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Do not include an upstream response body: proxies sometimes echo query
		// parameters, including the consumer key, in diagnostic bodies.
		return nil, fmt.Errorf("ODPT returned HTTP %d", resp.StatusCode)
	}
	var source []odptTrain
	if err := json.NewDecoder(resp.Body).Decode(&source); err != nil {
		return nil, errors.New("decode ODPT response failed")
	}
	fetchedAt := p.now().UTC()
	trains := make([]Train, 0, len(source))
	for _, item := range source {
		fromStation := strings.TrimSpace(item.From)
		toStation := strings.TrimSpace(item.To)
		if fromStation == "" {
			continue
		}
		positionKind := "section"
		// JR East reports a train stopped at a station with a null toStation.
		// Keep that observation so clients do not lose the selected train while
		// waiting for its next station-to-station section.
		if toStation == "" {
			positionKind = "station"
		}
		observed := fetchedAt
		if parsed, err := time.Parse(time.RFC3339, item.Date); err == nil {
			observed = parsed
		}
		trains = append(trains, Train{
			ID: trainID(item), TrainNumber: strings.TrimSpace(item.Number), Direction: strings.TrimSpace(item.Direction),
			FromStation: fromStation, ToStation: toStation, DelaySeconds: item.Delay,
			ObservedAt: observed, PositionKind: positionKind,
		})
	}
	return trains, nil
}

// trainID prefers ODPT's canonical identity. @id is an RDF resource identifier,
// while owl:sameAs is the stable ODPT train identifier exposed by v4 responses.
// A deterministic fallback keeps the public schema usable for incomplete records.
func trainID(item odptTrain) string {
	if id := strings.TrimSpace(item.SameAs); id != "" {
		return id
	}
	if id := strings.TrimSpace(item.ID); id != "" {
		return id
	}
	identity := strings.Join([]string{
		strings.TrimSpace(item.Number),
		strings.TrimSpace(item.Direction),
		strings.TrimSpace(item.From),
		strings.TrimSpace(item.To),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("odpt:Train:fallback:%x", sum[:16])
}

// Poll performs one request. A failure leaves the prior successful result intact.
func (p *Poller) Poll(ctx context.Context) error {
	trains, err := p.fetch(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.lastErr = err
		s := p.snapshotLocked()
		p.publishLocked(s)
		return err
	}
	confirmations := p.observeTransitionsLocked(trains)
	p.latest, p.updated, p.lastErr = trains, p.now().UTC(), nil
	s := p.snapshotLocked()
	p.publishLocked(s)
	for _, confirmation := range confirmations {
		p.publishStationConfirmationLocked(confirmation)
	}
	return nil
}

func (p *Poller) publishLocked(s Snapshot) {
	for ch := range p.watchers {
		select {
		case ch <- s:
		default:
			// Keep only the newest snapshot for a slow SSE writer. Receiving from a
			// channel is safe while holding p.mu because cancellation also takes it.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- s:
			default:
			}
		}
	}
}

func (p *Poller) publishStationConfirmationLocked(confirmation StationConfirmation) {
	for ch := range p.stationWatchers {
		select {
		case ch <- confirmation:
		default:
			// Retain only the newest event for a stalled consumer. Coordinators must
			// reconcile their active round from chain state after a restart.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- confirmation:
			default:
			}
		}
	}
}

func (p *Poller) snapshotLocked() Snapshot {
	trains := make([]Train, len(p.latest))
	copy(trains, p.latest)
	for i := range trains {
		trains[i].Progress = p.progressLocked(trains[i])
	}
	s := Snapshot{Trains: trains, Stale: p.updated.IsZero()}
	if !p.updated.IsZero() {
		updated := p.updated
		s.GeneratedAt = &updated
		age := p.now().Sub(p.updated)
		if age > 0 {
			s.AgeSeconds = int64(age.Seconds())
		}
		s.Stale = age > 2*p.config.Interval
	}
	if p.lastErr != nil {
		s.LastError = p.lastErr.Error()
		s.Stale = true
	}
	return s
}

// observeTransitionsLocked records the elapsed provider-observation time for a
// section only when the same train is next observed on its adjoining section.
func (p *Poller) observeTransitionsLocked(trains []Train) []StationConfirmation {
	confirmations := make([]StationConfirmation, 0)
	next := make(map[string]trainObservation, len(trains))
	nextEntered := make(map[string]trainObservation, len(trains))
	for _, train := range trains {
		key := newSegmentKey(train.FromStation, train.ToStation, train.Direction)
		current := trainObservation{segment: key, observedAt: train.ObservedAt, enteredAt: train.ObservedAt}
		previous, hadPrevious := p.previous[train.ID]
		if hadPrevious && previous.segment == key {
			current.enteredAt = previous.enteredAt
		} else if hadPrevious && previous.segment.to == key.from && previous.segment.direction == key.direction {
			// This is an observed section transition. Its duration is an inference
			// bounded by provider observations, not a physical station-arrival time.
			p.estimator.record(previous.segment, current.observedAt.Sub(previous.observedAt))
			start, end := previous.observedAt, current.observedAt
			if end.Before(start) {
				start, end = end, start
			}
			confirmations = append(confirmations, StationConfirmation{
				TrainID: train.ID, Station: previous.segment.to, Direction: key.direction,
				ArrivalWindowStart: start, ArrivalWindowEnd: end, ConfirmedAt: train.ObservedAt,
				Confidence: "provider_confirmed_transition",
			})
		}
		next[train.ID] = current
		nextEntered[train.ID] = current
	}
	// Dropping absent trains prevents a later reappearance from being treated as
	// a consecutive observation.
	p.previous, p.segmentEntered = next, nextEntered
	return confirmations
}

func (p *Poller) progressLocked(train Train) *VisualProgress {
	if train.PositionKind != "section" {
		return nil
	}
	entered, ok := p.segmentEntered[train.ID]
	if !ok || entered.segment != newSegmentKey(train.FromStation, train.ToStation, train.Direction) || entered.enteredAt.IsZero() {
		return nil
	}
	duration, _, _, method, confidence := p.estimator.estimate(entered.segment)
	seconds := int64(duration.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	fraction := p.now().UTC().Sub(entered.enteredAt).Seconds() / float64(seconds)
	if fraction < 0 {
		fraction = 0
	}
	// A train stays visually within its current section until a provider section
	// transition is observed, so it never appears to have arrived early.
	if fraction >= 1 {
		fraction = 0.99
	}
	return &VisualProgress{EstimatedFraction: fraction, SegmentEnteredAt: entered.enteredAt, ExpectedDurationSeconds: seconds, Method: method, Confidence: confidence}
}

// SegmentEstimate returns a generated display estimate for an exact section.
func (p *Poller) SegmentEstimate(from, to, direction string) SegmentEstimate {
	p.mu.RLock()
	defer p.mu.RUnlock()
	key := newSegmentKey(from, to, direction)
	duration, median, samples, method, confidence := p.estimator.estimate(key)
	expected := int64(duration.Round(time.Second) / time.Second)
	if expected < 1 {
		expected = 1
	}
	result := SegmentEstimate{FromStation: key.from, ToStation: key.to, Direction: key.direction, ExpectedDurationSeconds: expected, SampleCount: samples, Method: method, Confidence: confidence, GeneratedAt: p.now().UTC()}
	if median != nil {
		seconds := int64(median.Round(time.Second) / time.Second)
		result.ObservedMedianSeconds = &seconds
	}
	return result
}
func (p *Poller) Snapshot() Snapshot { p.mu.RLock(); defer p.mu.RUnlock(); return p.snapshotLocked() }

// ServiceStatus deliberately uses only a small, every-day JST off-hours window
// (02:00 through before 04:00). The 04:30 JST resumption estimate is a cautious
// heuristic: JR East's public Yamanote timetable includes early departures at
// about that time, but weekday, weekend, holiday, and special timetables vary.
// A fresh live train observation wins even inside the window (for example, an
// exceptional service). Empty, failed, or stale data is never called a
// disruption and never receives a resumption time.
func (p *Poller) ServiceStatus() ServiceStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := p.now().UTC()
	result := ServiceStatus{GeneratedAt: now, Source: "odpt:Train"}
	snap := p.snapshotLocked()
	if !snap.Stale && len(snap.Trains) > 0 {
		latest := snap.Trains[0].ObservedAt
		for _, train := range snap.Trains[1:] {
			if train.ObservedAt.After(latest) {
				latest = train.ObservedAt
			}
		}
		result.Running = true
		result.Status = "active"
		result.Reason = "live_train_observations"
		result.ObservedAt = &latest
		result.Confidence = "high"
		return result
	}

	if inConservativeScheduledOffHours(now) {
		resumesAt := conservativeResumesAt(now)
		result.Status = "scheduled_off_hours"
		result.Reason = "conservative_jst_schedule_window"
		result.Source = "jst_schedule_heuristic"
		result.Confidence = "medium"
		result.ResumesAt = &resumesAt
		return result
	}
	if snap.GeneratedAt == nil || snap.Stale || snap.LastError != "" {
		result.Status = "degraded"
		result.Reason = "live_feed_unavailable_or_stale"
		result.Confidence = "none"
		return result
	}
	result.Status = "unknown"
	result.Reason = "no_live_train_observations"
	result.Confidence = "low"
	return result
}

func inConservativeScheduledOffHours(now time.Time) bool {
	hour := now.In(jstLocation).Hour()
	return hour >= 2 && hour < 4
}

var jstLocation = time.FixedZone("JST", 9*60*60)

// conservativeResumesAt intentionally does not model weekday, holiday, or
// disruption exceptions. It is only emitted after inConservativeScheduledOffHours.
func conservativeResumesAt(now time.Time) time.Time {
	jst := now.In(jstLocation)
	return time.Date(jst.Year(), jst.Month(), jst.Day(), 4, 30, 0, 0, jstLocation)
}

// Subscribe atomically installs a subscriber and returns the corresponding initial
// snapshot. Sending that snapshot after subscribing cannot lose a concurrent poll.
// Call cancel to release the client channel.
func (p *Poller) Subscribe() (Snapshot, <-chan Snapshot, func()) {
	ch := make(chan Snapshot, 1)
	p.mu.Lock()
	snapshot := p.snapshotLocked()
	p.watchers[ch] = struct{}{}
	p.mu.Unlock()
	return snapshot, ch, func() {
		p.mu.Lock()
		if _, ok := p.watchers[ch]; ok {
			delete(p.watchers, ch)
			close(ch)
		}
		p.mu.Unlock()
	}
}

// SubscribeStationConfirmations is an internal subscription API. HTTP clients
// should continue using the SSE stream, which remains backward-compatible.
func (p *Poller) SubscribeStationConfirmations() (<-chan StationConfirmation, func()) {
	p.mu.Lock()
	ch := make(chan StationConfirmation, 1)
	p.stationWatchers[ch] = struct{}{}
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		if _, ok := p.stationWatchers[ch]; ok {
			delete(p.stationWatchers, ch)
			close(ch)
		}
		p.mu.Unlock()
	}
}

func (p *Poller) delay(failures int) time.Duration {
	const maxDelay = 5 * time.Minute
	d := p.config.Interval
	if d > maxDelay {
		d = maxDelay
	}
	for i := 0; i < failures && d < maxDelay; i++ {
		if d > maxDelay/2 {
			d = maxDelay
		} else {
			d *= 2
		}
	}
	// d is capped before jitter, so this multiplication cannot overflow.
	return d * time.Duration(90+p.random()*20) / 100
}

// Run polls immediately, then retries failures with capped exponential backoff and small jitter.
func (p *Poller) Run(ctx context.Context) {
	failures := 0
	for {
		if err := p.Poll(ctx); err != nil {
			if failures < int(^uint(0)>>1) {
				failures++
			}
		} else {
			failures = 0
		}
		timer := time.NewTimer(p.delay(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
