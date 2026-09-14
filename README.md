# NasMate

UGOS Pro 原生应用：React 前端 + Go 后端。当前后端提供安全边界内的任务、真实文件元数据搜索、真实存储统计、Docker 只读诊断、备份状态和下载审批 API；Docker 与备份能力仍通过 Provider 接口隔离，等待接入 UGOS 实例。

## 本地开发

终端一启动 Go 服务：

```bash
cd backend
UGOS_DEV_MODE=1 go run .
```

终端二启动 Vite 前端：

```bash
npm install
npm run dev -- --host 127.0.0.1
```

访问 <http://127.0.0.1:5173/>。Vite 会把 `/api` 请求代理到 `http://127.0.0.1:21010`。

开发模式会使用本地演示用户。运行在 UGOS Pro 时不要设置 `UGOS_DEV_MODE`，由系统网关提供以下请求头：

- `Ugreen-User-ID`
- `Ugreen-User-Name`
- `Ugreen-User-Type`

前端始终执行 `UGOSCore.init()`；在 UGOS 窗口中通过 `cloudWindow.useCapacity('getThirdToken')` 获取 `third_token`，并将其作为 `Ugreen-Ttk` 请求头发送。Vite 开发服务器没有 UGOS 宿主时不会伪造 token，只有显式设置 `UGOS_DEV_MODE=1` 的后端才接受本地开发用户。

UGOS 运行时目录由系统注入：`UGAPP_SHARED_DIR` 用于用户主动授权的文件夹，任务和追加式会话状态写入 `UGAPP_DATA_DIR`。Go 标准输出和标准错误交给 UGOS 按官方规则收集到 `UGAPP_LOG_DIR`；应用不写入安装目录或任意系统目录。

## 检查与构建

```bash
npm run lint
npm run build
cd backend && go test ./...
```

`npm run build` 使用官方 `@ugreen-nas/builder-open` 生成 `version.json`，正式构建需要在有 Git `HEAD` 的 Debian 12/Linux 环境中执行。当前 macOS 工作树可运行 `npm run dev`，但不能替代官方打包验证。

按 UGOS 原生应用结构构建两个架构的后端和前端静态文件：

```bash
make build
```

构建结果：

```text
rootfs_common/www/                 # 前端静态文件
rootfs_amd64/bin/ugreen-ai-backend # Linux amd64
rootfs_arm64/bin/ugreen-ai-backend # Linux arm64
```

根目录 `project.yaml` 已配置 `start_cmd: bin/ugreen-ai-backend`、`port: 21010`、`proxy_path: api` 和 `open_type: inner`。正式 `upk` 打包应在 Debian 12/Linux 上使用 `ugcli check` 和 `ugcli pack`，以保留可执行文件权限并符合 UGOS 校验流程。开发者授权 NAS、`ugcli` 和隐私/源码链接仍是上架前置条件。

## 后端 API

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

下载计划只允许写入 `UGAPP_SHARED_DIR` 下的授权路径，并且始终从 `待确认` 状态开始。文件搜索和存储统计只读取授权目录；生产模式下 Docker 和备份能力在适配器接入前明确返回不可用，只有显式 `UGOS_DEV_MODE=1` 才启用本地测试数据。不执行任意 Shell，不修改容器。
