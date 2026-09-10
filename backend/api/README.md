# Yamanote train-section API

This Go service polls the **ODPT v4 `odpt:Train`** endpoint, filtered to the JR East Yamanote Line, and publishes its latest successful observation as JSON and Server-Sent Events (SSE). ODPT is the Public Transportation Open Data Center; do not describe this API as a direct JR East service.

## Important: this is section-level data, not GPS

ODPT's train feed reports the train's `fromStation` and `toStation`. It does **not** provide latitude/longitude or a precise point on a map. Every normalized record sets `position_kind` to `"section"`; clients must present it as a train being on/between that railway section, never as a live GPS location.

The feed and access are subject to ODPT's terms and the data provider's applicable license. Before redistribution, check the [ODPT terms](https://developer.odpt.org/terms/center_use_rules.html), the data catalog's provider-specific terms, and the [developer guideline](https://developer.odpt.org/terms/data_basic_use_guideline.html). The guideline requires public dynamic-data displays to show the source generation time, identify ODPT as the source, state that accuracy/integrity are not guaranteed, and provide the application's contact rather than directing users to a transport operator. Source endpoint: [ODPT v4 `odpt:Train`](https://api.odpt.org/api/v4/odpt:Train).

## Run

1. From this directory:

```sh
export CORS_ALLOWED_ORIGINS='http://localhost:5173'
go run .
```

The service includes a committed public ODPT consumer key, so no key is required for a standard run. To use your own key, obtain one from [developer.odpt.org](https://developer.odpt.org/) and set `ODPT_CONSUMER_KEY` before starting the service.

The server listens on `:8080` by default. Try it:

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/trains
curl -N http://localhost:8080/api/v1/trains/stream
```

The SSE endpoint atomically subscribes a client before it sends the initial `event: snapshot`, so a poll cannot be missed in that handoff. It sends updates after both successful and failed polls, sends a snapshot if heartbeat processing observes a staleness transition, and emits a `: heartbeat` comment every 15 seconds. Multiple clients are supported; closing the connection releases its subscription.

## Environment

`ODPT_CONSUMER_KEY` is optional and overrides the committed public default when non-empty. `ODPT_POLL_INTERVAL` defaults to `30s`, `ODPT_HTTP_TIMEOUT` to `10s`, `LISTEN_ADDR` to `:8080`, and `CORS_ALLOWED_ORIGINS` to `*`. The latter accepts a comma-separated origin list for development. See [`../../.env.example`](../../.env.example).

Failed polls retain the last successful snapshot and surface `stale`, `age_seconds`, and `last_error`; diagnostic errors are intentionally sanitized and never contain the consumer key. Retries use capped exponential backoff with small jitter. The process handles SIGINT/SIGTERM and shuts HTTP down gracefully.

## API shape

`GET /api/v1/trains` returns a snapshot with `trains`, `generated_at`, `stale`, `age_seconds`, and optional `last_error`. `generated_at` is the API's successful-poll time; each train's `observed_at` is ODPT's `dc:date` when present (otherwise the poll time). A train always includes `id`, `train_number`, `direction`, `from_station`, `to_station`, `delay_seconds`, `observed_at`, and `position_kind` (`"section"`). `id` prefers ODPT's `owl:sameAs`, then `@id`, with a deterministic fallback for incomplete upstream records. A service with no successful data responds `503` but still returns the diagnostic snapshot. `/healthz` uses matching freshness fields plus `status` (`ok`, `stale`, or `unavailable`). All three endpoints accept `GET`; unsupported methods return JSON `405` with `Allow: GET, OPTIONS`.
