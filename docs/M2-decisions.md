# M2 录像生命周期决策（批次 1：文件管理 + 保留策略 + 删除）

本文件同时是本批次的计划记录：范围是补齐 M2 出口条件的前半段——「录像从生成到**本地删除**全生命周期可验证」。

## 本批交付

- **录像一级入口**：侧边栏「录像」（`/recordings`）下分「回放 / 录像管理」两个页签，切页签保留通道与时间上下文；深链接与即时回放入口仍走同一路由（`tab` 参数选择页签）。
- **录像管理页**：按通道、时间范围、来源版本、存储池、文件健康、保护状态筛选；摘要（片段数 / 可播放时长 / 逻辑大小 / 已删除释放）；表格 + 选中集 + 批量删除；逐行操作（下载、保护/解除）。
- **保留策略**：站点级 `recording_retention_days`，**默认 0 = 不自动删除**，管理员显式开启。
- **删除本地副本**：逐项结果、释放字节、审计一行/段。受保护、未完成、正在写入、文件已缺失、未知 ID 各自给出理由，互不掩盖。

## 关键决策

1. **保留默认关闭，不随升级生效。** 开启后自动删除既有录像是运维决策，不能由升级替操作者做出。需要保留期的站点自行在管理页设置；这同时使本批次对既有部署的默认行为零改动。
2. **删除顺序：先 unlink，后改状态。** 若两步之间崩溃，索引仍为 `ready` 而文件已不在，现有对账（`List`、池用量校验）会把它标为 `missing`。这正好满足 PRD FILE-04「系统到期删除与外部缺失不能混淆」：成功删除 → `deleted`，崩溃 → `missing`，不需要新的状态机。
3. **`deleted` 与 `missing` 严格区分。** `deleted` 只表示 one-nvr 主动删除了本地副本；文件自行消失一律记为 `missing`。删除后的回放按「不存在」处理：`GET /recordings/{id}/content` 返回 404 `not_found`，而不是 409「尚未完成发布」——后者会误导操作者等待一个永远不会回来的文件，也维持 M1-C「未授权与不存在不可区分」。
4. **保护（evidence protection）必须与删除同批交付。** 用户要求的三项里没有「保护」，但保留策略与手动删除都必须能跳过证据，否则自动清理会销毁操作者明确要留下的录像。本批只做最小形态：`protected` 列 + 跳过判定 + 管理员接口 + 列表开关。
5. **新鲜度延迟只约束保留策略。** 自动清理跳过发布后 10 分钟内的片段（避免与仍在落定的分片竞争）；管理员的显式删除作用于具名行，不受该延迟限制——`state='ready'` 本身已表示文件完整且无人写入。
6. **删除与保护仅管理员**（PRD §3）。通道授权决定谁可以看，不决定谁可以销毁证据。
7. **批量删除是同步端点**，不是后台任务：与既有 `PUT /recording-policies` 同形（逐行结果、部分成功、幂等键），unlink 本身很快，不需要作业轮询的复杂度。重复请求幂等：已删除项返回 `deleted` 而不是失败。
8. **管理列表按池分组校验**，不再逐段 `OpenMediaRoot`（每段一次 EvalSymlinks + 读标记）。`GET /recordings` 的分页上限为 500、默认 100（契约即此，见下文「2026-10-11 修复批次」：HTTP 层实际执行这一上限）。
9. **媒体参数复用发布证据**：`recording_segments.media_evidence` 在发布时已由 ffprobe 采集编码/分辨率/帧率，列表直接读它，零新增探测。零值表示该证据早于采集，不代表属性缺失。
10. **保留策略只作用于已启用的存储池。** 池被停用时通常意味着操作者正在处理它（排查、迁移、准备回收），自动清理不应在其间继续搬运文件；停用池的历史仍可回放，只是不参与自动到期删除。需要清理停用池时先启用它。
11. **池回收未解开**（记录在案，非本批）：`recording_runs` 是永久溯源记录、从不删除，其 pool 引用仍然保留池。删除位置行只是不再额外占用引用；真正「用过即不能删池」的约束来自 run 溯源，需要单独决策，本批不做。

## 明确不做（避免混入 P0 完成度）

- 云归档、仅云端回放、云端到期清理（M2.1）。
- 范围导出与导出抽屉（FILE-06 的导出部分）：依赖导出任务设计（EXP-01..05）。
- 容量压力淘汰、通道配额（STO-02/03、REC-07 的配额部分）：依赖归档前置条件（「仅清理已验证云副本的源」）。
- 播放租约（M1-C 已裁定推迟）：已知限制——保留清理可能删除正在播放的超期片段。POSIX fd 语义保证在途播放读完即止，但若此时又发起新请求会得到 404。
- 回放时间轴的源切换标记与按源筛选的**标记**部分（PLAY-10）：筛选已在本批交付（`source_revision_id` 参数 + 管理页筛选），时间轴标记留待与多路同步回放一起评估。

## 2026-10-11 修复批次（现场验收发现，回放索引与播放器）

真机验收暴露四类问题，一并修复；均不改变既有 API 形状与安全边界。

1. **回放索引静默截断 → 游标翻页加载。** 此前回放页单次请求 `limit=100` 且不翻页：服务端按 `start_at DESC` 返回，60 秒分片的 24 小时窗口约有 1440 段，时间轴只含最新约 100 分钟，未取回的部分被 `complement()` 画成「缺口」——「没取回来」与「没录像」在页面上不可区分。现在回放页经 `next_cursor` 翻页取全窗口（每页 500），加载上限 2000 段（约 33 小时）；触顶是显式警告（role=alert，注明已覆盖到的时间点并建议缩小范围），不再伪装成缺口。翻页期间时间轴逐步向过去扩展，播放中的片段始终保留在索引里，不因翻页中断。
2. **`GET /recordings` 的分页上限在 HTTP 层落地。** 契约（maximum 500 / default 100）与服务层校验早已写 500，但共享的 `pagination()` 仍按 100 拒绝——上条目 8 的「上限提到 500」实际未生效。现拆出 `paginationLimit(r, fallback, max)`：recordings 传 (100, 500)，其余端点维持 (50, 100) 不变。
3. **未播放时不显示伪位置。** IDLE 的 `clock=0` 此前被格式化成「当前位置 1970-01-01 08:00:00」（epoch 0 按站点时区），时间轴红线也被钳到窗口起点，读作真实位置。现在 `clock<=0` 一律显示 `—`，时间轴红线在首次播放前不渲染。
4. **底部文案改为「当前通道」。** 「正在回放 CHxx」只取侧边栏选中通道，与播放状态无关——零录像的通道也显示「正在回放」，与状态框矛盾。
5. **倍速跨片段保持。** 浏览器装载新资源会把 `playbackRate` 重置为 `defaultPlaybackRate`（1），控制器在 `load()` 前设置的倍速因此失效——自动续播、跳段、跨段定位全部回到 1x，而倍速下拉框仍显示原倍速。现在 `ready()`（`loadedmetadata`）在装载完成后重新应用；单测桩 `load()` 模拟该重置以钉住此行为。附带的空状态合并：时间轴组件不再渲染第二个空态框，由页级「该时间范围没有已登记录像」单独承担。

## 2026-10-11 导出与证据批次（M2 收尾）

计划与裁定见 `docs/superpowers/plans/2026-10-11-recording-export.md`。范围：EXP-01/02/03/05 + FILE-06 前半 + EXP-04 窗口保护。要点：

1. **任务模型复用 jobs 基座**：`recording_exports` 行是领域事实，执行用 `jobs(kind='recording.export')`，worker 两个消费循环 = PRD 的并发 2；崩溃重跑从零开始（`.work/exports/<id>` 重建），产物原子发布 + 单事务登记，无半成品可见。作业重试耗尽由 30 秒对账循环把行标 `failed: job_exhausted`。
2. **分析与执行共用同一纯函数**（`analyzeExportWindow`）：预览看到的分组/缺口就是任务将执行的计划；亚秒级分片接缝不算缺口（SeamTolerance 2s），换源重叠按最早起点取一份并计数，不重复拼接。
3. **只做原码拼接**（ffmpeg concat `-c copy`）：参数一致性以入库的 `media_evidence` 视频参数为准，不一致在创建时警告、执行时 `failed: media_incompatible`，不静默转码；产物完成后 ffprobe 校验（时长容差 max(5s, 10%)）。音频参数未按分片入库——同一连续 run 内一致，清单如实列源分片。精确裁剪留后续批次。
4. **临时引用保护（EXP-05）**：创建即 pin 源分片（`recording_export_sources`），`queued/running` 期间批量删除与保留策略都跳过（逐项理由 `pinned_by_export`）；终态自动失效。
5. **清单不落池**：ZIP 下载时由清单构建器现场生成（EXP-03 字段：任务、创建人反规范化、通道、请求/实际窗口、缺口、产物 SHA-256、pin 源分片），与产物字节不可能互相矛盾；SHA-256 完成时已算好入库。
6. **产物 7 天到期**：保留扫描无条件清到期产物（unlink → `expired`），受保护产物豁免（admin 开关，对齐分片保护语义）；下载遇 expired 按 404。
7. **权限**：analyze/create/list/下载走通道级 `auth.Export`；窗口保护（EXP-04，一次请求标记相交分片）与产物保护 admin-only。下载先审计后出字节。
8. **单次窗口 ≤30 分钟**服务端强制；更长范围等待批次内的自动拆分编排。容量闸两道：创建时新鲜 `free_bytes` 对比估算（×1.05），不足 409（`capacity_unknown` 前端可先检查存储池重试）；worker 写前复查。

## 2026-10-11 云归档批次 1（M2.1 第一段：WebDAV 目标与手动上传）

计划与裁定：`docs/superpowers/plans/2026-10-11-archive-batch1.md`。范围：ARC-01（配置+探针）、ARC-02（手动入队执行与验证）、ARC-03（远端对象键）、ARC-08 手动模式、FILE-02 位置维度。**未做**（批次 2/3）：定时/立即自动上传调度、本地安全清理与播放租约（ARC-05）、仅云端回放（ARC-04）、云端到期清理（ARC-09）、远端对账与缓存（ARC-07）、云源导出、容量压力淘汰与配额。

1. **上传权限单独授予**（ARC-08）：`channel_grants.archive` 新列，`auth.Archive` action 全链路（requireChannel/grants 读写/permissions 数组/契约枚举）；管理员默认拥有——迁移回填既有 admin 行，`site.setup`/`SetGrants` 写入路径为 admin 行强制 archive=true（防「初始化晚于迁移」的窗口）；操作员默认没有。
2. **凭据复用源凭据加密**：`secrets.SealCredential/OpenCredential` 以 (site, targetID, targetID, "password") 为 AAD；库中只存 key_id/nonce/ciphertext；目标删除仅在零副本引用时允许（ARC-06），有引用只能停用。
3. **远端对象键 = `<site_id>/recordings/<通道号>/<本地日期>/<与本地同名文件>`**：直接由本地 `target_relative_path` 推导（ARC-03：共用录像 ID 与冻结命名），客户端拼 base_url。
4. **探针全链**（ARC-01）：PUT→GET 全量比对→GET Range→DELETE，能力四项落 `last_check`；缺 DELETE 如实显示「到期清理不可用」。审计详情只记名称——safejson 消毒器拒绝 URI 形态的值与含 url 的键，端点只在目标表里。
5. **验证 = 存在 + Content-Range 总长一致**（ARC-02 底线）；SHA-256 在上传流式计算入库，远端哈希对账留批次 3。上传中（`uploading`）的源被删除保护拦截（理由 `archiving`），与导出 pin 对称；重试期错误码随行展示，任务走 jobs 退避。
6. **List 带 `archived`**：`publicationColumns` 加 EXISTS 子查询，管理页显示「本地+云端」。

## 2026-10-11 云归档批次 2（M2.1 第二段：归档策略与自动上传）

计划与裁定：`docs/superpowers/plans/2026-10-11-archive-batch2.md`。范围：ARC-08 完整（手动/定时/立即）+ ARC-05 的「发布时固定归档要求」。

1. **策略 = 通道级一行**（`archive_policies`：target + mode manual/scheduled/immediate + 每日时刻列表「HH:MM,HH:MM」站点时区）；切回手动不取消已提交任务（ARC-08）。
2. **立即上传挂在发布事务里**：`recording.Service.OnPublished` 可选钩子（worker 装配注入 archive 服务），copy 行与分片 ready 同事务落库；hook 失败只记日志、发布不受影响（ARC-06 故障隔离的落点）。
3. **定时调度以账本去重**：`archive_schedule_runs(policy, date, time)` UNIQUE 是多实例栅栏；每次触发只入队（≤100/次），积压由任务队列消化。入队内核（对象键/幂等键/copy 行）三路共用一个 `enqueueUpload`。
4. 实现教训：pgx 事务内外层 rows 未关就再查同一事务会「conn busy」——候选先缓冲再入队。

## 2026-10-11 云归档批次 3（M2.1 收尾：安全清理、租约、云端回放、到期）

计划与裁定：`docs/superpowers/plans/2026-10-12-archive-batch3.md`。M2.1 出口条件全部可验证。

1. **播放租约回归**（M1-C 推迟项）：`recording_playback_leases`，content 成功打开（本地或云端）即登记 90s，播放端每 30s 心跳续期（`POST /recordings/{id}/lease`，playback 权限）；删除/保留清理/云端到期三处都跳过活跃租约（理由 `lease_held`）。POSIX fd 语义保证在途播放读完；租约防的是「删完再发新请求」。
2. **本地安全清理（ARC-05）**：归档绑定通道走独立窗口（`sites.archive_local_retention_hours` 默认 48，0=永不），候选 = 窗口届满 + verified 未到期副本 + **远端此刻真实可达**（gate 钩子对副本做 0-0 读取确认，不可确认整轮停止而非猜测）；无绑定通道维持原 days 语义（候选查询排除归档通道）。保护/上传中/导出 pin/租约均拦截。
3. **仅云端回放（ARC-04）**：`/content` 本地失败（deleted/missing/damaged/pool 不可达）且存在 verified 副本时代理 WebDAV Range（206/416/ETag/审计与本地同语义）；deleted+无副本仍 404（M1-C 语义不变）；云端读取失败 503 `cloud_read_failed` 独立错误码，远端 404 将副本行对账为 expired（不动归档成功历史）。
4. **云端到期清理（ARC-09）**：`cloud_expires_at = 分片实际结束时间 + 目标保留天数`（创建时写入，迁移回填，目标天数调整时整批重算）；worker 每小时扫描过期 verified 副本 → DELETE 远端 → 行 `expired` + 审计（`archive.copy_expired`）；保护与活跃租约例外。**等待中的上传不补传已过期源**（`archive_expired_skip`，保护源例外）。
5. 副本状态 CHECK 扩展 `expired` 终态；Stats 加 `cloud_only`（本地已删、云端可播计数，FILE-01）。

## 2026-10-12 M3 批次 1：事件接收链路与事件中心

计划：`docs/superpowers/plans/2026-10-12-intelligence-batch1.md`。范围：EVT-01/02/03/07 主体 + CH-07 事件侧。

1. **MQTT 客户端自实现**（`internal/events/mqtt.go`，零新依赖）：照 `operations/probe.go` 的 3.1.1 帧格式扩展到 CONNECT/SUBSCRIBE/QoS-0 PUBLISH 接收/PINGREQ 保活；单主题 `frigate/events`、256KB 帧上限、断线指数退避重连。SUBACK 前到达的 PUBLISH 缓存不丢。
2. **归一与去重**（AI-05 落地侧）：上游事件 id 唯一（`UNIQUE(source,source_id)`）；「update」只做单调前进（结束时间取最新、分数取最大、抓拍 pending→ready），迟到的「new」不会重开已结束事件；**未映射摄像头记审计丢弃，不猜通道**（CH-07/CH-08）。
3. **流中断即记录**：订阅断开写 `event_stream_gaps`（含原因），恢复留痕——静默会被误读为「没有事件」（EVT-04 雏形）。
4. **抓拍不落盘**：`GET /events/{id}/snapshot` 代理 Frigate HTTP API（`http://frigate:5000`），未授权 404、未生成 409 `snapshot_pending`、远端 404 把 `snapshot_state` 对账为 gone；抓拍字节不进 one-nvr 存储。
5. **处理流程**（EVT-03）：`PATCH /events/{id}` 状态机 open/acknowledged/closed/false_positive，版本守卫（409 `event_version_conflict`），`event_handling` 记录操作者姓名/时间/备注；事件可重开。
6. **90 天到期**（EVT-07）：`expires_at = ended_at + 90 天`；未处理**不**延长期限；唯一例外是「与受保护分片时间重叠」（±回放窗口）；只清事件索引，不动录像/云对象/审计。
7. **权限**：列表/详情/抓拍/处理都走通道级 `playback` grant；无授权的行缺席而非报错；映射表 admin-only。
8. 前端新增「事件」一级菜单（监控组）：7 天窗口、通道/类别/处理状态筛选、抓拍卡片、详情抽屉（处理按钮+记录+「查看事件录像」深链带事件前 10 秒定位）。
