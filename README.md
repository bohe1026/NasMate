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

根目录 `project.yaml` 已配置 `start_cmd: bin/ugreen-ai-backend`、`port: 21010`、`proxy_path: api` 和 `open_type: inner`。正式 `upk` 打包应在 Debian 12/Linux 上使用 `ugcli check` 和 `ugcli pack`，以保留可执行文件权限并符合 UGOS 校验流程。当前已使用官方 `ugcli v1.1.0.25` 在 Debian 12 容器中完成检查和打包验证：

```bash
# 在项目根目录执行
ugcli check
ugcli pack --build 2 --arch all --product-series nasync
```

输出位于 `build_dir/pkgs/upk/`，分别对应 `amd64_nasync_*.upk` 和 `arm64_nasync_*.upk`。构建号必须在同一 `x.y.z` 版本内递增，不能重复；正式包应在 Linux 环境中生成。

## UGOS 开发授权与设备安装

绿联开发者授权文件不能放入仓库，也不是本地打包密钥。按照官方“开发准备”文档操作：

1. 将官方发来的授权文件重命名为 `ugdev.sig`。
2. 把 `ugdev.sig` 上传到目标 UGOS NAS 的管理员用户个人文件夹。
3. 在该设备的应用中心选择“手动安装”，上传与产品线匹配的 `.upk`：nasync NAS 使用 `nasync` 包，不能安装 HomeAgent 包。
4. 安装后从应用中心或桌面打开 NasMate，检查应用启动、端口探测、UGOS 登录请求头和授权目录访问。

本地只负责生成和校验 `.upk`；是否能在设备上安装、启动和访问真实 Docker/备份能力，必须以目标 NAS 的 UGOS 版本和官方授权结果为准。

## 后端 API

- `GET /api/health`
- `GET|POST /api/tasks`
- `GET /api/tasks/{id}`
- `POST /api/tasks/{id}/resume`
- `GET /api/tasks/{id}/events?limit=1..200&before=<eventId>`
- `POST /api/tasks/{id}/cancel`
- `GET /api/storage/usage`
- `GET /api/files/search`
- `GET /api/docker/containers`
- `GET /api/backups/status`
- `GET /api/backups/recovery-plan`
- `GET /api/models/status`
- `GET /api/index/status`
- `POST /api/index/rebuild`
- `GET /api/index/rebuild/{id}`
- `POST /api/index/rebuild/{id}/cancel`
- `GET /api/index/search`
- `GET /api/artifacts`
- `GET /api/artifacts/{name}`
- `POST /api/reports/health/generate`
- `POST /api/network/sources/validate`
- `POST /api/network/sources/search`
- `POST /api/network/sources/probe`
- `POST /api/downloads/prepare`
- `GET /api/downloads`
- `GET /api/downloads/{id}`
- `POST /api/downloads/{id}?action=approve|deny|cancel`

下载计划只允许写入 `UGAPP_SHARED_DIR` 下的授权路径，并且始终从 `待确认` 状态开始。文件搜索和存储统计只读取授权目录；生产模式下 Docker 和备份能力在适配器接入前明确返回不可用，只有显式 `UGOS_DEV_MODE=1` 才启用本地测试数据。不执行任意 Shell，不修改容器。
