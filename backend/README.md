# NasMate backend

Go backend for the UGOS Pro native application prototype. The service follows the UGOS application layout: compile the service into `rootfs_{arch}/bin`, configure `start_cmd` and `port` in the root `project.yaml`, and serve the built frontend from `rootfs_common/www`.

## Local development

```bash
cd backend
UGOS_DEV_MODE=1 go run .
```

The default address is `http://127.0.0.1:21010`. Development mode supplies a local demo user. On UGOS, leave `UGOS_DEV_MODE` unset and let the gateway provide `Ugreen-User-ID`, `Ugreen-User-Name`, and `Ugreen-User-Type` headers.

Persistent task, download, and session state is stored below `UGAPP_DATA_DIR` (`state.json` and `events.jsonl`). `UGAPP_SHARED_DIR` is the only filesystem root used by the read-only storage provider and download-plan path validation; its top-level UGOS authorization symlinks are resolved as approved roots, while nested symlinks are ignored. Application logs remain on standard output/error so UGOS can collect them under `UGAPP_LOG_DIR`.

`UGAPP_HEALTH_INTERVAL` 可选配置定时健康报告，例如 `24h`；未设置时调度器关闭，最小有效间隔为 1 分钟。健康报告最多保留 30 份，旧报告按时间自动清理。

健康报告只使用上一份有效的本地报告快照计算正增长目录，不读取文件正文；没有基线时不展示增长。交互式报告的失败任务、失败下载数量只统计当前 UGOS 用户。由于结果文件目前保存在应用级数据目录，持久化报告会省略用户级计数；没有用户身份的定时报告也不聚合这些私人状态。

任务创建立即返回 `规划中`，规划与只读工具在受限的后台任务上下文中运行；用户取消会中断模型请求，服务重启不会自动重放遗留的规划或下载。配置 `DEEPSEEK_API_KEY` 后，云端规划器只接收本地分类的工具意图及允许工具，而不接收用户请求原文、NAS 路径或文件内容；模型输入、模型成功结果或脱敏的本地降级原因、最终工具计划分别记入追加式轨迹。包含明显凭据赋值或 Bearer Token 的任务请求在持久化前被拒绝。

同时最多运行 3 个规划或只读任务；达到上限时创建接口返回 `RATE_LIMITED`，不会生成任务或轨迹。取消或执行结束后释放名额。

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
- A natural-language download request without validated sources and an authorized target cannot create a plan or approval. Use `POST /api/downloads/prepare` for a structured plan; only a real user action on its approval endpoint may start a download.
