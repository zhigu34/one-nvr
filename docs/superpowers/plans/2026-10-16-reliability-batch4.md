# M4 可靠性批次 4：呈现层与导出（REL-05）

续 `2026-10-16-reliability-batch3.md`。批次 1–3 把后端做齐了：事实（区间、故障）、应录与完整性、指标与汇总层，且在 NAS 上经过真实站点验收（抓出并修掉三个现场缺陷）。本批交付 **REL-05 的呈现层**——目前数据齐备但**没有任何界面**。

## 为什么要分四个子项而不是一次做完

REL-05 一句话里有四件事：三个页面位置（通道页签、录像工作区、总览/运维）、四种视图（指标卡、按日趋势、时间带、故障表）、筛选与区间选择、CSV 与告警阈值。它们可以独立验收，且**前三个共用一批新查询**（页签数据、趋势、时间带），CSV 与阈值是独立的写路径。所以按「先读视图、后写导出、最后告警」推进，每步都能单独看真实数据。

## 先看清现状：有什么、缺什么

### 后端已就绪（批次 1–3，NAS 已验证）

| 端点 | 给什么 |
| --- | --- |
| `GET /channels/{id}/reliability/metrics` | 八项指标，按 main/sub/recording 分别返回，率值带分子分母与状态 |
| `GET /channels/{id}/reliability/intervals` | 状态区间（时间带的原料），可按 kind 过滤 |
| `GET /channels/{id}/reliability/obligations` | 应录区间与排除 |
| `GET /channels/{id}/reliability/integrity` | 四个时长 + 归因 + 精度 |
| `GET /channels/{id}/reliability/trend` | 按日趋势（读日汇总） |
| `GET /reliability/summary` | 跨通道比较（admin） |
| `GET /faults`、`PATCH /faults/{id}`、`GET /faults/{id}/impact` | 故障列表/确认/影响 |
| `GET/POST /maintenance-windows` | 维护窗口 |
| `GET /recording-policy-history` | 策略时间线 |

### 缺的

1. **前端全部视图**：没有可靠性页签、没有时间带、没有趋势图、没有指标卡、没有故障表。
2. **录像工作区从缺口跳故障**（REL-05 明令）。
3. **总览异常通道**与**运维页跨通道比较**。
4. **CSV 导出**：需含分母、排除量、待核验量、指标版本与时区。
5. **告警阈值**：主流连续不可用、应录分片超时、每日覆盖下降、监测停止；记录触发窗口/阈值/实际值；持续故障通知去重，恢复留历史。

## 范围

### 4.1 通道「可靠性」页签（只读视图）

`features/channels/reliability.tsx`，挂进 `configure.tsx` 的通道详情。**注意 PRD 说「通道详情增加可靠性页签」且「不新增一级入口」**——所以它是通道详情内部的一块，不是新的侧边栏项。

内容（自上而下）：
- **指标卡**：八项指标各一张，显示率值**与分子/分母/不可判定时长**（PRD 明令不能只给百分比）；`status` 非 `ok` 时显示原因（无有效观测/不适用/无样本）而不是 0%。
- **主/子流分开**：main 与 sub 并排各一组，**不做任何合并**。
- **时间带**：按 kind 画可用/异常/未知/排除四色带，来自 intervals。与回放时间轴同源的画布风格。
- **按日趋势**：recharts 折线（已有依赖），读 trend。
- **录像应录/有效/缺口对照**：来自 integrity 的四个时长条形。
- **故障表**：该通道的故障列表，带分类、时长、状态、确认入口。
- **区间选择**：24 小时 / 7 / 30 / 90 天 / 自定义。
- **精度提示**：`precision` 非 `exact` 时明确标注（REL-06 要求「注明精度与可下钻范围」）。
- **样本不足提示**：观察时长过短时提示，不给出结论。

### 4.2 录像工作区从缺口跳故障

回放页的缺口提示已经是既有能力；补上「跳到对应故障」——点击缺口跳到该时段的故障详情（或筛选后的运维页/故障表）。这需要把缺口时间与故障窗口对上：`recording_gaps` 已有 `start_at`/`end_at`/`reason_code`，故障表有窗口与分类，按时间重叠匹配。

### 4.3 总览异常通道 + 运维页跨通道比较

- 总览页加一张「异常通道」卡：用 `GET /reliability/summary` 找出观察充分（`sample_sufficient`）且可用率最低的若干通道，链接到各自页签。
- 运维页加可靠性比较区：跨通道表格（通道 / 观察时长 / 可用率 / 覆盖率 / 中断数），**观察时长与 `sample_sufficient` 必须同时显示**——PRD 明令「没有足够记录时显示样本不足，不能判为最可靠」。

### 4.4 CSV 导出（`GET /channels/{id}/reliability/export.csv`）

后端生成（与导出一致：服务端出文件，前端只是链接）。文件必须包含（PRD 逐条）：
- 分母、排除量、待核验量
- **指标版本**与**时区**
- 每行一个时间窗（按日或按区间），不只是最终百分比
- 脱敏：不含凭据、路径、IP

### 4.5 告警阈值（迁移 `0027`）

四类阈值：主流连续不可用、应录分片超时、每日录像有效覆盖下降、监测停止。

```sql
CREATE TABLE reliability_alert_rules (
  id uuid PRIMARY KEY,
  channel_id uuid REFERENCES channels(id) ON DELETE CASCADE,  -- NULL = 全站
  kind text NOT NULL CHECK(kind IN ('main_down_streak','recording_segment_timeout','daily_coverage_drop','monitoring_stopped')),
  threshold_ms bigint,          -- 时长类
  threshold_percent numeric,    -- 百分比类
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1,
  updated_by uuid REFERENCES users(id),
  created_at/updated_at
);
CREATE TABLE reliability_alerts (
  id uuid PRIMARY KEY,
  rule_id uuid NOT NULL REFERENCES reliability_alert_rules(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind text NOT NULL,
  state text NOT NULL CHECK(state IN ('firing','resolved')),
  started_at timestamptz NOT NULL, ended_at timestamptz,
  -- REL-05: 记录触发窗口、阈值、实际值
  window_start timestamptz NOT NULL, window_end timestamptz,
  threshold_value numeric NOT NULL,
  observed_value numeric NOT NULL,
  -- 持续故障通知去重：一条告警只通知一次，恢复后再触发是新的一条
  notified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX reliability_alerts_one_firing ON reliability_alerts(rule_id,channel_id) WHERE state='firing';
```

**评估循环**：挂在 worker，读区间/义务/汇总（都是现成的），命中则开告警；恢复则关闭（保留历史，PRD 明令「恢复留历史」）。**去重靠部分唯一索引**：`firing` 状态下同一规则同一通道只有一条，重复命中更新 `observed_value` 而不再通知。这与 `notifications_one_per_hit`、`fault_events_one_open` 是同一套做法。

**告警不是故障**：`fault_events` 记「出了什么问题」，`reliability_alerts` 记「达到了你设的阈值」。一个持续 3 小时的中断是一条故障，但如果阈值是 5 分钟，它同时是一条告警。两者**不复用表**，因为故障由观测派生、告警由配置派生——阈值改了，历史告警不应改变。

**通知**：复用 `notify` 层（批次 4 已交付的分层：渠道无关变量 + 适配器）。新增一个事件类型，站内提醒 + 已配置的外部渠道。

## 明确不做

- 分钟/日汇总的**下钻明细页**（汇总行可点开看区间）：PRD 未要求，批次 3 已给精度标注。
- 本批不做 `planned`/`manual` 录像模式（PRD REC-02/03/04 仍未实现）。
- 不做「综合设备评分」——PRD 明令禁止。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 前端 | `features/channels/reliability.tsx` | 页签：指标卡、时间带、趋势、完整性对照、故障表、区间选择 |
| 前端 | `features/channels/reliability-parts.tsx` | 时间带画布、指标卡、状态文案（可单测的纯逻辑分出来） |
| 前端 | `features/channels/configure.tsx` | 挂页签 |
| 前端 | `features/playback/*` | 缺口 → 故障跳转 |
| 前端 | `features/operations/index.tsx` | 跨通道比较表 |
| 前端 | `features/foundation`（总览）| 异常通道卡 |
| 后端 | `internal/httpapi/reliability.go` | CSV 导出端点 |
| 迁移 | `0027_reliability_alerts.sql` | 规则与告警两表 |
| 后端 | `internal/reliability/alerts.go` | 规则 CRUD、评估、去重、恢复 |
| 后端 | `internal/app/run.go` | 告警评估循环 |
| 契约 | `m1b.openapi.yaml` + `api:generate` | 新端点 |

## 测试覆盖

- **单测（前端）**：率值格式化（`no_observation` 等状态不得渲染成 0%）、时间带分段（区间 → 色块）、区间选择换算、精度文案、样本不足判定。
- **单测（后端）**：告警评估四类各自命中/不命中、去重（重复命中不开第二条）、恢复后再次触发是新的一条、阈值边界。
- **集成**：CSV 内容含分母/排除/待核验/版本/时区且脱敏；告警规则 CRUD 的权限（admin）；评估循环从真实区间开告警；`fault_events` 与 `reliability_alerts` 互不影响。

## 验收对照

- **REL-05**：通道详情可靠性页签 ✓；录像工作区从缺口跳故障 ✓；总览异常通道 ✓；运维页跨通道比较 ✓；不新增一级入口 ✓；24 小时/7/30/90 天/自定义 ✓；按 source_id、配置版本、主子流与原因筛选（本批做 kind 与原因，source_id 筛选随页签的来源列）；指标卡/按日趋势/时间带/应录对照/故障表 ✓；最长断流、连续异常、样本不足提示 ✓；CSV 含分母、排除量、待核验量、指标版本与时区 ✓；四类阈值可配且记录触发窗口/阈值/实际值 ✓；去重与恢复历史 ✓。
- **REL-06 的呈现要求**：跨保留范围查询标注精度与可下钻范围 ✓。

## 风险与边界

- **时间带的性能**：90 天窗口的区间可能有上千条。画布按像素列聚合（同回放时间轴的做法），不做逐区间 DOM。
- **告警的评估频率与窗口**：四类阈值的语义不同（连续不可用是即时状态，每日覆盖是结算后的日粒度）。**按各自的自然节奏评估**：即时类每轮看区间，覆盖类每日汇总后看。混在一个循环里会让「每日」类被高频重算。
- **阈值默认关闭**：PRD 说「可配置」而不是「默认开启」。默认不配规则即无告警，避免升级后突然刷屏。
- **CSV 的时区**：与站点时区一致，并在文件头写明（PRD 要求时区进文件）。
