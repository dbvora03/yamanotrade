package market

import (
	"context"
	"testing"
	"time"

	"github.com/example/yamanote-api/train"
)

type fakeChain struct {
	active ActiveRound
	report SettlementReport
}

func (f *fakeChain) ActiveRound(context.Context) (ActiveRound, error) { return f.active, nil }
func (f *fakeChain) SubmitSettlement(_ context.Context, r SettlementReport) error {
	f.report = r
	return nil
}

func TestCoordinatorSubmitsOnlyMatchingConfirmation(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	active := ActiveRound{SeriesID: 1, RoundID: 2, TrainID: "train-a", TargetStation: "Shibuya", Direction: "clockwise", ScheduledArrival: now.Add(time.Minute)}
	chain := &fakeChain{active: active}
	schedules := FixtureSchedule{
		{TrainID: "train-a", TargetStation: "Shibuya", Direction: "clockwise", ScheduledArrival: active.ScheduledArrival},
		{TrainID: "train-b", TargetStation: "Shibuya", Direction: "clockwise", ScheduledArrival: now.Add(5 * time.Minute)},
	}
	c := Coordinator{Schedules: schedules, Chain: chain}
	if err := c.HandleConfirmation(context.Background(), train.StationConfirmation{TrainID: "train-a", Station: "Shibuya", Direction: "clockwise", ArrivalWindowEnd: now.Add(65 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if chain.report.Next.TrainID != "train-b" || !chain.report.ArrivalWindowEnd.Equal(now.Add(65*time.Second)) {
		t.Fatalf("report=%+v", chain.report)
	}
	if err := c.HandleConfirmation(context.Background(), train.StationConfirmation{TrainID: "other", Station: "Shibuya", Direction: "clockwise", ArrivalWindowEnd: now}); err != nil {
		t.Fatal(err)
	}
}
