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

The service includes a committed public ODPT consumer key, so no key is required for datasets available through the standard endpoint. JR East's Challenge 2026 feed requires a Challenge access token and endpoint; keep the token out of source control and run with:

```sh
export ODPT_ENDPOINT='https://api-challenge.odpt.org/api/v4/odpt:Train'
export ODPT_CONSUMER_KEY='<Challenge 2026 access token>'
export CORS_ALLOWED_ORIGINS='http://localhost:3000'
go run .
```

The server listens on `:8080` by default. Try it:

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/trains
curl 'http://localhost:8080/api/v1/segment-estimates?from_station=odpt.Station:JR-East.Yamanote.Tokyo&to_station=odpt.Station:JR-East.Yamanote.Kanda&direction=odpt.RailDirection:JR-East.Yamanote.Inner'
curl -N http://localhost:8080/api/v1/trains/stream
```

The SSE endpoint atomically subscribes a client before it sends the initial `event: snapshot`, so a poll cannot be missed in that handoff. It sends updates after both successful and failed polls, sends a snapshot if heartbeat processing observes a staleness transition, and emits a `: heartbeat` comment every 15 seconds. Multiple clients are supported; closing the connection releases its subscription.

## Environment

`ODPT_CONSUMER_KEY` is optional and overrides the committed public default when non-empty. `ODPT_ENDPOINT` defaults to `https://api.odpt.org/api/v4/odpt:Train` and can select an authorized compatible ODPT endpoint. `ODPT_POLL_INTERVAL` defaults to `30s`, `ODPT_HTTP_TIMEOUT` to `10s`, `SEGMENT_FALLBACK_DURATION` to `150s`, `SEGMENT_HISTORY_SIZE` to `32`, `LISTEN_ADDR` to `:8080`, and `CORS_ALLOWED_ORIGINS` to `*`. The latter accepts a comma-separated origin list for development. See [`../../.env.example`](../../.env.example). The fallback duration controls only visual progress before provider-observed section transitions have accumulated; history size bounds retained inferred durations per directed section.

Failed polls retain the last successful snapshot and surface `stale`, `age_seconds`, and `last_error`; diagnostic errors are intentionally sanitized and never contain the consumer key. Retries use capped exponential backoff with small jitter. The process handles SIGINT/SIGTERM and shuts HTTP down gracefully.

## API shape

`GET /api/v1/trains` returns a snapshot with `trains`, `generated_at`, `stale`, `age_seconds`, and optional `last_error`. `generated_at` is the API's successful-poll time; each train's `observed_at` is ODPT's `dc:date` when present (otherwise the poll time). A train always includes `id`, `train_number`, `direction`, `from_station`, `to_station`, `delay_seconds`, `observed_at`, and `position_kind` (`"section"`). Provider records without both endpoints (for example, a train currently reported at a station with `toStation: null`) are omitted until they describe a complete section. `id` prefers ODPT's `owl:sameAs`, then `@id`, with a deterministic fallback for incomplete upstream records. A service with no successful data responds `503` but still returns the diagnostic snapshot. `/healthz` uses matching freshness fields plus `status` (`ok`, `stale`, or `unavailable`). All endpoints accept `GET`; unsupported methods return JSON `405` with `Allow: GET, OPTIONS`.

`GET /api/v1/service-status` always returns `200` and this contract: `running` (boolean), `status`, `reason`, `observed_at` (omitted unless live observations establish activity), optional `resumes_at` (RFC3339), `generated_at`, `source`, and `confidence`. `status: "active"` / `running: true` means a fresh non-empty ODPT `odpt:Train` snapshot was observed (`reason: "live_train_observations"`, high confidence). `scheduled_off_hours` / `running: false` is only the conservative daily 02:00–03:59 JST window (`jst_schedule_heuristic`, medium confidence). Only this status includes `resumes_at`, currently a 04:30 JST heuristic. It is based on the early service shown in [JR East's public Yamanote timetable](https://timetables.jreast.co.jp/2609/timetable-v/630u2p.html), but it is not an exact operating promise: weekday, Saturday/holiday, and special timetables vary. `unknown` means a fresh empty snapshot, and `degraded` means no usable/fresh snapshot or a poll error; both set `running: false` for a safe inactive display but **do not confirm a suspension or disruption**, and intentionally omit `resumes_at`. Their reasons make that distinction explicit. The response contains no ODPT consumer key or upstream URL.

Each current train may also contain `progress`: `estimated_fraction` (always clamped below `1` until ODPT reports a new section), `segment_entered_at`, `expected_duration_seconds`, `method`, and `confidence`. It is visual-only inference based on the provider observation timeline. It never represents a GPS measurement, a measured point between stations, or a confirmed physical arrival.

`GET /api/v1/segment-estimates` requires exactly one untrimmed canonical ODPT identifier for each of `from_station` and `to_station` (for example, `odpt.Station:JR-East.Yamanote.Tokyo`), plus one exact `direction`. It returns the exact key, `expected_duration_seconds`, optional `observed_median_seconds`, `sample_count`, `method`, `confidence`, and `generated_at`. With no observations it returns the configured duration with `method: "fallback_config"` and `confidence: "low"`; otherwise it returns the bounded rolling observed median with `method: "observed_median"`.

The SSE stream continues to send `snapshot` events and additionally sends `train.section_changed` only when a train's reported section changes. A contiguous A→B then B→C observation also sends `station.confirmed` for B, containing `arrival_window_start` and `arrival_window_end`: this is a bounded inference between successive provider observations, not an exact physical arrival timestamp. Its confidence is `provider_confirmed_transition`. A separate market service consumes `station.confirmed` and owns all market-resolution and settlement rules; this API intentionally contains none.
