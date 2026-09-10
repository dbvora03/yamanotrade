# Frontend placeholder

No frontend application is included yet. A client can consume `GET /api/v1/trains` for a snapshot or `/api/v1/trains/stream` with `EventSource` for updates. Treat each result as a section between `from_station` and `to_station`, never a GPS coordinate.
