# Yamanote frontend

A static-exported Next.js station explorer. It has no API routes, server actions,
database, analytics, music, or third-party client services.

The “Place a guess” card is interaction-only: it uses a declared mock ETH
balance and deterministic placeholder range bars. It never connects a wallet,
sends a transaction, or makes a market/network request.

## Run locally

```sh
cd frontend
npm install
npm run dev
```

`npm run build` creates a fully static export in `out/`.

## Modes

The default is a self-contained demo: controls browse the fixed 30-station
Yamanote loop and the train marker is deliberately animated visual-only motion.

For direct live data, copy `.env.example` to `.env.local` and set:

```sh
NEXT_PUBLIC_TRAIN_LIVE_MODE=true
NEXT_PUBLIC_TRAIN_API_BASE_URL=http://localhost:8080
NEXT_PUBLIC_APP_CONTACT=hello@example.com
```

The browser requests `${NEXT_PUBLIC_TRAIN_API_BASE_URL}/api/v1/trains` once and
subscribes directly to `${NEXT_PUBLIC_TRAIN_API_BASE_URL}/api/v1/trains/stream`
with `EventSource`. It also refreshes `/api/v1/service-status` every 30 seconds.
When that endpoint reports `running: false`, the station rail and progress UI
remain visible and an accessible banner appears above the rail. It only shows a
JST resumption time when the backend confidently reports `scheduled_off_hours`;
unknown or degraded provider state instead says that no scheduled resumption time
is available. The Go API must permit the frontend origin through
`CORS_ALLOWED_ORIGINS` (for example `http://localhost:3000`). No proxy is used.

Live records are displayed strictly as station-to-station sections, never GPS.
The footer supplies ODPT attribution, source generation time, accuracy notice,
and an application contact; set the contact value before publishing a live display.
