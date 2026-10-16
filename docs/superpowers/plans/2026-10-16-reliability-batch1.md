# M4 可靠性批次 1：观测持久化、状态区间与故障归因（REL-01/02）

**Goal:** 把「当前状态」升级为「可复算的历史」。今天 one-nvr 只知道*现在*主/子流是否健康，不知道**过去一个月**它有多少时间可用、断过几次、为什么断——而 PRD §6.12（REL-01～06，整章 P0）与 M4 出口条件「可用性/完整性记录能复算」要求的正是后者。

本批交付 REL-01 与 REL-02：**观测的持久化与分层保留**、**状态区间（可用/不可用/未知）**、**故障事件与归因**。指标口径（REL-03/04）、页面（REL-05）、保留策略的完整矩阵（REL-06）留批次 2/3——但没有本批的区间与故障记录，那两批无从计算。

## 先看清现状：已有的与缺的

**已有（可复用，不重造）**

| 能力 | 位置 | 说明 |
| --- | --- | --- |
| 采样循环 | `recording/monitor.go`，**每 10 秒**一轮、4 个并发 worker | 比 PRD 要求的 15 秒更密，无需改频率 |
| 采样内容 | `Reconcile` → `s.Media.Inspect`（ZLM `getMediaList`） | 得到 `Tracks[]`（含 `frames`/`ready`）、`Recording`、`BytesPerSecond`、`ObservedAt` |
| 主/子流分别观测 | `runtime_source.go`（main/sub）、`scheduler.go`（recording） | 三种 kind 已在写 |
| 视频进展判定 | `runtimeFrameStatus`（帧数/字节是否推进） | **已经不只是 TCP 可达**，符合 REL-01 的反面要求 |
| 观测行 | `source_observations`（kind/state/reason/evidence/observed_at/expires_at） | 30 秒 TTL，只服务「当前状态」 |
| 缺口记录 | `recording_gaps`（start/end/reason/evidence/run/switch） | 有去重，**是证据**（REL 的原料，勿删） |
| ZLM 回调 | `recording/hook.go`（`on_record_mp4`/`on_publish`/`on_play`/`on_stream_none_reader`/`on_http_access`） | 已消费 |
| 分片证据 | `recording_segments.media_evidence`（编码/分辨率/帧率/时长，ffprobe 采集） | REL-03 的直接输入 |
| 来源身份 | `source_identities`、`source_revisions`、`stream_sessions.generation` | REL-04 的 `source_id` 下钻基础 |

**缺（本批要建）**

1. **没有任何持久化的状态历史**。`source_observations` 30 秒就过期、且我们刚加的清理会删掉超过 24 小时的行——**今天查不到昨天的可用性**。REL-04 要的所有率值都需要它。
2. **没有状态区间**。「可用 40 分钟 + 不可用 10 分钟 + 未知 10 分钟」这种 `[start,end)` 区间是三层数据的第一层，目前只有逐条瞬时观测。
3. **没有故障事件**。断流、认证失败、无视频进展、录像停滞、存储不可写、ZLM 中断、监测中断、源切换——这些都只作为**状态行**存在，没有「一次故障从何时到何时、什么原因、是否恢复、是否持续中」的记录。REL-02 整条、DASH-03、EVT-04 都建立在其上。
4. **没有归因**。`reason_code` 是扁平字符串（`source_unavailable`、`sub_source_unavailable`……），没有 PRD 要求的八大类归因（上游源/链路、认证/配置、录制服务、存储目录、检测服务、监测未知、计划维护/切换、原因未知），也没有「管理员补充确认原因 + 保留原始分类 + 修改审计」。
5. **没有监测中断的记录**。Worker 停止或采样断档后，最后状态会被无限延伸当作在线——PRD 明确禁止（「重启时先保存中断区间再恢复采样」）。
6. **没有根事件关联**。ZLM 挂掉影响 8 个通道时，今天会得到 8 条独立故障；PRD 要求「同时影响多通道的组件故障关联同一个根事件，业务影响区间按集合并集计算」。
7. **没有计划维护登记**。PRD 要求「预先登记的计划维护单独统计」，不能与真实故障混在一起拉低可用率。
8. **没有连续异常计数与防抖**。PRD 要求「默认连续 3 次异常触发通知，可配置」且「原始异常观测与状态区间仍保留，不因防抖忽略短暂中断」。

## 范围

### 1. 观测持久化与状态区间（REL-01 的存储面）

**新增 `reliability_intervals`（状态区间）作为第一层事实**：区间由**观测流的变化点**产生，而非按固定窗口切分。

```
reliability_intervals(
  id, channel_id, source_revision_id, session_id,
  kind,                  -- main | sub | recording
  state,                 -- healthy | unavailable | unknown | excluded
  reason_code,           -- 扁平原因（保留现有词汇，见 §4 归因）
  exclusion,             -- null | disabled | not_configured | maintenance | planned
  started_at, ended_at,  -- 半开区间 [start,end)，ended_at 为空表示持续中
  observed_count,        -- 这个区间由多少次采样确认（防抖与置信度用）
  first_observed_at,     -- 区间内首次观测
  last_observed_at,      -- 区间内最后一次观测（判断是否被监测中断切断）
  created_at, updated_at
)
```

**为什么用区间而不是保留全部原始观测**：REL-06 要求「原始观测默认保留 7 天」，但 32 路 × 3 kind × 每 10 秒 = 每天约 83 万条原始观测——30 天就是 2500 万条，而查询只需要「区间 + 计数」。因此：

- **原始观测行**（复用现有 `source_observations`，但去掉 30 秒 TTL 语义、改为按 7 天保留）→ 支撑「下钻到原始证据」
- **状态区间**（新表）→ 支撑所有率值计算与页面时间带

两者的关系：**区间是原始观测的增量折叠**，不是一个独立的真相来源。折叠规则：新观测与当前区间同 state/reason 则只更新 `ended_at`/`observed_count`/`last_observed_at`；不同则闭合旧区间、开新区间。这样**同一份观测既可用于下钻，也可用于汇总，且两者永远一致**。

**监测未知也是区间**：PRD 要求「超过两个采样周期没有有效观测后，不继续把最后状态延伸为在线」。实现上，采样每轮结束时写入一个「心跳」时间戳（内存 + 表）；区间折叠时若发现 `last_observed_at` 距当前超过 2 个采样周期，**先闭合当前区间为那一时刻**，再开一个 `state='unknown', reason='monitoring_gap'` 的区间。这样**未知时段是被显式记录的，不是被推断填满的**。

**重启时保存中断区间**：Worker 启动时，把所有 `ended_at IS NULL` 的区间按 `last_observed_at` 闭合，并插入一条 `monitoring_gap` 区间——这正是 PRD「重启时先保存中断区间再恢复采样」的字面要求。**这一步必须在开始采样之前完成**，否则新观测会与陈旧的开放区间混在一起。

**ZLM 回调的用途**：回调比 10 秒轮询更及时，因此回调到达时**立即触发该通道的一次采样**（而非等到下一轮），让状态边界更贴近真实。回调不直接写区间——保持「区间只由观测产生」这一个写入口，避免两条路径产生不一致。

### 2. 故障事件与归因（REL-02）

**新增 `fault_events`（故障事件）**：

```
fault_events(
  id, channel_id,
  source_revision_id, session_id,   -- 归属：发生时的来源
  kind,                             -- 与区间对应：main|sub|recording|storage|zlm|monitoring|switch
  state,                            -- open | resolved
  started_at, ended_at, duration_ms,  -- 持续中则 ended_at 为空
  initial_reason,                   -- 故障发生时的自动归因
  confirmed_reason,                 -- 管理员确认后的归因（不改写 initial）
  category,                         -- 八大类之一（见下）
  root_event_id,                    -- 组件级根事件（多通道共因）
  recovery,                         -- auto | manual | null（未恢复）
  note,                             -- 管理员备注
  evidence,                         -- jsonb：观测证据快照
  category_revisions,               -- jsonb 数组：归因修订历史（保版本）
  created_at, updated_at
)
```

**八大类归因**（PRD 原文枚举，不用自造词汇）：

| category | 含义 | 典型 reason_code |
| --- | --- | --- |
| `upstream_unavailable` | 上游源/链路不可用 | `source_unavailable`、`sub_source_unavailable` |
| `auth_or_config` | 认证/配置 | `authentication_failed`、`credentials_rejected` |
| `recording_service` | 录制服务 | `recording_stalled`、`zlm_unavailable` |
| `storage_directory` | 存储目录 | `pool_unavailable`、`low_space`、`pool_unwritable` |
| `detection_service` | 检测服务 | `frigate_unavailable`、`mqtt_unavailable` |
| `monitoring_unknown` | 监测未知 | `monitoring_gap`、`observation_stale` |
| `maintenance_or_switch` | 计划维护/切换 | `source_switch`、`maintenance` |
| `unknown` | 原因未知 | 兜底 |

**关键约束（PRD 明令，别自己加戏）**：超时或断流**只证明「源/链路不可用」**，不得直接断言摄像头硬件损坏——`upstream_unavailable` 就是这一条的落地，绝不细分成「摄像头故障」。细分归因要么有证据、要么人工确认。

**同一连续故障更新原事件**：与区间折叠同样的思路——故障由区间状态转变驱动。`healthy → unavailable` 开一条 open 故障；`unavailable → healthy` 闭合它并记 `recovery`。重复的采样或回调**只更新证据与时长，不新增事件**（幂等的栅栏：同一 channel+kind 同时只有一条 open 故障，用部分唯一索引保证）。

**根事件关联**：当多个通道在同一时刻（同一采样轮内）出现同类故障、且原因指向同一个组件（`zlm_unavailable`/`storage pool` 等）时，创建一条 `root_event_id` 指向的组件级根事件，各通道故障挂在其下。**业务影响区间按集合并集计算**——即 8 个通道同时断流 5 分钟，总影响是 5 分钟而不是 40 分钟。

**归因修订可审计**：管理员可以设 `confirmed_reason` 与备注；`initial_reason`/原分类**永不改写**，修订追加进 `category_revisions`。**不能静默提高可用率**——修订后的统计用修订值计算，但原始值在记录里可查（REL-04 的统计版本要求）。

**计划维护登记**：新增 `maintenance_windows(channel_id?, starts_at, ends_at, note, created_by)`——管理员预先登记。落在窗口内的故障与不可用区间标 `exclusion='maintenance'`，**单独统计**、不计入非计划中断。**事后补登记要留痕**（记录登记时间），避免「事后把故障登记成维护」静默改善数字。

### 3. 连续异常计数与防抖（REL-01 后半）

PRD：「默认连续 3 次异常触发通知，可配置；原始异常观测和状态区间仍保留，不因告警防抖忽略短暂中断」。

实现：**防抖只影响通知，不影响记录**。

- 区间与故障**在第一次异常就记录**（`observed_count=1` 起步）——短暂中断完整可见
- 通知在 `observed_count >= consecutive_threshold`（默认 3，站点级可配）时才发出
- 阈值与采样实际间隔一起写进通知正文（「连续 3 次、间隔 10 秒」），不谎称精确知道故障起始秒

**采样边界误差必须在数据里可见**：轮询推断的状态边界保留 `last_observed_at`（最后已知正常的观测）与 `first_observed_at`（首次确认异常），区间本身用这两者表达「故障发生在两者之间」，**不用区间起点谎称精确到秒**。REL-04 返回率值时必须同时给出这个不确定量。

### 4. 监控中断与持久化（REL-01/06 的交界）

PRD 要求「Worker/宿主机停止或超过两个采样周期没有有效观测后标为监测未知」与「数据库短时不可用时，可持久缓冲到独立有容量上限的本地任务日志，恢复后幂等补写」。

**分两半，本批做前半**：

- **监测未知**（本批）：如上 §1，显式开 `monitoring_gap` 区间。这是「不填满停机时间」的落地。
- **本地缓冲补写**（本批做到「不阻塞 + 有界 + 幂等」，完整恢复在批次 2）：采样写库失败时**不得阻塞录制路径**——现有的采样已经有 25 秒超时与 4 个 worker，但数据库长时间不可用会积压。做法：写入失败时把观测追加到**有容量上限的本地 spool**（复用 `recording/spool.go` 的既有形态：有界、原子追加、可重放），恢复后幂等补写（幂等键 = 观测自身的 id）。**缓冲耗尽时明确记录未知区间**，而不是静默丢弃。

**注意一处既有风险**：`monitor.go` 目前每轮会 `DELETE FROM recording_bitrate_samples WHERE observed_at < now() - 10 minutes`，且**数据库错误会直接 return 退出整个 Monitor 循环**（第 79-81 行 `return err`）——数据库抖一下采样就停了。本批要把这处改成「记日志 + 继续」，否则监测中断比预想的频繁得多。

### 5. 保留策略（REL-06 的第一阶段）

PRD 的分层：

| 数据 | 默认保留 |
| --- | --- |
| 原始观测 | 7 天 |
| 分钟汇总 | 30 天 |
| 状态/故障/应录/缺口/生成校验证据 | 365 天 |
| 按日汇总 | 730 天 |

**本批做前两列与第三列的状态/故障部分**（应录与生成校验证据属批次 2/3）：观测 7 天、区间 365 天、故障 365 天、分钟/日汇总留批次 2（先把区间存对，汇总才有意义）。保留时长**站点级可配**，且 PRD 要求「归档/录像保留长于默认时提示调整相关证据保留策略」——这条在批次 3 的页面上提示。

**所有保留清理沿用现有 `RunRetention` 的分批形态**（bounded batch，避免首次启用时一次 DELETE 锁住整表）——这是本仓库已验证的做法（`SweepObservations`、`SweepRetention`）。

## 明确不做（批次 2/3）

- **应录区间与录像完整性证据**（REL-03）：需要**策略版本历史**（今天 `recording_policies` 只有当前值，`source_switches` 记了 `old_mode`/`desired_mode` 但不构成完整时间线），是独立的一块。
- **指标口径与 CSV**（REL-04/05 的导出）：建立在本批的区间与故障之上。
- **可靠性页面**（REL-05）：通道页签、运维比较、趋势图、时间带、CSV。
- **分钟/日汇总表**（REL-06 的后两列）：先把区间与故障存对。
- **告警阈值配置 UI**（REL-05 后半）：本批只做连续异常计数，阈值配置界面留批次 3。
- **HW-03/04 的检测容量报告**：它与本批共用采样，但属另一章，单独批次。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0024_reliability.sql` | `reliability_intervals`、`fault_events`、`maintenance_windows`；`source_observations` 去掉 30 秒 TTL 语义（改为按 7 天保留）；索引 |
| 区间 | `internal/reliability/intervals.go` | 观测 → 区间的增量折叠；采样断档检测；重启时闭合开放区间 |
| 故障 | `internal/reliability/faults.go` | 区间状态转变 → 故障开闭；八大类归因映射；根事件关联与并集计算；归因修订 |
| 维护 | `internal/reliability/maintenance.go` | 计划维护窗口 CRUD 与命中判定 |
| 采样 | `internal/recording/monitor.go`、`scheduler.go`、`observations.go` | 采样后调用区间折叠；回调解锁即时采样；**修掉「数据库错误退出整个循环」** |
| 缓冲 | `internal/recording/spool.go`（复用） | 采样写入失败的本地缓冲与幂等补写 |
| 保积 | `internal/reliability/retention.go` | 观测 7 天 / 区间与故障 365 天，分批清理，挂进既有 `RunRetention` |
| 装配 | `internal/app/run.go` | worker 起 reliability 服务与清理循环；站点级保留配置读取 |
| HTTP/契约 | `internal/httpapi/reliability.go` + `m1b` + `api:generate` | 区间查询、故障列表/详情/确认原因/备注、维护窗口 CRUD（页面在批次 3 消费） |
| 测试 | 见下 | |

## 测试覆盖

- **单测**：区间折叠（同 state 续期、变 state 开新区间、`[start,end)` 半开边界）、归因映射（每个 reason_code → category，含未知兜底）、防抖计数（第 1 次就记录、第 3 次才通知）、并集计算（多通道同因故障不重复计时长）。
- **集成**：
  - **观测 → 区间 → 故障的完整链路**：注入一次断流（ZLM 返回 stream absent）→ 断言开了 `unavailable` 区间与 open 故障；恢复 → 断言区间闭合、故障 `resolved` 且 `recovery=auto`。
  - **同一连续故障不新增事件**：连续 10 轮异常采样 → 只有 1 条故障，`observed_count`/时长增长。
  - **监测未知被显式记录**：停掉采样超过 2 个周期 → 断言区间在 `last_observed_at` 处闭合、随后是 `monitoring_gap`；**开机后不会把这段算成在线**。
  - **重启保存中断区间**：构造开放区间 → 重启服务 → 断言开放区间被闭合为 `monitoring_gap`。
  - **根事件与并集**：两个通道因同一 ZLM 故障同时断流 → 断言 1 条根事件、2 条通道故障、并集时长（非求和）。
  - **归因修订不改写原始值**：确认原因后断言 `initial_reason` 未变、`category_revisions` 追加一条。
  - **计划维护单独统计**：维护窗口内的故障标 `exclusion='maintenance'`，不计入非计划中断。
  - **保留分层**：超过 7 天的观测被清理、区间与故障保留；跨保留范围查询时返回的精度标注正确。
  - **数据库错误不中断采样**：让写入失败一次 → 断言采样循环继续、观测进入 spool、恢复后补写且不重复。
- **前端**：本批不做页面，但契约变更要让 `api:generate` 与 tsc 通过。

## 验收对照

- **REL-01**：独立于浏览器的持续采样 ✓（已有，本批补齐持久化）；主/子流分别记录 ✓；无视频进展不算在线 ✓（已有 `runtimeFrameStatus`）；**监测未知显式记录** ✓；**重启保存中断区间** ✓；防抖不影响记录 ✓。
- **REL-02**：四类故障形成事件与区间 ✓（断流/认证/无进展/录像停滞/存储/ZLM/监测/切换）；八大类归因 ✓；**不断言硬件损坏** ✓；同一连续故障更新原事件 ✓；根事件与并集 ✓；归因修订可审计 ✓；计划维护单列 ✓。
- **REL-06**（部分）：观测 7 天 / 区间与故障 365 天 ✓；有界批量写入 ✓；本地缓冲与幂等补写 ✓；数据库故障不阻塞录制 ✓。
- **DASH-03 / EVT-04 的基础**：故障事件表建立后，批次 2 的事件中心接入与总览告警才有数据源。

## 风险与边界

- **区间表会随时间增长**：32 路 × 3 kind，若每次状态变化都开区间，正常站点每天约几十到几百条；故障多的站点更多。365 天保留下量级可控（百万级），但仍需 `(channel_id, kind, started_at)` 索引与分批清理。
- **`source_observations` 的 TTL 语义变更**要小心：它今天被 `statusTx` 用来判定「观测过期」（`expires_at`）；去掉 30 秒 TTL 会让「当前状态」读旧数据。**做法是保留 `expires_at` 列与判定逻辑，只把清理窗口从 24 小时改为 7 天**——TTL 是「这份状态何时算过期」，保留窗口是「这行还能保留多久」，两者本是不同的东西，今天被混在一个字段里。
- **根事件判定是启发式的**：同轮同类故障是否同因，靠「同时刻 + 同 reason_code 指向同组件」判断，可能误合并。因此根事件**只做聚合展示与并集计算，不改变各通道故障自身的记录**——即使归错，单通道数据仍然正确。
- **本批不产生任何率值**：REL-04 的指标计算在批次 2。本批先让**事实**完整且可复算，指标只是它们的投影。
