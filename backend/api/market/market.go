// Package market contains the chain-agnostic coordination rules for the local
// prediction-market prototype. Chain signing/submission is injected so this
// package can be tested against fixtures without an RPC node.
package market

import (
	"context"
	"errors"
	"time"

	"github.com/example/yamanote-api/train"
)

type ScheduledTrain struct {
	TrainID          string
	TargetStation    string
	Direction        string
	ScheduledArrival time.Time
}

// ScheduleProvider is intentionally replaceable. The local app uses fixtures;
// an ODPT adapter may be enabled only after its timetable licence is reviewed.
type ScheduleProvider interface {
	Match(ctx context.Context, trainID, station, direction string, observedAt time.Time) (ScheduledTrain, error)
	Next(ctx context.Context, station, direction string, after time.Time) (ScheduledTrain, error)
}

type FixtureSchedule []ScheduledTrain

func (f FixtureSchedule) Match(_ context.Context, trainID, station, direction string, observedAt time.Time) (ScheduledTrain, error) {
	for _, item := range f {
		if item.TrainID == trainID && item.TargetStation == station && item.Direction == direction && !item.ScheduledArrival.Before(observedAt.Add(-24*time.Hour)) {
			return item, nil
		}
	}
	return ScheduledTrain{}, errors.New("no matching scheduled train")
}

func (f FixtureSchedule) Next(_ context.Context, station, direction string, after time.Time) (ScheduledTrain, error) {
	var found ScheduledTrain
	for _, item := range f {
		if item.TargetStation == station && item.Direction == direction && item.ScheduledArrival.After(after) && (found.ScheduledArrival.IsZero() || item.ScheduledArrival.Before(found.ScheduledArrival)) {
			found = item
		}
	}
	if found.ScheduledArrival.IsZero() {
		return ScheduledTrain{}, errors.New("no next scheduled train")
	}
	return found, nil
}

type ActiveRound struct {
	SeriesID         uint64
	RoundID          uint64
	TrainID          string
	TargetStation    string
	Direction        string
	ScheduledArrival time.Time
}

type SettlementReport struct {
	ActiveRound
	ArrivalWindowEnd time.Time
	Next             ScheduledTrain
}

// Chain is the narrow adapter implemented by the Anvil signer/submission
// process. Submit must be idempotent by report nonce/round on the chain side.
type Chain interface {
	ActiveRound(context.Context) (ActiveRound, error)
	SubmitSettlement(context.Context, SettlementReport) error
}

type Coordinator struct {
	Schedules ScheduleProvider
	Chain     Chain
	Now       func() time.Time
}

func (c Coordinator) HandleConfirmation(ctx context.Context, confirmation train.StationConfirmation) error {
	if c.Schedules == nil || c.Chain == nil {
		return errors.New("market coordinator is not configured")
	}
	active, err := c.Chain.ActiveRound(ctx)
	if err != nil {
		return err
	}
	if active.TrainID != confirmation.TrainID || active.TargetStation != confirmation.Station || active.Direction != confirmation.Direction {
		return nil // confirmation belongs to another train or series
	}
	matched, err := c.Schedules.Match(ctx, confirmation.TrainID, confirmation.Station, confirmation.Direction, confirmation.ArrivalWindowEnd)
	if err != nil || !matched.ScheduledArrival.Equal(active.ScheduledArrival) {
		return errors.New("unmatched or inconsistent schedule")
	}
	next, err := c.Schedules.Next(ctx, active.TargetStation, active.Direction, confirmation.ArrivalWindowEnd)
	if err != nil {
		return err
	}
	return c.Chain.SubmitSettlement(ctx, SettlementReport{ActiveRound: active, ArrivalWindowEnd: confirmation.ArrivalWindowEnd, Next: next})
}

// Run subscribes to the poller in main. Confirmation errors are intentionally
// returned to the caller through its logging/metrics wrapper, while the feed
// keeps running; a production adapter must reconcile active chain state first.
func (c Coordinator) Run(ctx context.Context, confirmations <-chan train.StationConfirmation, reportError func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case confirmation, ok := <-confirmations:
			if !ok {
				return
			}
			if err := c.HandleConfirmation(ctx, confirmation); err != nil && reportError != nil {
				reportError(err)
			}
		}
	}
}
