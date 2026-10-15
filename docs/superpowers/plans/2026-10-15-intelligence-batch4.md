# M3 智能与事件批次 4：通知变量层 + 渠道适配 + 抓拍归档

**状态：已交付（2026-10-16）**。九个提交，见 AGENTS.md 交接快照。

> 交付时的偏差记录（计划与实际不一致处）：
> 1. **渠道绑定挂在规则上**（计划原案），用户确认「按选择的方式提醒」后保持此设计；实测一条规则可绑多个渠道，空绑定 = 只记录不通知。
> 2. **类别选择器由单选改多选**是交付中发现的必要前提：后端 `categories` 一直是数组，前端却是单选，导致「人+车」要建两条规则。
> 3. **事件页降权**（用户口径「不需要处理，只要回看」）原计划未包含，交付时新增。
> 4. **测试走本机集成 + 现场部署**：用户决定不再跑 CI，因此验收依据是本机 `verify.sh` 与 `tests/integration`，不是 CI 全绿。


**Goal:** 收口 M3 里程碑。补齐批次 1–3 都留空的两项——**通知（EVT-05）**与**抓拍归档**。

**本批的核心不是「做一个 webhook」，而是先把通知变量定死**：事件命中规则时生成一份**渠道无关的通知内容**，之后任何渠道（站内、Webhook、邮件、企业微信…）都只是「把这份变量带出去发送」。这样后续加渠道**只新增一个适配器，不动事件、规则、通知核心**。

## 用户口径（2026-10-15，权威）

- **事件不做必须处理**：「我只要知道这个时间点有事件，然后去回看就行」。处理流程（确认/关闭/误报）是 PRD EVT-03 的 P0 且已交付，**保留但降权**——不删功能，把事件页重心从「处理」移到「何时·哪个通道 → 看回放」。
- **通知渠道要可扩展**：「先生成通知需要的变量，后续要加邮件、企业微信等通知渠道，只需要把变量带入渠道发送」。→ 本批交付**变量层 + 适配器机制 + Webhook 与站内两个首批渠道**；邮件/企业微信等后续批次只加适配器。

## 职责边界

one-nvr **不负责渲染各家的消息格式**。变量层提供结构化事实 + 一段通用文本摘要；渠道适配器决定怎么用（Webhook 发 JSON，邮件取 Title/Body 当主题正文，企业微信发 markdown）。**新增渠道不得要求改动 `internal/notify/message.go`**——这是本批的设计约束，也是验收点。

## 范围

### 1. 通知变量层（本批核心）

事件命中规则时，在**同一事务里**生成一份冻结的通知内容。字段是渠道无关的事实：

```
DeliveryID   稳定投递 ID（重试不变，接收端据此去重）
EventID      事件 ID
Site         站点名 + 站点时区
ChannelNo    通道号（不是 UUID——通知里说 CH03 才有人看得懂）
ChannelName  通道名
Category     person / car / …
Score        置信度
Zone         命中区域（可空）
RuleName     规则名
Severity     low / medium / high
StartedAt    事件开始（UTC + 站点本地格式化文本两种）
EndedAt      结束（进行中为空，不伪造）
SnapshotURL  抓拍地址（渠道可自取；本批起为本地副本优先）
ReplayURL    回放深链（跳到事件时刻）
Title/Body   通用文本摘要（供无法自定义渲染的渠道直接用）
```

**冻结语义沿用仓库既有范式**（`event_rule_hits.reason` 的注释：「Stored at match time so a later rule edit cannot rewrite history」）：变量在生成时固化，之后改规则名、改区域、改渠道都不改写已发出的通知。**这是通知与规则解耦的关键**。

### 2. 渠道适配器

```go
type Channel interface {
    Kind() string                                  // "inapp" / "webhook"
    Send(ctx context.Context, m Message) error     // 把变量带出去
}
```

**首批两个适配器**（PRD EVT-05 的「站内告警与通用 Webhook」）：

| 适配器 | 投递方式 | 配置 | 失败语义 |
| --- | --- | --- | --- |
| `inapp` | 落库即达（通知行本身） | 无需配置，默认启用 | 无网络失败 |
| `webhook` | HTTP POST JSON + 签名 | URL / 签名密钥 / 启用 | 走 jobs 退避重试，五态可见 |

**后续渠道只加适配器**（email / wecom / …），核心不动。计划里**不做**它们，但本轮要把接口与注册点做成加法——验收时以「新增一个假适配器不需要改核心」为证据。

**Webhook 投递**：POST JSON（变量原样序列化）+ 头部
- `X-OneNVR-Delivery: <DeliveryID>`（重试不变）
- `X-OneNVR-Signature: sha256=<HMAC-SHA256(密钥, raw_body)>`

密钥**write-only**，复用 `secrets.SealCredential`（AAD 绑定 site/channel/channel/"secret"），与 `archive_targets` 密码同范式；不回显、不进审计负载。

### 3. 渠道选择

规则已有完整的**事件选择能力**（区域、类别、布防时段、置信下限、级别），所以「通知哪些事件」由规则表达，「用什么方式」由规则绑定渠道表达：

- 新表 `rule_notification_channels(rule_id, channel_id)` ——一条规则可绑多个渠道。
- **不绑渠道的规则 = 只记录命中，不外发**（命中照旧写 `event_rule_hits`，AI-05 的「不丢事件记录」）。
- **想「所有事件都通知」= 建一条兜底规则**（目标出现 + 全部类别），不需要另造站点级默认开关。
  **这里有一处当前做不到，本批要补**：规则编辑器（`features/detection/rules.tsx`）的「目标类别」是**单选下拉**（`categories: [next || 'person']`），而后端早已支持数组（`len(in.Categories) <= 32`、`evaluateRules` 按 `containsString` 逐个匹配）。所以现在要「全部类别」得建 8 条规则——与「只要知道有事件」的诉求相悖。本批把编辑器改为**多选**（后端零改动、契约已是数组），使一条规则即可覆盖全部类别。

### 4. 站内告警与冷却

**站内告警不参与冷却**：你说了「只要知道这个时间点有事件」——那么每一次命中都必须在事件页可见，不能被冷却吃掉。冷却只压制**外部渠道**的重复投递。

冷却（本批首次真正执行，此前 `cooldown_seconds` 只做范围校验、全库无读取点）：
- 判定在**入队时**、作用于**外部渠道**：同一规则、同一通道，`cooldown_seconds` 内已有外发投递 → 不再入队新的外部投递；**站内告警与命中记录照常**。
- 用规则**当前**的 `cooldown_seconds`（不是冻结值）——冷却是可调的操作偏好，不是历史事实。

**`notified_at` 语义定死**：命中**产生**了通知（入队成功）时写入，表示「这条命中已通知」，**不表示已送达**（送达看 `notification_deliveries.state`）。前端文案按此区分，不把两者混说。

### 5. 重试与状态

复用 `internal/jobs`（自带 `job_attempts` 账本、退避、fencing token），新增 kind `notification.webhook`。

**需要动一处基座**：`jobs.Enqueue` 目前不接收 `max_attempts`（表默认 6），PRD 要求 webhook 5 次。给 `Input` 加可选 `MaxAttempts *int`（缺省 6，不改变任何既有 job 行为），而不是在通知侧另写计数——尝试次数继续只有 `job_attempts` 一个事实来源。

五态映射 PRD 措辞：发送中 `sending` / 失败 `failed` / 已重试 `retrying` / 成功 `succeeded` / 最终失败 `exhausted`（4xx 立即终态不重试，5xx 与网络错误重试）。

**失败隔离**（PRD 硬要求）:入队与事件入库**同事务**（命中必有对应任务），**执行全在 worker**。Webhook 超时/拒绝/宕机只改投递行与 job，不回写事件、不阻塞录像。

### 6. 抓拍归档

PRD 里程碑行与 STO-07 两处点名的 P0；现状只有 HTTP 代理（`the bytes never touch one-nvr storage`），Frigate 一清理就永久丢失。

- **取帧**：沿用既有 `http://frigate:5000/api/events/<id>/snapshot.jpg`，不新增卷挂载。
- **落地**：通道绑定池的 `snapshots/`（`storage.marker` 已列为保留分类）：
  `snapshots/<channel_no:02d>/<站点本地日期>/<event_id>.jpg`，经 `os.Root` 写入。
- **读取本地优先**：`/events/{id}/snapshot` 有本地副本就服务本地，缺失才回退代理——与录像 `/content` 同构。
- **状态如实**：上游 `ready`+本地无 → 代理；上游 `ready`+本地有 → 本地；**上游 `gone`+本地有 → 仍可看**（这正是归档的意义）；两者皆无 → `gone`，不伪装。
- **未绑池**：跳过并如实标注 `pool_unbound`，不假装已归档。
- **保留**：抓拍独立到期，随事件到期清理同时删本地文件；与受保护分片时段重叠的事件例外保留（与事件索引同一口径）。

### 7. 事件页降权（用户口径）

不改后端能力，只调整呈现重心：
- **回看入口前置**：卡片与抽屉里「查看事件录像」提到显眼位置（现在已经存在，但被处理按钮挤在 hover 里）。
- **处理动作收进抽屉**：卡片 hover 只留回看，确认/关闭/误报移入详情抽屉，不再占据卡片主视觉。
- **保留全部功能与筛选**：PRD EVT-03 的 P0 能力不动，测试只调整选择器位置。

## 明确不做

- **邮件 / 企业微信 / 浏览器通知适配器**（EVT-06 P1 及以后）——本批只把机制做成加法并留出注册点。
- **通知模板自定义 / 手工重发 UI**。
- **出网地址白名单**：与归档目标现状一致；要做应覆盖两者，另开批次。
- **抓拍的云端归档**（只落本地池）。
- 越界/计数/人脸/车牌（P2）、实时目标框、误报自动调参。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0022_notifications.sql` | `notification_channels`（含密钥加密三列）、`notifications`（含冻结变量 + 已读）、`notification_deliveries`、`rule_notification_channels`；`events` 补本地抓拍路径/归档状态列 |
| 变量层 | `internal/notify/message.go` | `Message` 结构 + 生成函数（从事件/规则/站点构造）；**渠道无关** |
| 适配器 | `internal/notify/channel.go`、`inapp.go`、`webhook.go` | `Channel` 接口 + 注册表 + 两个首批实现 |
| 基座 | `internal/jobs/repository.go` | `Input.MaxAttempts *int`（可选，缺省 6 不变） |
| 服务 | `internal/notify/service.go` | 渠道 CRUD、绑定、冷却判定、入队、投递状态 |
| 事件 | `internal/events/service.go`、`rules.go` | 命中后同事务生成通知并入队；`Snapshot()` 本地优先；`SweepExpiry` 连带删本地抓拍 |
| 归档 | `internal/events/snapshot_archive.go` | worker 循环：拉取 → 写池 → 登记 |
| 装配 | `internal/app/run.go`、`exportjobs.go` | `notification.webhook` 消费循环 + 抓拍归档循环（随 `FrigateEnabled`） |
| HTTP/契约 | `internal/httpapi/notifications.go` + `m1b` + `api:generate` | 渠道 CRUD + 测试投递、通知列表/已读 |
| 前端 | `features/settings`（通知渠道）、`features/events`（降权 + 通知状态）、`features/detection/rules.tsx`（类别改多选） | 渠道管理（密钥 write-only）；事件页回看前置、处理入抽屉；规则类别多选（后端已支持） |
| 测试 | 见下 | |

## 测试覆盖

- **单测**：`Message` 生成（字段完整、进行中事件 EndedAt 为空不伪造、站点本地时间格式）；HMAC 签名对 canonical body 稳定；冷却边界（恰好等于/超过）；状态映射（4xx 终态 vs 5xx 重试）；抓拍路径构造（通道号 + 站点时区）与越界拒绝。
- **集成**：
  - 入库即入队：事件与通知任务**同事务**（回滚不留孤儿任务）。
  - 冷却：窗口内第二次命中**记 hit 与站内通知、但不入队外部投递**。
  - 重试到耗尽：假 webhook 持续 5xx，断言尝试次数、退避、终态 `exhausted`；**全程录像与事件不受影响**（失败隔离的正面证据）。
  - 4xx 立即终态，不耗满 5 次；`DeliveryID` 全部重试不变。
  - **渠道可扩展性**（本批设计约束的验收）：注册一个测试用假适配器，断言**无需修改变量层或事件/规则代码**即可投递——以「假渠道收到完整 Message」为证据。
  - 抓拍：本地优先（Frigate 不可达仍可读）、上游 gone 但本地在仍可看、未绑池报 `pool_unbound`、到期连带删文件、受保护例外保留。
- **前端**：渠道密钥不回显；投递状态与 `notified_at` 的区分文案；事件页回看入口前置后处理功能仍可用。

## 验收对照

- **EVT-05**：站内告警 + 通用 Webhook ✓；五态 ✓；5 次退避重试 ✓；稳定投递 ID 与签名 ✓；通知失败不影响事件与录像 ✓。
- **AI-05 冷却**：首次真正执行，且不丢事件记录 ✓。
- **M3 里程碑**：抓拍归档 ✓（补齐点名项）。
- **STO-07**：Frigate 短抓拍由 Worker 归档至业务存储池 ✓。
- **用户口径**：事件页以「何时·哪通道 → 回看」为主 ✓；通知方式可选、渠道可扩展 ✓。

## 风险与边界

- **变量层是本批最不该返工的部分**，所以字段在动手前定死并写进本计划；后续渠道若需要变量层没有的字段，应评估「是渠道特有渲染」还是「变量层缺了事实」——前者渠道自己拼，后者才扩变量层。
- **Webhook 无法像 WebDAV 那样做写读删探针**：保存时改为管理员**显式触发一次测试投递**，如实显示状态码，不假装已验证可达。
- **站内未读存服务端**（多设备一致），**不新增一级菜单**（PRD 7.2 一级入口列表不含通知）。
- **抓拍跟随通道绑定池**；换池不迁移历史抓拍（与录像同口径）。
