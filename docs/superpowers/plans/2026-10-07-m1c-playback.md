# 历史录像回放（M1-C）Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan in the current session, task by task, followed by one independent review.

**Goal:** 在固定通道上交付受授权的历史录像回放：按通道与时间检索、单段 Range 播放、跨分片连续播放、跳转与倍速、即时回放入口，并用真实媒体在隔离环境验收。

**Architecture:** 浏览器只访问同源授权 API（`GET /api/v1/recordings`、`GET /api/v1/recordings/{id}/content`）。Go 在 `auth.Playback` 通过后按 segment id 在服务端解析存储池与冻结路径、校验发布身份（bytes/mtime），以 HTTP Range 流式返回；不暴露池根、相对路径或上游标识。跨分片与时间轴对齐由前端按 segment 列表编排；服务端不拼接文件、不转码、不写文件。

**Tech Stack:** Go、PostgreSQL、`internal/storage` 只读文件句柄、React/shadcn-admin、原生 `<video>` + Range。

**Spec:** docs/PRD.md §6.5（PLAY-01/02/08）与能力矩阵「按时间查找与普通回放」「即时回放」（P0）。M1-B 已交付元数据检索与 `AuthorizeSegment`，内容读取保持 501（internal/httpapi/recordings.go）。

## Global Constraints

- **权限**：只认 `auth.Playback`；未授权与不存在一律 404（沿用 `AuthorizeSegment` 的信息隐藏策略），不得用响应或耗时区分。
- **状态**：`state != ready`、文件缺失、identity 不匹配时不得返回任何媒体字节；返回明确错误，不伪装成功。
- **只读**：只读打开池内文件，不修改/移动/删除；M1 不自动清理录像的既有约定不变。
- **不泄漏**：响应、日志、审计都不得含池根、冻结相对路径、上游标识；日志只记 segment id、通道、字节范围与结果。
- **Range**：`Accept-Ranges: bytes`；单区间 206、越界 416（带 `Content-Range: bytes */size`）、`If-Range` 过期回 200 全量；多区间策略明确并测试（不拼 multipart）。
- **审计**：媒体访问（Range 与完整下载）写审计；审计失败不放行（与查看/导出凭据的既有规则一致）。
- **时区**：时间轴按站点 IANA 时区显示，DB 存 UTC；跨分片/跨池切换保持实际录像时间。
- **不回归**：播放并发下不影响录像写入、取流与实时预览。

## Review Focus

- 未授权/跨通道/已删除 segment：404 且与「不存在」不可区分。
- `state != ready`、identity 不匹配、池不可用：没有任何媒体字节流出。
- Range 边界：`bytes=0-`、`bytes=N-`、`bytes=N-M`、`bytes=N-`（N=size）、越界、`If-Range`。
- 播放期间录像仍在写入与预览仍可用（并发断言）。
- 前端跨分片续播的边界：片段缺口、跨池、片段缺失/损坏时的可见提示，不静默跳时间。

## Task 1: 契约与授权（B1）

Files: `docs/api/m1b.openapi.yaml`、`internal/recording/content.go`（新增）、`internal/httpapi/recordings.go`

- [x] 先写 RED：OpenAPI 的 content 端点补齐 Range 语义（`Range`/`If-Range` 参数、200/206/416/404/409/503 响应、`Accept-Ranges`、`Content-Type: video/mp4`），`x-one-nvr-implementation` 改为 implemented。
- [x] `recording.Service.OpenSegment(ctx, p, segmentID) (*Content, error)`：复用原 `AuthorizeSegment` 的权限与事务（该函数已删除，授权与解析同事务），读出 pool/target/bytes/state，返回只读句柄 + 大小 + modTime + 明确错误（not_found / not_ready / missing / damaged / pool_unavailable）。
- [x] handler 映射错误（404/409/503），删除 501 分支；绝不回落到静态文件路径。

**落地偏差（执行中修正，均已按实际代码复核）**

- 签名返回 `*Content` 而非 `Content`：`Content` 持有 `*os.File` 且实现 `Close()`，句柄语义必须是指针。
- 错误从 4 类细化为 5 类：`missing`（文件被移走）与 `damaged`（发布身份不匹配）语义不同、运维处置不同，都落在 409，与契约一致。
- 测试落在 `tests/integration/recording_content_test.go`：`OpenSegment` 需要真实数据库、存储池与发布痕迹，`internal/recording/content_test.go` 无法构造；契约/授权/状态/审计四组断言全在集成层。

## Task 2: Range 分发与审计（B2）

Files: `internal/recording/content.go`、`internal/httpapi/recordings.go`、`tests/integration/recording_content_test.go`（新增）

- [x] RED：用本机 ffmpeg 生成真实 MP4 作为 fixture，写入临时存储池与 `recording_segments`/`recording_locations` 行，断言 206 的 `Content-Range`/`Content-Length`/字节一致、416 边界、`If-Range` 行为。
- [x] Range 由 `recording.Content.PlanRange` 单一实现（单区间、suffix 形式、越界判定、`If-Range` 回退全量），handler 只做响应编排。
- [x] 审计：媒体访问写 `audit.Append`（action `recording.streamed`，segment/通道/字节窗口），审计失败不放行（drop 审计表 → 503 且零媒体字节）。
- [x] 并发断言：同分片 8 路并发 Range 请求与并发元数据查询均返回一致字节。

**落地偏差（执行中修正）**

- **不用 `http.ServeContent`**：本契约只服务单区间，多区间要 416、416 要 JSON 错误信封、且审计必须先于第一个媒体字节落库。`ServeContent` 会给多区间拼 `multipart/byteranges`、416 回 `text/plain`，并且它自己决定状态码 → 审计无法先于写响应体，与「审计失败不放行」直接冲突。改为句柄 + `io.NewSectionReader` 精确送 `[start,end]`，行为与契约一一对应。
- 顺带发现：畸形 Range（如 `bytes=abc`、`bytes=19-10`）按 RFC 7233 必须**忽略**并回全量 200，而不是 416；只有「格式合法但本 API 不服务」（多区间、`bytes=-0`、起点越界）才 416。
- **审计粒度是每请求一行**：对「谁在什么时候取走了哪些字节」最完整，代价是长回放会产生较多行。若实际量级偏高，后续按 (session, segment) 去重或加留存策略，属独立批次；不在本批偷偷降级。

## Task 3: 前端回放页与跨分片（B3）

Files: `apps/web/src/features/channels/recordings-*.tsx`、对应路由与侧边栏、`apps/web/src/features/live/*`（即时回放入口）

- [ ] RED：浏览器测试覆盖日历/时间轴选择、单段播放、拖动、±10 秒、0.5/1/2/4 倍速、跨分片续播、片段缺失/损坏提示、未授权 404 提示。
- [ ] 时间轴按站点时区显示；从实时预览进入「即时回放」（最近完成分片，默认检索最近 5 分钟）。
- [ ] 只用生成类型；`web build` / `lint` / 浏览器测试通过。

## Task 4: 真实容器与浏览器验收（B4）

Files: `tests/e2e/*`、`deploy/production/test-media*.sh`（如需）、`docs/M1-C-validation.md`（新增）

- [ ] 固定 ZLM 生成真实 MP4 → 经授权 API 播放：CI 断言 206 响应且浏览器实际解码帧增长；未授权/跨通道请求 404。
- [ ] 网关边界：私有媒体路径不可经公共路由访问（沿用既有断言）。
- [ ] 记录成功证据、失败与未测项（16–32 路容量、云端统一播放仍属未测）。

## 范围外（本批不承诺）

- 剪辑导出、录像加锁/证据保护与下载审计的导出侧（另批，需 DB 迁移）。
- OpenList/WebDAV 云端统一播放（依赖归档业务，属后续阶段）。
- 更高倍速、倒放、逐帧、多路同步回放（PRD 明确不计入首版）。
