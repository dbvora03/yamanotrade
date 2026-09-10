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
	ConsumerKey string
	Endpoint    string
	Interval    time.Duration
	HTTPTimeout time.Duration
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

	mu       sync.RWMutex
	latest   []Train
	updated  time.Time
	lastErr  error
	watchers map[chan Snapshot]struct{}
}

func NewPoller(c Config) (*Poller, error) {
	if c.ConsumerKey == "" {
		return nil, errors.New("ODPT_CONSUMER_KEY is required")
	}
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 10 * time.Second
	}
	return &Poller{
		config: c,
		client: &http.Client{
			Timeout: c.HTTPTimeout,
			// The consumer key is part of the query string. Do not follow a
			// redirect that could forward it to a different origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now, random: rand.Float64, watchers: make(map[chan Snapshot]struct{}),
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
		observed := fetchedAt
		if parsed, err := time.Parse(time.RFC3339, item.Date); err == nil {
			observed = parsed
		}
		trains = append(trains, Train{ID: trainID(item), TrainNumber: item.Number, Direction: item.Direction, FromStation: item.From, ToStation: item.To, DelaySeconds: item.Delay, ObservedAt: observed, PositionKind: "section"})
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
	p.latest, p.updated, p.lastErr = trains, p.now().UTC(), nil
	s := p.snapshotLocked()
	p.publishLocked(s)
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

func (p *Poller) snapshotLocked() Snapshot {
	trains := make([]Train, len(p.latest))
	copy(trains, p.latest)
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
func (p *Poller) Snapshot() Snapshot { p.mu.RLock(); defer p.mu.RUnlock(); return p.snapshotLocked() }

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
