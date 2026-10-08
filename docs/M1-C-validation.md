# 历史录像回放验证

M1-C 在 M1-B 已交付的录像索引之上提供历史回放：按通道与时间检索、单段受授权字节区间播放、跨分片连续播放、跳转与倍速，以及从实时预览进入的即时回放。浏览器只访问同源授权 API（`GET /api/v1/recordings`、`GET /api/v1/recordings/{id}/content`）；服务端按 segment id 解析存储池与冻结路径，校验发布身份后才交付字节。

读路径的对外行为：

- 只认 `auth.Playback`；未授权、通道不在授权范围、segment 不存在一律 `404 not_found`，响应体与耗时不可区分，也不写媒体审计。
- `state <> ready` 回 `409 recording_not_ready`；目标文件被移走回 `409 recording_missing`；文件存在但字节身份与发布记录不一致回 `409 recording_damaged`；存储池不可用回 `503 pool_unavailable`。任何非 200/206 响应都不含媒体字节。
- Range 只服务单区间：`bytes=0-`、`bytes=a-b` 回 206；起点越界与多区间回 416 并带 `Content-Range: bytes */<size>`；畸形 Range 按 HTTP 要求忽略并回 200 全量；`If-Range` 用强 `ETag`，不匹配时回全量。
- 每次成功媒体响应前先落一条 `recording.streamed` 审计（操作者、segment、通道、字节窗口）；审计不可持久化则回 503 且零媒体字节。
- 响应头、响应体、日志与审计都不含池根、冻结相对路径或上游标识。

部署沿用根目录 `./deploy.sh`，没有新增容器、`.env` 参数或数据库迁移（`migrations/` 相对 M1-B 无差异）。前端新增 `/recordings` 页面，侧边栏新增「录像回放」，可见性对所有登录角色开放、由 API 权限过滤。

## 本机验证（2026-10-07 / 08）

- `go test -race ./internal/... ./cmd/... ./migrations` 全部通过。
- 完整 PostgreSQL 集成套件通过（本机 19 分 9 秒），含新增 5 项针对读路径的断言：未授权与不存在不可区分、`state`/文件/身份三类 409、13 组 Range 边界、审计失败即 503 且零字节、并发读与并发元数据查询。
- 前端 27 个测试文件 147 项浏览器测试、类型检查、lint 与生产构建通过；其中时区换算覆盖 UTC / Asia-Shanghai / 纽约夏令时两侧、不可能日期，以及「今天/昨天」按站点整天解析。

## 容器验收

验收在隔离 Compose 项目的固定 digest ZLM、合成 H.264 摄像头与 Chromium 上执行，不使用用户 NAS 数据，也不把 mock UI 当作端到端证据。回执写入 `media-browser-contract` artifact 的 `media-browser.json`。

| 场景 | 断言 | 覆盖方式 |
|---|---|---|
| 授权单区间 | `Range: bytes=0-` 回 206，`Content-Range: bytes 0-<n-1>/<n>`，`Accept-Ranges: bytes`，`Content-Type: video/mp4` | 真实片段经真实 Nginx 入口 fetch |
| 区间窗口正确 | `bytes=a-b` 回 206、长度等于窗口，且该窗口字节摘要与整段同一窗口的摘要一致 | 同上；页内 FNV-1a 摘要比对，不把媒体搬过调试通道 |
| 越界 | 起点等于长度回 416，`Content-Range: bytes */<n>` | 同上 |
| 浏览器真实解码 | 播放页开始播放后实际解码帧 > 2，且 `videoWidth` / `videoHeight` / `duration` 均大于 0 | 真实 ZLM 产出的 MP4 + Chromium |
| 只走授权端点 | 播放期间所有 `/content` 响应均为 200 或 206，没有 blob 或私有路径 | 页面响应监听 |
| 未授权等于不存在 | 仅对 CH02 持有回放权的 viewer 读取 CH01 片段与一个不存在的 UUID，均回 404、`error.code` 为 `not_found`，且响应体不含 `ftyp` | 独立浏览器上下文 + 真实登录 |
| 工作区反映授权 | 该 viewer 的 `/recordings` 只列出 CH02，不出现 CH01 | 真实 UI |
| 网关边界 | 私有媒体路径不可经公共路由访问 | 沿用 M1-B 既有断言 |

### 已通过：读路径与工作区本体（提交 `f86cf7e`）

[CI 37655395677](https://github.com/zhigu34/one-nvr/actions/runs/37655395677) 的 11 个 job 全部成功：
`media-contract`、`gateway-runtime`、`backend`、`frontend`、`joint-media-runtime`、`media-browser-runtime`、`browser-runtime`、`production-runtime`、`hardware-cpu-runtime`，以及 app 与 gateway 两个镜像。其中 `backend` 的 PostgreSQL 17.11 集成套件、`gateway-runtime` 的真实 Nginx、`media-browser-runtime` 的真实 ZLM 加 Chromium，都在包含本次读路径改动的代码上重新跑通；`frontend` 的类型漂移检查确认 OpenAPI 与生成的 `api-types.ts` 一致。

这一轮证明的是：读路径与回放工作区没有破坏任何既有回归，且部署产物仍可构建。它**不包含**上表新增的回放专用断言，也不包含随后的检索时间时区修复。

### 已通过：回放专用断言、站点时区修复与时间轴最小宽度（提交 `11846e0`）

[CI 37660099874](https://github.com/zhigu34/one-nvr/actions/runs/37660099874) 的 11 个 job 全部成功。其中：

- `media-browser-runtime` 执行了上表全部回放断言：真实 ZLM 产出的 MP4、真实 Chromium、经真实 Nginx 入口，包括第二次浏览器上下文用真实登录完成的未授权与跨通道读取。
- `backend` 重跑 PostgreSQL 17.11 集成套件（含读路径的 5 项真实 MP4 断言）；`gateway-runtime` 重跑真实 Nginx；`frontend` 重新确认生成类型无漂移与生产构建通过。
- `browser-runtime`、`media-contract`、`joint-media-runtime`、`production-runtime`、`hardware-cpu-runtime` 与 app / gateway 两个镜像同批通过。

回执是本次运行的 `media-browser-contract` artifact，id `11501126091`、538 字节、ZIP SHA256 `747ba3975df2e3c7867078ad88b13a11e716477a002988ec362153743406e101`、生成于 2026-10-07T17:44:32Z。**已核对的是 artifact 的存在、大小与摘要（来自 API 元数据）；其内容未下载**——下载 artifact 需要认证，本次环境没有凭据，因此本文件不引用回执里的具体数值（例如实际解码帧数）。

回执中的 `playback_*` 字段与该规格里的硬断言一一对应，例如 `playback_range_416: true` 对应 `expect(rangeProof.overflow.status).toBe(416)`、`playback_unauthorized_404: true` 对应 `expect(unauthorizedRead.status).toBe(404)`；`playback_gateway_range_passthrough` 表示同一批经 Nginx 的 206 与 `Content-Range` 断言，与既有 `live_unauthorized_denied` 等字段一样，**是对已断言事实的登记，不是独立测量**。

这一轮验证的是 `11846e0` 的树（`9de20e5`），与当前远端分支一致；本文件随后的补充说明只改文档，不改动已验收的代码。

## 现场部署检查

在 NAS 上切到含本批改动的分支（`codex/m1c-playback`，它已包含 M1-A/M1-B 的全部内容），执行根目录 `./deploy.sh`。沿用已有数据目录、端口、密钥与通道，无需重新初始化站点。

1. 登录后确认侧边栏出现「录像回放」。
2. 选择一路有录像的通道，确认检索输入框显示的是**站点时区**的墙上时间（页头同时显示该时区），点「最近 24 小时」后检索。
3. 时间轴出现片段条；**双击片段**开始播放，确认画面出现且时间前进。
4. **拖动时间轴或播放头**，确认画面跳到对应时间——这一步同时证明网关原样透传了 `Range` 与 206。
5. 依次测试暂停、±10 秒、0.5/1/2/4 倍速，以及「下一可用片段」。
6. 把范围改到一段没有录像的时间（或点到缺口），确认提示缺口起止与原因，并且没有静默跳过。
7. 回到实时预览，选中一个画面，点「查看 CHxx 即时回放」，确认进入回放页并预填最近 5 分钟。
8. 用一个只有其他通道回放权的账号登录，确认该通道不出现在回放页，且直接访问其片段返回 404。

若画面一直不出现，先在浏览器开发者工具确认 `/api/v1/recordings/<id>/content` 是否返回 206；返回 200 且长度等于整段说明 Range 在链路上被吞掉，返回 409 表示该片段状态不是 ready（提示文案会说明原因）。

## 未测与范围外

- 16–32 路并发播放的容量结论不在本轮承诺内；单通道验收不代表并发容量达标。
- 跨池与云端（OpenList/WebDAV）统一播放依赖归档业务，属后续阶段。
- 剪辑导出、录像加锁与证据保护需要数据库迁移，另批实施。
- 本文件未引用回执中的具体数值（解码帧数等），原因见上节；需要这些数字时在 CI 页面下载 `media-browser-contract` artifact 查看。
- 两台 NAS 上的真机部署与现场播放仍需用户执行；CI 通过不代表现场完成。
