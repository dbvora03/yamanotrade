export type MarketStatusState = "In progress" | "Bets closed" | "Complete" | "Forfeited";

const statusCopy: Record<MarketStatusState, { detail: string; tone: string }> = {
  "In progress": { detail: "Market is accepting bets.", tone: "active" },
  "Bets closed": { detail: "Market is not accepting bets right now.", tone: "closed" },
  "Complete": { detail: "Market outcome has been resolved.", tone: "complete" },
  "Forfeited": { detail: "Market has been forfeited.", tone: "forfeited" },
};

/**
 * Presentational market state. Resolution data can later supply Complete or
 * Forfeited without changing this component's structure or accessible copy.
 */
export function MarketStatus({ status }: { status: MarketStatusState }) {
  const { detail, tone } = statusCopy[status];

  return (
    <section className={`market-status market-status-${tone}`} aria-label={`Market status: ${status}. ${detail}`} role="status">
      <span className="market-status-dot" aria-hidden="true" />
      <span className="market-status-label" aria-hidden="true">{status}</span>
    </section>
  );
}
