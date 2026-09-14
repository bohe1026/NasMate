# NasMate backend

Go backend for the UGOS Pro native application prototype. The service follows the UGOS application layout: compile the service into `rootfs_{arch}/bin`, configure `start_cmd` and `port` in the root `project.yaml`, and serve the built frontend from `rootfs_common/www`.

## Local development

```bash
cd backend
UGOS_DEV_MODE=1 go run .
```

The default address is `http://127.0.0.1:21010`. Development mode supplies a local demo user. On UGOS, leave `UGOS_DEV_MODE` unset and let the gateway provide `Ugreen-User-ID`, `Ugreen-User-Name`, and `Ugreen-User-Type` headers.

Persistent task, download, and session state is stored below `UGAPP_DATA_DIR` (`state.json` and `events.jsonl`). `UGAPP_SHARED_DIR` is the only filesystem root used by the read-only storage provider and download-plan path validation; its top-level UGOS authorization symlinks are resolved as approved roots, while nested symlinks are ignored. Application logs remain on standard output/error so UGOS can collect them under `UGAPP_LOG_DIR`.

## API surface

- `GET /api/health`
- `GET|POST /api/tasks`
- `GET /api/tasks/{id}`
- `GET /api/tasks/{id}/events`
- `POST /api/tasks/{id}/cancel`
- `GET /api/storage/usage`
- `GET /api/files/search`
- `GET /api/docker/containers`
- `GET /api/backups/status`
- `GET /api/backups/recovery-plan`
- `GET /api/models/status`
- `GET /api/index/status`
- `POST /api/index/rebuild`
- `GET /api/index/search`
- `GET /api/artifacts`
- `GET /api/artifacts/{name}`
- `POST /api/reports/health/generate`
- `POST /api/network/sources/validate`
- `POST /api/network/sources/probe`
- `POST /api/downloads/prepare`
- `GET /api/downloads`
- `GET /api/downloads/{id}`
- `POST /api/downloads/{id}?action=approve|deny`

Storage uses a read-only filesystem provider scoped to `UGAPP_SHARED_DIR`. Docker and backup implementations remain provider interfaces; production returns an unavailable error until their UGOS adapters are validated. Safe mock data is enabled only when `UGOS_DEV_MODE=1` is explicitly set for local development. The backend does not execute arbitrary shell commands or modify containers.

## Security boundary

- Protected routes require UGOS user headers, except in explicit development mode.
- Download plans require an authorized path under `UGAPP_SHARED_DIR` and always start in `待确认`.
- URL schemes are restricted to HTTP(S), request bodies are capped, and path traversal is rejected.
- API endpoints are rate-limited per authenticated user; expensive search, task, and download-plan routes have stricter limits.
- Health reports and metadata indexes are stored below `UGAPP_DATA_DIR`; report retention is capped at 30 files.
- Docker diagnostics redact common credential fields before returning logs.
- Every task and approval emits an append-only event and persists session state below `UGAPP_DATA_DIR` when the UGOS runtime provides it.
