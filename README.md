# Yamanote Train API

The Go backend is in [`backend/api`](backend/api). It turns Yamanote `odpt:Train` responses supplied through the ODPT Public Transportation Open Data Center into a snapshot and SSE API. The feed describes train sections (`from_station` → `to_station`), not GPS locations.

See [backend/api/README.md](backend/api/README.md) for setup, API usage, ODPT links, and licensing caveats. `frontend/` is intentionally a tracked placeholder for a future client.
