# Yamanote Train API and local prediction-market prototype

The Go backend is in [`backend/api`](backend/api). It turns Yamanote `odpt:Train` responses supplied through the ODPT Public Transportation Open Data Center into a snapshot and SSE API. The feed describes train sections (`from_station` → `to_station`), not GPS locations. It also derives display-only section progress and observed section-duration estimates from successive provider observations; neither is a physical location or exact arrival time.

See [backend/api/README.md](backend/api/README.md) for setup, API usage, ODPT links, and licensing caveats.

## Local prediction-market prototype

`contracts/` contains an Anvil-only Foundry workspace with mock six-decimal USDC, quorum-signed train-arrival rounds, time-weighted range tickets, and pari-mutuel claims. `frontend/` includes a range-betting panel that becomes live when its Anvil contract addresses are configured. See [contracts/README.md](contracts/README.md) for local deployment.

The Go `market` package consumes internal `station.confirmed` domain events and matches them against replaceable schedules. It deliberately exposes a narrow chain-submission interface so fixture coordination can be exercised without embedding signer keys in the train API. Production use, real collateral, ODPT timetable licensing, and regulatory review are outside this prototype.
