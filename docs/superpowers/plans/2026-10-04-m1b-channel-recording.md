# one-nvr M1-B 通道接入与连续录像 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. 保留已选择的本会话原生执行方式；全部任务完成后做一次独立整分支评审。Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在固定通道上配置、测试、更换和批量导入摄像头，由 ZLM 生成连续录像，建立可恢复、可追溯到原视频源的本地分片索引。

**Architecture:** 复用 M1-A 的 Go API/Worker、PostgreSQL、持久秘密、权限、任务租约与目录池。API 保存不可变源版本和期望状态；Worker 执行测试、切换、录制及对账；ZLM 写入同池工作目录，Worker 发布标准文件。发布、源切换和媒体恢复使用持久意图，不用内存状态或当前摄像头猜测历史归属。

**Tech Stack:** Go 1.27.1、pgx/v5 5.11.0、PostgreSQL 17.11、React/TypeScript/shadcn-admin、现有固定 digest ZLM；FFmpeg/ffprobe 仅作有界探测，不负责正式录像。

**Spec:** [M1 控制面设计](../specs/2026-10-03-m1-control-plane-design.md) 第 3–6、8–10 节；[PRD](../../PRD.md) CH-03/05/09/10/11/12、REC；[录像文件规则 v3](../../recording-file-layout.md)。

**状态：** 用户已确认继续，沿用本会话原生执行；Task 1 已实现，Tasks 2–10 按顺序执行。M1-A 功能基线 `93dfefc7ff3fbf8b4ab7b6a385fa7e67927b8f24` 已通过 CI，最新文档基线 `904c9d4d6118ca15ed9659d20df22c2edadb7405`。M1-A 草稿 PR #1 保持其已交付范围，M1-B 执行时从该基线创建 `codex/m1b-channel-recording` 分支，不自动合并 main。

## Global Constraints

- 单站点 Linux amd64，固定 16/32 槽位，编号 CH01–CH32；更名、换源、改池不能改通道 ID、授权或历史媒体。
- 正式八列为 `channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path`；额外仅允许可选 `onvif_port`，本阶段保存该端口，不实现 ONVIF 自动发现。
- IPv4/IPv6 字面量；端口 1–65535，空 RTSP 端口 554；主路径单 `/` 开头，子路径可空。拒绝完整 URL、`//host`、控制字符、fragment、无效百分号转义和掩码密码。
- CSV UTF-8/BOM、标准引号/逗号/换行，最多 1 MiB、32 条记录；预览有效期 30 分钟。测试并发上限 2，结果有效期 5 分钟；主流须读到真实视频帧。
- 凭据 AES-256-GCM；AAD 绑定 site/channel/revision/field，随机 nonce、key ID；已有秘密缺失/不匹配不得重新生成替代。普通响应无用户名明文、密码、完整 RTSP URL、ZLM 管理密钥。
- 密码查看/配置导出须管理员角色及每目标通道 `configure` 授权；成功审计与读取同一事务，审计失败不交付明文，响应 `Cache-Control: no-store`。操作员可在授权通道输入新凭据，不能查看/导出已有凭据。
- 会话、CSRF/Origin、授权版本及 M1-A 的 HTTP/HTTPS Cookie 策略继续使用。GET 状态轮询不续期会话；明文不得进入 Query 缓存、浏览器持久存储、URL 或日志。
- Worker 租约 30 秒、10 秒续期；外部调用最长 10 秒；切换就绪窗口 30 秒，回滚窗口 30 秒，持久 deadline 不因重启重新计时。退避 2/4/8/16/30 秒；同通道 session advisory lock 与 fencing 验证共同约束副作用。
- ZLM 是唯一正式录像器。默认分片目标 60 秒、不转码；Frigate 录像仍 `record.enabled=false`；模块关闭不启动其依赖，常驻容器数仍 5/7/6/8。
- 池只登记已有 `/storage` 下目录，保留身份标识；API/Worker/ZLM 三方写入证据、10 秒采样/30 秒过期；不创建丢失池根，不管理 NAS/RAID/挂载。
- 标准路径 `recordings/CH01/YYYY-MM-DD/CH01_YYYYMMDD_HHMMSS_<32位小写录像UUID>.mp4`；DB/API UTC，路径采用站点 IANA 时区，默认 Asia/Shanghai；首次发布意图冻结时区、offset、原始开始时刻和路径。
- M1 无循环删除；安全线 `max(10 GiB, 同一文件系统全部应录通道近期总码率的10分钟写入量)`，恢复到两倍安全线后才恢复录制。禁止将未知码率当作 0 或重复累加同一流的不同协议观测。
- M1-B 交付 `none/continuous` 普通录像策略和最小索引查询。周计划、事件录像/前录、云归档、完整性评分分别留在 M2/M3/M4；`event_recording` 保持 false，不开放未实现能力。
- WebRTC 授权、分屏、SSE、Range 单段回放按已有切分留在 M1-C；M1-B 不公开原生录像目录、ZLM 播放 URL 或暂时绕过媒体鉴权。
- 不修改既有四个迁移的内容/校验和，不改 M0。禁止将未跟踪的私有 M0 验收文件、真实摄像头配置或部署密码放入提交、构建上下文或 CI artifact。

## Review Focus

- 第一次接入尚无默认池/可用源：关闭录像可以接入；选择连续录像必须完成池验证，不能陷入“没有源不能测池、没有池不能接源”循环（Task 3/4/6）。
- Worker 在外部副作用成功、事务提交之前崩溃或耗尽普通重试次数：原切换/录制意图仍可对账，不能一直 applying 或重复启动（Task 6/7/10）。
- 旧源尾片、重复 Hook、spool 重放在换源/改池后到达：仍归属旧 revision/run/pool，名称/时区变更不改变已冻结路径（Task 4/5/10）。
- 预览后撤权、改配置、批次过期或重试：逐项复核，成功行不重放，过期不能应用，明文不残留在失败反馈（Task 2/8/9）。
- 池目录被替换、目标已存在、同文件系统池共享容量或短暂低空间：核验身份，不覆盖/复制删除原片，只阻断相关录制，仍可列出历史媒体（Task 4/5/7）。

## 分阶段交付与已细化的边界

1. Tasks 1–2：可保存加密草稿、查看脱敏源历史、受权显式查看凭据；仍不发布新源。
2. Tasks 3–5：固定 ZLM 的真实测试/写入证据与完成片发布；测试媒体只进入专用 probe 工作目录，不进入正式录像列表。
3. Tasks 6–7：源切换、关闭/连续录制、改池、源与录像状态历史；普通录像关闭仍维持主/子流取流。
4. Tasks 8–9：CSV/JSON 预览、测试、逐项应用和前端完整操作流程。
5. Task 10：两路模拟摄像头的真实 Docker/浏览器 CI、故障恢复验收、部署文档与一次最终评审。

现有 `recording_policies.mode=none` 不在迁移中批量改为 continuous。首次手动配置空通道的表单默认勾选连续录像，用户可关；提交明确携带首次录像选择。导入文件不携带/覆盖策略，预览另有“未配置通道首次接入后连续录像”的明确选项，默认开启；已有源通道始终保留原策略/池/事件开关。缺池时用户可改选关闭录像，不能暗中降级已选择的连续录像。

增加非秘密部署项 `ONE_NVR_CAMERA_CIDRS`（逗号分隔），示例 `192.168.33.0/24,192.168.66.0/24`。缺省空集合仍允许初始化面板，但拒绝保存/测试/应用实际源并提示配置允许网段；不自动授权所有私网。运行时再排除当前内部管理服务地址、loopback、unspecified、multicast、link-local 与 IPv4-mapped 绕过；新增/应用/重试均校验同一规范规则。摄像头因 RTSP 重定向跳往不允许的目标也不能通过固定镜像验收；若该镜像不能约束这一行为，停止该源能力放行并报告，不以字符串校验声称网络限制已验证。

## 文件与类型边界

不另建微服务。扩充 `internal/channel` 管理源/身份/批次；`internal/recording` 负责 run、Hook inbox/spool、发布、索引；`internal/app` 串联两者，避免 channel 与 recording 相互 import。API Handler 放现有 `internal/httpapi`，复用 `protected`/If-Match/错误封装。

| 单元 | 创建/修改的文件 | 职责 |
| --- | --- | --- |
| 数据/契约 | `migrations/0005_channel_recording.sql`, `internal/channel/source_types.go`, `internal/recording/types.go`, `docs/api/m1b.openapi.yaml` | 保留 M1-A 数据，复合 FK/唯一约束、脱敏 DTO、端点与异步状态 |
| 凭据与地址 | `internal/secrets/credentials.go`, `internal/channel/{address,source_repository,source_service,source_credentials}.go`, `internal/config/{config,env,render}.go` | 加密边界、部署允许范围、不可变源/身份、查看/导出 |
| ZLM/探测 | `internal/media/zlm/{client,protocol,probe}.go`, `internal/media/probe/{runner,types}.go`, `deploy/production/Dockerfile.app`, `deploy/production/media-packages.lock` | 私有 HTTP typed client、固定镜像契约、受限首帧/结构探测 |
| Hook/池 | `internal/recording/{hooks,inbox,spool,run_repository}.go`, `internal/storage/{media,path,probe,service}.go` | 服务身份、有界持久收件箱、ZLM 写入证据、池根受限操作 |
| 发布/索引 | `internal/recording/{identity,path,publish,publish_linux,publish_darwin,index}.go` | 稳定 UUIDv5、冻结命名、同池不覆盖发布、恢复和时间检索 |
| 执行与状态 | `internal/channel/{switch_repository,switch_service,status}.go`, `internal/recording/{recorder,scheduler,capacity}.go`, `internal/app/{run,sourcejobs,recordingjobs}.go`, `internal/jobs/repository.go` | 每通道互斥、切换/回滚、运行代次、持续对账及状态历史 |
| 批量导入 | `internal/channel/{import_parse,import_repository,import_service}.go`, `internal/app/importjobs.go` | 限额解析、加密预览、逐行复核/测试/应用/取消 |
| HTTP/前端 | `internal/httpapi/{router,channels_source,channels_import,recordings}.go`, `apps/web/src/features/channels/`, `apps/web/src/routes/_authenticated/channels/{index,configure}.tsx`, `apps/web/src/lib/api-types.ts` | 真实源操作、进度/失败反馈、无明文缓存、生成类型 |
| 验收 | `tests/integration/{source,source_switch,recording,source_import,http_sources}_test.go`, `tests/e2e/m1b.spec.ts`, `deploy/production/{compose.media-test.yaml,test-media.sh}`, `.github/workflows/foundation-ci.yml`, `docs/M1-B-validation.md` | 真实 DB、可控故障、固定 ZLM 与两路 RTSP fixture、实际浏览器 |

通用 ID、权限、事务使用现有 `id.ID`、`auth.Principal`、`auth.RequireChannelTx`、`database.DB.WithinTx`。下列是拟实现签名，不表示当前已有：

- `channel.SourceConfig`：IP/RTSPPort/MainPath/SubPath/Transport/可选 ONVIFPort；不含明文凭据，不公开 raw RTSP URL。`channel.CredentialInput` 使用 `Username *string`、`Password *string`、`PasswordAction`（keep/replace/clear）；明文类型实现脱敏 `String`，不自动 JSON 编码。
- `channel.DraftInput{Config SourceConfig, Credentials CredentialInput, IdentityIntent string, HistorySourceID id.ID}`；IdentityIntent 为 modify/replace/history；modify 仅限已有身份，replace 新建本通道身份，history 仅选本通道已有身份。
- `channel.SourceRevision` 为只读脱敏 DTO：ID/ChannelID/SourceID/Number/Config/UsernameSummary/PasswordState/CreatedAt；字段密文在专用 repository 私有类型中。
- `channel.SourceApplyInput{RevisionID, TestID id.ID, ExpectedVersion int64, FirstRecordingMode *string}`；FirstRecordingMode 仅未配置槽位首次接入可用，取 none/continuous；已配置通道拒绝该字段。
- `channel.SourceTestResult`：ID/RevisionID/State/Main 与 Sub 探测结果/ObservedAt/ExpiresAt；每流保存首帧是否验证、编码/宽高/帧率/脱敏错误；没有子流与子流失败分开。
- `channel.Change{JobID id.ID, State string}`；`channel.Status`：desired/current revision、enabled、分项连接/录制状态、观察/过期时间和 reason，不返回内部物理流地址。
- `zlm.StreamKey{VHost,App,Stream string}`、`zlm.ProxyInput{Key StreamKey, URL string, Transport string}`（仅 Worker 内部）；`zlm.StreamSnapshot` 包含轨道就绪/帧计数/采样时刻/实际录制状态，排除 originUrl 等凭据字段。
- `recording.Run`：SiteID/ChannelID/SourceRevisionID/SessionID/PoolID/ID、StreamKey、WorkRelativePath、Purpose(probe/continuous)、State、CreatedAt；历史绑定不可更新。
- `recording.Completion`：StreamKey/MediaServerID、原始 FilePath、Size、StartTime UTC、Duration；`recording.Segment` 为只读公共 DTO：ID/ChannelID/SourceRevisionID/PoolID/RunID、Start/End、Bytes、State、NamingTimezone/UTCOffset、基础检查结果，不含原生绝对路径。
- `recording.Handle{Run Run}`；`recording.StartInput{ChannelID,SourceRevisionID,SessionID,PoolID id.ID, Key zlm.StreamKey, Purpose string}`。
- `channel.ImportPreview{BatchID id.ID, ExpiresAt time.Time, Items []ImportItem}`；ImportItem 仅行号/本地 ChannelID/脱敏差异/错误/预期版本，凭据不进入 DTO。`ImportSelection` 明确 Row/ChannelID/ExpectedVersion/IdentityIntent/PasswordAction/ImportName/首次录像选择。
- `channel.Execution` 包含 `jobs.Lease` 和持锁 `*pgx.Conn`；`Check(ctx)` 在每次外部副作用/状态写入前检查租约与对应 fence，失联即取消。任务 payload 只保存对象 ID/版本，不保存地址或凭据。

辅助类型与签名约定（实现时按包归属定义，不使用匿名跨包类型）：

- 本文 service 方法的 `ctx` 均为 `context.Context`，`p` 为 `auth.Principal`，`expected` 为 `int64`，`key` 为 `string`，所有资源 ID 为 `id.ID`；涉及多个 ID 时名称决定其语义，不能交换通道与源 ID。
- `secrets.EncryptedCredential{KeyID string,Nonce,Ciphertext []byte}`；KeyID 是主密钥的域分离标识，不提供密钥轮换 UI。
- `channel.NetworkPolicy{Allowed []netip.Prefix,Denied []netip.Addr}`；`RevisionPage{Items []SourceRevision,NextCursor *id.ID}`；`RevealedCredentials{Username,Password string}` 仅用于已完成审计的敏感HTTP响应。
- `channel.ExportFile{FormatVersion int,SiteID id.ID,ExportedAt time.Time,Channels []ExportItem}`；ExportItem 含八列明文（可选onvif_port）及本地channel_id/source_revision_id追溯，不含其他策略。普通JSON DTO不得引用 ExportItem。
- `zlm.ProxyRef{Key StreamKey,OpaqueKey string}` 仅内部；`probe.VideoEvidence{FirstFrame bool,Codec string,Width,Height int,FPS float64,ObservedAt time.Time}`；`probe.FileEvidence{Video VideoEvidence,Duration time.Duration,Size int64,Readable bool}`。
- `recording.Media` 包含 Task3 的 AddProxy/RemoveProxy/Inspect/StartRecord/StopRecord 同签名方法；`recording.Probe` 包含 FirstFrame/InspectMP4 同签名方法。`recording.FrozenPath{RelativePath,Timezone string,UTCOffsetSeconds int,OriginalStart time.Time}`。
- `recording.Page{Items []Segment,NextCursor *id.ID}`；`BitrateSample{SessionID id.ID,BytesPerSecond int64,ObservedAt time.Time}`；负数/重复Session样本拒绝，不从同一流多协议重复求和。
- `channel.ParsedItem` 为仅解析阶段明文 Row/八列输入及可选元信息；`ImportProgress{BatchID id.ID,State string,Items []ImportItem}`；ImportItem 另含持久 State（draft/testing/tested/queued/running/succeeded/skipped/failed/cancelled）、JobID 与固定错误码，不含文件原文。
- Task8 的 RetryImport 签名与 TestImport 相同；实现选择行重试，不能隐式重试全批。`SourceService.ListRevisions(ctx,p,channelID,cursor id.ID,limit int) (RevisionPage,error)`。

所有端点由 Task 1 的 OpenAPI 定义具体字段、成功/失败状态及权限。M1-A 原端点和 DTO 保持兼容，扩展 `/channels` 状态字段不改编号类型。尚未完成的新增能力不在 UI 开放。

## Task 1: 保留现有数据的模型与接口契约

**Files:** Create 数据/契约行所列文件及 `tests/integration/source_schema_test.go`；Modify `migrations/embed.go`（仅如现有嵌入规则需要）、前端类型生成脚本，保留 `docs/api/m1a.openapi.yaml`。

**Interfaces:** 定义上文共有类型。新增 `channel.NewSources(db *database.DB,a *auth.Service,secret secrets.State,network NetworkPolicy) *SourceService` 与 `recording.New(db *database.DB,media Media,probe Probe,roots []string,dataDir string) *Service`；`Media` 是 Task 3 typed ZLM 操作，`Probe` 是 Task 3 媒体探测操作。

- [x] **Step 1:** 写 `TestSourceSchemaPreservesFoundation`：先只应用 0001–0004 并建立站点/授权/池，再升级；原 ID/授权/时区/none 策略及 TLS 状态相同。`TestSourceSchemaRejectsCrossChannelAndDuplicateRun` 断言跨通道 current FK、同通道非终态 switch、相同 run+原始路径/池+标准路径重复均拒绝；移除源不能级联删除历史。
- [x] **Step 2:** `./deploy/production/dev.sh test-db ./tests/integration -run 'SourceSchema'`，确认因缺少新增模型失败。
- [x] **Step 3:** 创建 0005：source_identities/revisions/tests/switches、import_batches/items、stream_sessions、recording_runs/segments/locations/gaps、hook_inbox、source_observations。扩充 channels current/desired/pool/enabled，策略版本沿用现有列；source密文、状态/阶段及 composite FK 明确约束。增加来源键、时间区间、恢复队列索引；run/test/probe/正式记录用途独立。状态历史表保存 transition/过期 unknown，不宣称可用率评分。
- [x] **Step 4:** 新 OpenAPI 定义源历史/草稿/test/apply/clear/reveal/export、导入 preview/get/test/apply/retry/cancel、pool 改绑、recording-policy 与最小 `GET /recordings`，沿用 `/api/v1`、If-Match、幂等和分页。版本冲突 409；未实现播放 content 501。生成组合类型，运行上述真实 DB 测试、`web api:generate`/build，类型无漂移。
- [x] **Step 5:** 仅提交本任务列明文件，`feat: define channel source and recording contracts`。

## Task 2: 加密草稿、地址验证与凭据交付

**Files:** Create 凭据与地址行的新文件及相邻 `*_test.go`、`tests/integration/source_test.go`, `internal/httpapi/channels_source.go`；Modify `internal/channel/service.go`, config 文件、`.env.example`, `internal/httpapi/router.go`, `internal/app/run.go`。

**Interfaces:** `secrets.State.SealCredential(siteID,channelID,revisionID id.ID,field string,value []byte) (EncryptedCredential,error)`；`OpenCredential(...) ([]byte,error)`；`channel.ParseNetworkPolicy(string,[]netip.Addr) (NetworkPolicy,error)`；`SourceConfig.Validate(NetworkPolicy) error`、`BuildRTSPURL(config SourceConfig,username,password,path string) (string,error)`；`SourceService.CreateDraft(ctx,p,channelID,expected,in) (SourceRevision,error)`、`ListRevisions(ctx,p,channelID,cursor,limit)`、`Reveal(ctx,p,channelID,revisionID) (RevealedCredentials,error)`、`Export(ctx,p,[]id.ID) (ExportFile,error)`。

- [x] **Step 1:** 写 `TestCredentialAADAndMissingKey`：不同通道/版本/字段不能解密，同值每次 nonce 不同，密文/key错返回固定错误、不回退。`TestRTSPAddressAndAllowedCIDRs` 覆盖 IPv6、含 `@:/?#%` 的凭据、已有路径转义/query、空554/子路径、mapped-IP 和内部服务排除；原地址内容不进入错误字符串。
- [x] **Step 2:** 运行 `dev.sh test-go ./internal/secrets ./internal/channel ./internal/config` 和 `dev.sh test-db ./tests/integration -run 'SourceDraft|CredentialReveal|SourceExport'`，确认功能缺失失败。
- [x] **Step 3:** 实现不可变草稿与身份意图，事务锁版本并审计。keep 必须从所选本通道原版本读取后用新 revision AAD 重新加密；空密码是有效状态，不与未保存混淆；保存草稿不更改 current/旧录像。配置部署网段并在 Worker 重试重新校验。
- [x] **Step 4:** 测 `TestCredentialRevealAuditFailure` 与 `TestSourceExportSnapshotAuthorization`：管理员无该 configure 拒绝，operator/viewer 拒绝，事务内审计失败无明文响应，多通道任一解密失败整体失败；一致快照导出 `format_version=1/site_id/exported_at` 和八列配置追溯信息。对普通列表/错误/jobs/audit 做固定虚构凭据标记搜索，无泄漏；重新运行测试。
- [x] **Step 5:** 提交 `feat: add encrypted immutable channel source drafts`。

## Task 3: 固定 ZLM 契约、真实首帧与运行镜像

**Files:** Create ZLM/探测行文件与单元测试、`deploy/production/{compose.media-test.yaml,test-media.sh}`、`docs/M1-B-validation.md`；Modify 现有 ZLM client、Dockerfile.app、`cmd/admin/components.go`、CI、镜像基线说明。

**Interfaces:** `zlm.New(baseURL,secret string,client *http.Client) (*Client,error)`；`Client.AddProxy(ctx,ProxyInput) (ProxyRef,error)`、`RemoveProxy(ctx,ProxyRef) error`、`Inspect(ctx,StreamKey) (StreamSnapshot,error)`、`StartRecord(ctx,StreamKey,workDir string,maxSeconds int) error`、`StopRecord(ctx,StreamKey) error`。`probe.Runner.FirstFrame(ctx,internalURL string) (VideoEvidence,error)`、`InspectMP4(ctx,file *os.File) (FileEvidence,error)`；Evidence 仅 typed 轨道/时间/尺寸/结果。

- [x] **Step 1:** 写 `TestZLMRejectsFalseSuccessAndCredentialEcho`：HTTP200但code非0、result=false、过大/坏JSON、回显URL的msg都不能成功/泄密；禁止redirect，10秒超时。`TestProbeBoundedAndInternalOnly`：只允许持久映射生成的 ZLM 内部流，不接摄像头URL/任意命令/外部地址；子进程超时后确实结束且输出≤1MiB。
- [x] **Step 2:** 运行对应 Go 包测试确认红灯；在 CI 加固定镜像真实媒体 job，先运行 `test-media.sh --contract`，缺探测工具/操作应失败，不能用模拟HTTP替代该门槛。
- [x] **Step 3:** POST form 调用固定 API，管理 secret 不进入URL/log/公共DTO；拉流 `enable_mp4=0/enable_hls=0`，测试不自动录制，禁用透明无限重连，由业务对账建立新 generation。首帧用 FFmpeg 只解码一帧、固定参数/超时，无输出录像；MP4 结构用 ffprobe FD/受限路径。将 Bookworm 的 ffmpeg/ffprobe 及运行依赖从可验证 Debian snapshot 固定包版本/哈希至 media-packages.lock，安装源可配置但不得关闭签名/TLS或浮动升级，CI 记录实际版本。
- [x] **Step 4:** 在独立 test Compose 用另一固定 digest ZLM 充当摄像头 RTSP 服务，生成两路主/子H.264虚构样本，fixture 服务只在测试 profile 存在。实际检查拉流/首帧、开始/停止/状态、60秒与尾片、customized_path实际追加层级、完成Hook字段/路径、无人观看仍取流、重定向约束、敏感日志输出。保存脱敏契约证据；不匹配时修订适配器并保持 gate失败，不能按当前在线文档猜固定镜像行为。
- [x] **Step 5:** 单元及实际CI通过后提交 `feat: verify pinned ZLM media and bounded probes`；运行时常驻仍5/7/6/8，无新增录像容器。

## Task 4: 持久 Hook 收件箱和 ZLM 池写入验证

**Files:** Create Hook/池行文件及 `tests/integration/recording_hook_test.go`、spool/media 单元测试；Modify `internal/app/run.go`, `cmd/admin/components.go`, storage checks/RequestCheck 路径。

**Interfaces:** `recording.Service.Accept(ctx,Completion) error`（DB inbox 成功或 fsync spool 后返回）；`DrainSpool(ctx) error`；`recording.NewHookHandler(service *Service,token string) http.Handler`；`storage.Service.OpenMediaRoot(ctx,poolID id.ID) (*os.Root,Pool,error)`；`PublishZLMEvidence(ctx,poolID,runID id.ID,evidence WriteEvidence) error`。`zlm.WriteEvidence` 明确 run/文件存在/结构检查/实测时间，不用业务手写文件代替。

- [x] **Step 1:** 写 `TestHookDurabilityAndWrongIdentity`：错误服务令牌/超64KiB请求拒绝，合法Completion重复只一条；DB不可用落盘后应答，DB+spool都不可写必须非成功；hook的url/folder不用作媒体来源。`TestPoolProbeRequiresZLMFile` 断言 API可写/ZLM不可写不能healthy。
- [x] **Step 2:** `dev.sh test-db ./tests/integration -run 'Hook|PoolProbeRequiresZLM'`、`test-go ./internal/recording ./internal/storage` 红灯。
- [x] **Step 3:** Worker 独立内部 hook listener `:8083`，不挂gateway、不发布宿主端口。由主密钥域分离派生 hook 身份，仅私有生成ZLM配置持有；限制body/时间/字段，校验mediaServerID和StreamKey绑定，未登记流进入脱敏诊断不分配当前源。ZLM生成配置 `apiDebug=0`、MP4默认关闭、auto_close关闭、无人观看hook不关闭；M1-B播放hook仅允许Worker经已映射内部流持有的私有探测凭据，不接受匿名RTC/原生文件读取，M1-C再接入业务票据。spool为 `/data/recording-spool` 0700/0600，独立原子文件+目录fsync，256MiB上限，满时显式失败且不淘汰已有未处理回调；重放成功后才删。
- [x] **Step 4:** 临时测试源分配持久 Purpose=probe run，实际 ZLM 录制到池内专用 `.work/probes/zlm/<run_id>`，stop后等真实完成片并验证读取/结构；再受限删除该probe媒体。API/Worker基础检查健康即可进入此验证，不要求已有zlm healthy。无测试源仍pending；成功写证据30秒有效，正式活跃run完成片/实际状态持续刷新，空闲已验证池受控重测而不伪造新证据。测试 symlink/marker/离线/服务视角不一致，重跑Go/DB和 `test-media.sh --probe`。
- [x] **Step 5:** 提交 `feat: persist media hooks and verify ZLM pool writes`。

## Task 5: 同池标准文件发布、索引与崩溃恢复

**Files:** Create 发布/索引行文件与相邻测试、`tests/integration/recording_publish_test.go`, `internal/httpapi/recordings.go`；Modify storage业务占用查询、HTTP注册与运维诊断。

**Interfaces:** `recording.StableID(siteID,poolID,runID id.ID,originalRelativePath string) (id.ID,error)`（UUIDv5）；`FreezePath(channelNo int,start time.Time,timezone string,recordingID id.ID) (FrozenPath,error)`；`Service.Publish(ctx,inboxID id.ID) (Segment,error)`、`Recover(ctx) error`、`List(ctx,p auth.Principal,q Query) (Page,error)`。`Query{ChannelID,Start,End,Cursor,Limit}` 单通道且≤31天，交集条件为 start<query.end且end>query.start。

- [x] **Step 1:** 写 `TestRecordingPathTimezoneAndIdentity`：UTC 2026-10-03T16:04:39Z→上海 `2026-10-04/CH01_20261004_000439_<id>.mp4`；同秒不同原片不同ID；DST重复小时仍唯一；重放与时区修改不改冻结路径。`TestPublishCrashAndNoOverwrite` 覆盖移动前、移动后提交前崩溃、目标冲突、跨设备和目录同步失败，原片/证据保留。
- [x] **Step 2:** 运行 `dev.sh test-go ./internal/recording`、`test-db ./tests/integration -run 'RecordingPublish|RecordingQuery'` 确认红灯。
- [x] **Step 3:** inbox→按run定位普通已关闭MP4→结构/大小校验→事务保存finalizing意图→同池原子不覆盖rename→fsync→事务ready/local location。publisher用DB claim/fence（30秒租约、10秒续期）领取inbox，唯一来源键与不覆盖移动同时约束重复执行，旧claim不能提交ready；不套用普通job有限尝试丢弃未发布片。Linux使用renameat2 RENAME_NOREPLACE；Darwin仅供本机测试用等价不覆盖原语，两者均无copy/unlink降级。对根、父目录/末端symlink和inode替换做受限核验；冲突不覆盖/删除。重试先查来源键，移动后用已冻结文件身份核对目标。
- [x] **Step 4:** 只扫描登记run/发布意图/登记业务位置，恢复丢Hook片经固定版本原生文件规则和媒体证据建立来源；缺可靠绝对时间的片标 provisional/待核查，不仅凭mtime就ready。写 `.meta/recording-runs/<UUID>.json` 无凭据描述并校验site/pool；不自动接管未知文件。测试旧源迟到、重复回调源路径已移走、正在写点号临时片、损坏/未知片、no当前源仍可按授权列出历史；池占用只统计实际ready local位置并标明未发布占用未知。GET metadata按playback授权，未授权404、缺失媒体状态真实；content保持501直到M1-C。重跑测试和 `test-media.sh --publish`。
- [x] **Step 5:** 提交 `feat: publish channel recordings with recoverable index`。

## Task 6: 测试、源切换与故障回滚

**Files:** Create channel执行与状态文件、`internal/recording/recorder.go`, `internal/app/sourcejobs.go`, `tests/integration/source_switch_test.go`；Modify jobs运行策略、HTTP source handler、进程 wiring。

**Interfaces:** `SourceService.RequestTest(ctx,p,channelID,revisionID,key) (Change,error)`、`TestResult(ctx,p,channelID,testID) (SourceTestResult,error)`、`RequestApply(ctx,p,channelID,in SourceApplyInput,key string) (Change,error)`、`RequestClear(ctx,p,channelID,expected,key) (Change,error)`；`recording.Service.Start(ctx,Execution,StartInput) (Handle,error)`、`Stop(ctx,Execution,Handle) error`；`app.ExecuteSourceTest(ctx,Execution) (jobs.Result,error)`、`ExecuteSourceChange(ctx,Execution) (jobs.Result,error)`。

- [x] **Step 1:** 写 `TestSourceTestDoesNotInterruptOldRun`、`TestSourceSwitchRollbackAndLateTail`：测试两并发上限真实DB计数不因多Worker越界；主流测试5分钟到期或配置摘要不匹配不能发布；子流失败降级。切换成功current更新，失败旧revision以新generation恢复，旧尾片归旧run；回滚失败可见真实状态，不伪称旧源在线。
- [x] **Step 2:** `dev.sh test-db ./tests/integration -run 'SourceTest|SourceSwitch'` 确认缺少执行器红灯。
- [x] **Step 3:** 持久阶段 queued→testing→stopping_old→starting_new→verifying_new→committed 或 rolling_back→rolled_back/failed。临时测试源有专用 session，成功后清理，最多2个DB全局名额。每次流物理ID包含新session/generation，actual recorder restart总分配新run；调用前先保存意图/run descriptor，再操作，返回值后检查实际首帧/期望录制状态。同通道使用pgx专用连接advisory lock，任务renew与该连接活性共同控制Execution，不长时间持有配置事务。
- [x] **Step 4:** 事务复核授权/expected并保存job/audit；任务开始前再复核（撤权后未开始不执行），已经开始的变更继续安全完成/回滚。停止旧源收尾当前片，新源符合期望才commit current；none不要求池且不会录制，continuous必须源+池证据齐全，首次默认池绑定与明确策略一并提交。清空/禁用收尾并保留历史。测试 `TestSourceApplyLastAttemptCrashReconciles`：source.apply/clear/pool_switch/policy_apply 属持久副作用意图，普通max_attempt耗尽不能把未对账域任务丢弃；只扩展明确类型白名单，正常source.test仍有限重试。源切换deadline持久；重启先观察已有唯一物理流/run，不按超时盲目重做；相同配置跳过，更名不重连。重跑DB及真实 `test-media.sh --switch`。
- [x] **Step 5:** 提交 `feat: reconcile channel source switches and rollback`。

## Task 7: 普通录像开关、改池、容量与持续状态

**Files:** Create recording scheduler/capacity、`internal/app/recordingjobs.go`、`tests/integration/recording_scheduler_test.go`；Modify channel状态、storage引用限制/探测/占用、HTTP policy/pool入口、worker启动。

**Interfaces:** `SourceService.SetPolicy(ctx,p,channelID,expected int64,mode,key string) (Change,error)`；`BindPool(ctx,p,channelID,poolID,expected,key) (Change,error)`；`recording.Service.Reconcile(ctx,channelID id.ID) error`、`Monitor(ctx) error`；`recording.SafetyLine([]BitrateSample) (bytes int64,known bool)`；`SourceService.GetStatus(ctx,p,channelID) (Status,error)`。

- [ ] **Step 1:** 写 `TestRecordingOffKeepsPullAndHistory`：none停止录制，取流继续，换源/重启不重置策略、不开始事件buffer；`TestPoolSwitchKeepsOldLocations`：先验证目标池，收尾旧run，新片入新池，旧位置不变，改默认池不改绑定。
- [ ] **Step 2:** `dev.sh test-db ./tests/integration -run 'RecordingOff|PoolSwitch|RecordingCapacity|RecordingRecovery'` 红灯。
- [ ] **Step 3:** Worker10秒对账期望/实际、源可读/帧进展/录制状态/最近完成片，数据过期显示unknown。禁止以流已登记推算摄像头可用率；源恢复创建新session/run，Worker/API重启而上游仍录制则复用原run。DB不可用不强停已有ZLM录制、不执行新未知配置；根/marker/写失败时停受影响run并持久gap/原因，恢复后新run不覆盖旧片。
- [ ] **Step 4:** 按文件系统聚合近5分钟有效主流码率（10秒采样，至少两次有效且≤30秒过期），同时考虑已索引片实际bytes/duration；取可证据支持的保守较高估值。无近期证据的continuous启动阻断并显示bitrate_unknown（初次真实测试供证据），不得把未知当0；恢复两倍线并连续两次健康样本。测试10GiB底线、600秒写入量、溢出饱和、共享池去重和仅相关池阻断；停用池新写入停止，已有引用池不能删除、历史索引不消失。明确记录中断/未知区间而非完整性评分，重跑DB和 `test-media.sh --recovery`。
- [ ] **Step 5:** 提交 `feat: control continuous recording and pool recovery`。

## Task 8: 批量预览、测试、逐行应用与配置回导

**Files:** Create 批量导入行文件与测试、`internal/httpapi/channels_import.go`, `tests/integration/source_import_test.go`；Modify source服务、worker注册和HTTP routes。

**Interfaces:** `channel.ParseImport(r io.Reader,format string) ([]ParsedItem,error)`；`SourceService.PreviewImport(ctx,p,format string,r io.Reader) (ImportPreview,error)`、`SubmitImport(ctx,p,batchID id.ID,[]ImportSelection,key string) (Change,error)`、`GetImport(ctx,p,batchID) (ImportProgress,error)`、`TestImport(ctx,p,batchID,rows []int,key string) (Change,error)`、`RetryImport(...) (Change,error)`、`CancelImport(ctx,p,batchID) error`；`app.ExecuteImport(ctx,Execution) (jobs.Result,error)`。

- [x] **Step 1:** 写 `TestImportCSVLimitsAndPasswordSemantics`：八列/BOM/引号/逗号/换行、可选onvif端口、1MiB/32条边界、重复/未知header；CSV空密码明确空，keep在预览显式选择；JSON缺失/空密码keep/clear，掩码拒绝。`TestExportRoundTripsForeignAndSameSite`：同站核验ID+编号，跨站仅用本地编号/映射，不写入外部UUID。
- [x] **Step 2:** 跑 `dev.sh test-go ./internal/channel`、`test-db ./tests/integration -run 'Import|RoundTrip'` 确认红灯。
- [x] **Step 3:** 请求限额作用于原始multipart整体与文件内容；UTF-8/格式version校验，解析错误不回显整行秘密。加密保存规范草稿而非原明文文件；预览不改current。空槽位空源行明确skip不清空；已配置目标必须确认更新与身份意图，重复目标拒绝、重复地址警告；策略/权限字段按导出元信息规则忽略并提示，任意未知顶层版本/核心字段拒绝。
- [x] **Step 4:** 测 `TestImportExpiryRevocationAndPartialRecovery`：30分钟后不能新提交/测试/重试，已提交事务冻结的行不因执行超过TTL突然取消；撤权/expected冲突逐行失败，只有未执行项取消、已开始安全终结；关页继续，重启/重复key不重放成功项，失败项重试须再测并复核最新版本，不silent覆盖；单行失败不回滚其他成功行。同配置且名称未变skip；仅改名称不重新拉流；已有源模式/池/事件开关保持不变。重跑DB和实际三行混合有效/失败/冲突场景。
- [x] **Step 5:** 提交 `feat: import and export channel source configurations`。

## Task 9: 真实通道配置、导入进度与状态前端

**Files:** Create `apps/web/src/features/channels/{api,source-form,source-history,source-test,source-import,source-credentials,recording-policy,channel-status}.tsx`（api为`.ts`）及相邻测试、`tests/e2e/m1b.spec.ts`；Modify existing channels routes、生成类型、必要基础通道表格代码；不整体重写shadcn-admin布局。

**Interfaces:** 只消费生成 OpenAPI DTO 与同源 apiRequest；复用固定ChannelID。`SourceForm` 保存密码仅组件内存；`CredentialReveal` 使用独立POST不走useQuery/useMutation共享缓存，响应只存本地state。批任务进度通过现有job/新增import状态查询，每次HTTP按当前会话验证。

- [ ] **Step 1:** 写 `source-form.test.tsx` 断言keep/replace/clear不提交掩码，revision结果晚到不能覆盖新通道；`source-credentials.test.tsx` 断言隐藏、切页、退出/撤权清空明文，普通接口/缓存无明文；`source-import.test.tsx` 断言预览无副作用、失败/冲突/skip逐行明确。
- [ ] **Step 2:** `dev.sh web test` 红灯；e2e在真实API/PG上暂不能创建草稿/测试/应用而失败，不Mock业务完成。
- [ ] **Step 3:** 通道页增加源摘要/分项状态与按权限显示配置入口；配置页真实八列字段（编号只读），主/子路径直接填`/main`、身份意图、草稿历史/测试结果/应用/清空、池和普通录像开关。保存、测试、应用为不同动作；异步按真实job呈现，sub失败明确降级，continuous无池提示先添加或改选关闭。组件row key只用channelUUID，字段可按revision更新，任务notice不能随version remount丢失。
- [ ] **Step 4:** 增加导入模板下载、文件预览/选择/映射/密码意图/是否导入名称、测试/应用/取消/失败重试、管理员明文JSON导出；导出用局部Blob立即revokeURL且无服务端公开文件。界面显示M1无自动清理及录像元数据可查、播放M1-C待交付。真实浏览器测试正常换源、回滚、无录制取流、权限拒绝、明文清除、晚响应、导出回导；`web api:generate/build/lint/test`通过且类型无漂移。
- [ ] **Step 5:** 提交 `feat: expose channel sources imports and recording controls`。

## Task 10: CI 联合验收、部署与最终评审

**Files:** Modify CI、test-media.sh、media-test Compose、production README、镜像/录像规则状态说明、`docs/M1-B-validation.md`；Create `docs/M1-B-decisions.md`。保留M1-A所有CI job，原功能按变化运行适当回归。

**Interfaces:** `test-media.sh --contract/--probe/--publish/--switch/--recovery/--acceptance` 用唯一测试项目/私有临时状态，无生产.env或连接；测试退出仅清理自己资源。CI产物仅脱敏版本/计数/状态，禁止原始配置、credentials、trace、HAR、Hook原文或含媒体凭据的上游日志上传。

- [ ] **Step 1:** 先列出自动验收矩阵及预计失败断言：两路实际RTSP→取流→60秒正式MP4→standard ready索引；换源/主失败/子降级/回滚失败、改池和迟到尾片、重复Hook/DB停机spool、API/Worker分别与共同重启、发布移动前后崩溃、ZLM重启新run、跨32槽位映射而只用两路媒体样本。
- [ ] **Step 2:** 运行 `test-media.sh --acceptance`，对未完成场景保持非零失败；不能将未测硬件/16–32实际视频流容量或真实NVR验收当作CI通过。
- [ ] **Step 3:** 加入TLS实际证书更新期间两路片持续ready和同一ZLM容器ID；可选模块停用/不可达不影响基础录制；privateHook/原生MP4/未经授权RTC不能从网关或公开端口读取。test fixtures 含一条拒绝重定向/虚构凭据回显检查。浏览器跑真实源表单及导入流程，设备密码只来自隔离fixture、不使用用户真机配置。所有故障注入有恢复步骤和脱敏结果，不解析单纯日志“启动成功”来放行。
- [ ] **Step 4:** 核对精确待发布commit的Go单元/race、真实PG集成、前端类型/build/lint/浏览器、Linuxamd64镜像、实际ZLM合同与联合CI；更新证据及32槽位是设计规模说明。按已选择的执行方式做一次独立整分支评审，修复重要问题并验证，不自行开始第二次全量评审。M1-A已延期两处minor（users/pools remount反馈、时区搜索预览）继续有明确记录，不声称此次解决。
- [ ] **Step 5:** 创建并附加M1-B草稿PR（若M1-A仍未合并，明确以M1-A分支为base，依赖关系和仅M1-B差异）；推送开发分支，不合并main、不操作用户真机。交付CI/验证记录、部署新增参数和M1-C下一步，完成后清理该计划专用临时执行文件，不动私有M0附件或持续状态。

## 作者自检（2026-10-04）

- Spec覆盖：凭据/授权→Task2/9；源身份/测试/切换→Task1/3/6；批量导入→Task8/9；池/录像/run/inbox/spool/发布→Task4/5/7；部署固定镜像/实际验收→Task3/10。WebRTC/Range/SSE明确属于M1-C，智能/云归档未扩进M1-B。
- 接口与依赖：先定义共享DTO/迁移，再凭据与媒体适配、Hook与发布，再录制原语/切换、持续对账，再导入与前端；无channel↔recording循环依赖。所有跨任务引用类型已在本文定义；现有签名沿用现有代码而非M1-A旧计划拟议名称。
- 持久副作用：普通job有限尝试与域变更恢复分开，落实M1-A已经发现的“耗尽重试留下applying”风险；源测试仍有限失败，不无限等待用户发布。
- Review Focus五类各有明确测试；首次池/源闭环、未知码率、旧尾片/时区冻结、撤权/TTL、相同配置只更名分别有责任任务。
- 验证边界：本次仅计划文档检查，未运行/宣称新产品测试；Docker验收走GitHub CI、两路fixture，无16/32容量认证。实现前需用户评审本书面计划，延续已选择的原生执行方式。

## 上游核对资料

2026-10-04查阅官方 [ZLM REST API](https://docs.zlmediakit.com/guide/media_server/restful_api.html) 与 [Web Hook](https://docs.zlmediakit.com/guide/media_server/web_hook_api.html)，用来定位可用集成原语；这些在线文档不是锁定镜像的行为证明。Task3记录固定digest实际结果后才能放行；沿用 `docs/image-versions.md` 的镜像值，不重新使用浮动master镜像。
