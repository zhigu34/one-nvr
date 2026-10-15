# M3 智能与事件批次 4：通知（EVT-05）与抓拍归档

**Goal:** 收口 M3 里程碑——补齐里程碑行点名、而批次 1–3 都留空的最后两项：**通知（EVT-05）**与**抓拍归档**。做完这两项，M3 的「区域编辑、规则、消息归一、事件处理、通知、抓拍归档」六项齐备，「事件到录像和证据闭环」可整体验收。

## 为什么是这两项

批次 1 的计划写「通知与 Webhook（EVT-05）」留后、批次 3 写「通知留批次 4」，而批次 4 的计划文件一直不存在。抓拍归档则在 PRD 两处点名为 P0：M3 里程碑行，以及 STO-07「Frigate 自身配置/缓存/短期抓拍目录保持独立，由 Worker 归档至业务存储池」。当前 `internal/events/service.go` 的注释明写 `the bytes never touch one-nvr storage`——抓拍只做 HTTP 代理，从不落地，Frigate 一旦清理就永久丢失。

**同时关闭一个已经可见的空承诺**：规则编辑器现在就写着「冷却只限制提醒频率，不丢事件」，`event_rule_hits.notified_at` 列也已建好——但 `notified_at` **从未被写入**，`cooldown_seconds` 只做范围校验、从不执行（全库无读取点）。按 PRD AI-05，冷却的定义就是「限制通知频率」，没有通知它是个空转参数。本批让它名副其实。

## 范围

### 1. 通知渠道（EVT-05）

**两类渠道，语义不同，不混为一谈：**

| 类型 | 投递 | 失败语义 |
| --- | --- | --- |
| 站内告警 | 落库即达（写入 `notifications` 行，事件页/顶栏可读） | 无网络失败；仅记录产生 |
| 通用 Webhook | HTTP POST，带重试 | 有 发送中/成功/失败/已重试/最终失败 五态 |

**站点级配置**（管理员）：webhook 名称、URL、签名密钥（**write-only，复用 `secrets.SealCredential`，AAD 绑定 `siteID/channelID/channelID/"secret"`，与 `archive_targets` 密码同范式**）、启用开关、版本。URL 校验沿用 `archive.NewClient` 的 http/https 结构性检查（**并显式记录：本批不做出网地址白名单，与归档目标现状一致；如需限制，另开批次**）。

**规则到渠道的绑定**：PRD AI-04 把「动作」列为规则条件之一，所以绑定挂在规则上而非站点默认——新表 `rule_notification_channels(rule_id, channel_id)`。规则不绑任何渠道时不发外部通知，**命中照常记录**（命中与通知是两件事，AI-05 的「不因冷却丢弃事件记录」同理）。

**签名与去重**：每个 webhook 渠道一个签名密钥；投递头携带
- `X-OneNVR-Delivery: <delivery_id>`（稳定投递 ID，重试不变——接收端据此去重）
- `X-OneNVR-Signature: sha256=<HMAC-SHA256(secret, raw_body)>`

PRD 明确「接收端应据此去重」，所以 delivery_id 在首次入队时生成并**在所有重试中保持同一值**，不随尝试次数变化。

### 2. 投递状态机与重试

**不新造重试机制，复用 `internal/jobs`**：它自带 `job_attempts` 尝试账本（每次运行/结果/错误码）、指数退避与 fencing token。新增 job kind `notification.webhook`。

**注意一处需要动基座的地方**：`jobs.Enqueue` 目前**不接受** `max_attempts`（只收 kind/object_id/idempotency_key/payload），而表默认是 6，PRD 要求 webhook「默认最多 5 次退避重试」。所以本批给 `jobs.Input` 加一个可选 `MaxAttempts *int`（缺省沿用 6，不改变任何既有 job 的行为），而不是在通知侧另写一套计数——尝试次数继续只有 `job_attempts` 一个事实来源。

对外状态映射（PRD 措辞 → 内部值）：

| PRD 措辞 | 内部 state | 含义 |
| --- | --- | --- |
| 发送中 | `sending` | 已入队或正在尝试 |
| 失败 | `failed` | 本次尝试失败，仍会重试 |
| 已重试 | `retrying` | 已失败至少一次且仍在重试窗口 |
| 成功 | `succeeded` | 2xx |
| 最终失败 | `exhausted` | 退避耗尽或永久失败（4xx 不重试） |

**`job_attempts` 是尝试次数的唯一事实来源**，`notification_deliveries` 只存当前状态与外因（HTTP 状态码/网络原因），两处不一致时以 jobs 为准——不并行维护两套计数。

**失败隔离**（PRD 硬要求「通知失败不影响事件保存和录像」）：通知入队发生在**事件入库的同一事务**里（与 `evaluateRules` 同事务，保证命中一定有对应通知任务），但**执行**全部在 worker 的 job 循环里。Webhook 超时/拒绝/宕机只改 `notification_deliveries` 与 job 行，不回写事件、不阻塞入库。

### 3. 冷却真正生效（AI-05）

冷却在**入队时**判定，作用于**通知频率**而非事件记录：
- 同一规则、同一通道，在 `cooldown_seconds` 内有已产生的通知 → **不再入队新的外部投递**，命中仍写入 `event_rule_hits`。
- 站内告警**不受冷却限制**（「不丢事件记录」：操作者在事件页必须看到每一次命中），冷却只压制外部 webhook 的重复投递。
- 判定用规则当前的 `cooldown_seconds`（不是命中时刻冻结值）——冷却是可调的操作偏好，不是历史事实。

**`notified_at` 的语义定死**：命中**产**了一条通知（入队成功）时写入；它表示「这条命中已通知」，不表示 webhook 已送达（送达看 `notification_deliveries.state`）。这是它与投递状态的分工，前端文案按此区分。

### 4. 抓拍归档

**取帧来源**：不新增 Frigate 卷挂载，沿用既有 `http://frigate:5000/api/events/<id>/snapshot.jpg`（`Snapshot()` 已在用）。归档 = worker 拉一次写进池，此后 `/events/{id}/snapshot` **优先服务本地副本**，本地缺失才回退代理——与录像 `/content` 的「本地优先、云端兜底」同构。

**落地路径**：通道绑定池的 `snapshots/` 保留目录下（`storage.marker` 已把 `snapshots` 列为保留分类，无需新约定）：

```
snapshots/<channel_no:02d>/<站点本地日期>/<event_id>.jpg
```

按通道号而非通道 ID（与录像命名一致、可读、换源不迁移）；日期按**站点时区**取，与录像一致。写入经 `os.Root`（逐段 O_NOFOLLOW），与所有池写入同一套安全边界。

**状态如实**：事件已有 `snapshot_state`（`pending`/`ready`/`gone`）。本批新增**本地副本维度**，与上游维度分开报：
- 上游 `ready` + 本地未归档 → 代理（现状行为不变）
- 上游 `ready` + 本地已归档 → 本地（Frigate 清理后仍可看）
- 上游 `gone` + 本地有副本 → **仍可看**（这正是归档的意义）
- 两者皆无 → 如实 `gone`，不伪装

**未绑定池的通道**：跳过归档并在事件详情如实标注原因（`pool_unbound`），不静默假装已归档。

**保留**：抓拍独立到期（PRD EVT-07「抓拍采用独立保留期」）。事件到期扫描（`SweepExpiry`）同时删除对应本地抓拍文件；受保护证据引用（与受保护分片时段重叠的事件）**例外保留**——与事件索引同一例外口径，不另造规则。

**失败隔离**：Frigate 不可达、抓拍已 404、池不可写——一律不阻塞事件入库、不影响录像；归档是补强，不是前置条件。

## 明确不做

- **邮件 / 浏览器系统通知 / 第三方渠道**（EVT-06，P1）。
- **短信、原生手机推送、自动开门**（PRD 明列为首版外）。
- **出网地址白名单 / SSRF 加固**：与本批之外的归档目标现状一致，若需要应覆盖两者而非只做通知。
- **通知模板与重发 UI**：本批只做状态可见与自动重试，不做手工重发按钮（留待需要时）。
- **抓拍的云端归档**（云副本）——本批只落到本地池。
- 越界/计数/人脸/车牌（P2）、实时目标框、误报自动调参。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0022_notifications.sql` | `notification_channels`（含签名密钥加密三列）、`notifications`（站内告警）、`notification_deliveries`（投递状态）、`rule_notification_channels`；`events` 补本地抓拍路径/归档状态列 |
| 基座 | `internal/jobs/repository.go` | `Input.MaxAttempts *int`（可选，缺省 6 不变），供 webhook 用 5 次 |
| 通知 | `internal/notify/{service,webhook,inapp}.go` | 渠道 CRUD、签名、投递状态机、冷却判定、入队 |
| 事件 | `internal/events/service.go`、`rules.go` | 命中后同事务入队通知；`Snapshot()` 改为本地优先；`SweepExpiry` 连带删本地抓拍 |
| 归档 | `internal/events/snapshot_archive.go`（或 `internal/recording`） | worker 循环：拉取 → 写池 → 登记路径 |
| 装配 | `internal/app/run.go`、`exportjobs.go` | 起 `notification.webhook` 消费循环与抓拍归档循环（随 `FrigateEnabled`） |
| HTTP/契约 | `internal/httpapi/notifications.go` + `m1b` + `api:generate` | 渠道 CRUD/测试、投递列表查询、站内告警读取/已读 |
| 前端 | `features/settings`（通知配置）、`features/events`（站内告警与投递状态） | 渠道管理 + 密钥 write-only；事件详情显示通知结果与抓拍来源 |
| 测试 | 见下 | 单测（签名/冷却/状态映射）、集成（入库即入队、重试到耗尽、失败隔离、抓拍本地优先、到期清理）、vitest | |

## 测试覆盖

- **单测**：HMAC 签名对 canonical body 稳定；冷却窗口边界（恰好等于/超过）；投递状态映射（含 4xx 永久失败 vs 5xx 可重试）；抓拍路径构造（通道号/站点时区日期）与越界拒绝。
- **集成**：
  - 事件入库 → 命中与通知任务**同事务**可见（断言事务性：回滚不留下孤儿任务）。
  - 冷却：窗口内第二次命中**记入 event_rule_hits 但不入队 webhook**；站内告警照常产生。
  - 重试到耗尽：假 webhook 服务持续 5xx，断言尝试次数、退避、最终 `exhausted`；**全程录像/事件不受影响**（失败隔离的正面证据）。
  - 4xx 立即终态，不耗满 5 次。
  - delivery_id 在全部重试中不变（接收端去重的依据）。
  - 抓拍：本地优先（Frigate 不可达仍可读）、上游 gone 但本地在时仍可看、未绑池如实报 `pool_unbound`、事件到期连带删文件、受保护例外保留。
- **前端**：渠道表单密钥不回显（write-only，与归档目标同范式）；投递状态图标与文案；冷却说明与实际行为一致。

## 验收对照

- **EVT-05**：站内告警与通用 Webhook ✓；五态投递 ✓；默认 5 次退避重试 ✓；稳定投递 ID 与签名 ✓；通知失败不影响事件保存与录像 ✓。
- **AI-05 的冷却**：默认 60 秒限制通知频率、不丢事件记录 ✓（本批首次真正执行）。
- **M3 里程碑**：抓拍归档 ✓（补齐里程碑行点名项）。
- **STO-07**：Frigate 短期抓拍由 Worker 归档至业务存储池 ✓。

## 风险与边界

- **站内告警的读语义**需要定一次：本批按「事件页 + 顶栏未读」实现，未读状态存服务端（多设备一致），**不新增一级菜单**（PRD 7.2 的一级入口列表不含通知）。
- **Webhook 目标不可验证**：与归档目标不同，webhook 无法在保存时做「写读删」探针。改为保存时发一次**测试投递**（管理员显式触发，结果如实显示状态码），不假装已验证可达。
- **抓拍归档的池选择**：跟随通道绑定的存储池；换池不迁移历史抓拍（与录像同口径：历史按原池检索）。
