# M4 可靠性批次 3：指标口径与汇总（REL-04 + REL-06 后半）

续 `2026-10-16-reliability-batch1.md`（事实层）与 `2026-10-16-reliability-batch2.md`（应录与完整性）。批次 1 交付「流是否可用」、批次 2 交付「本该录多少」，本批交付**率值本身**，以及让长窗口查询可行的汇总层。

**批次切分说明**：REL-04/05/06 是 PRD 里最大的三节，一起做无法一次验证完。本批做 **REL-04 全部指标 + REL-06 的分钟/日汇总**（后端事实→率值→汇总三层齐备）；**REL-05 页面、CSV、阈值告警留批次 4**。理由与批次 1/2 一致：率值必须先算对，页面才有东西可画；而率值对不对是可以独立验证的——PRD 给了一个精确算例（见下）。

## 验收锚点：PRD 的算例

REL-04 原文给了两个算例，本批**直接用它们做测试**，不另行发明口径：

> 60 分钟期望拉流窗口内，40 分钟已知可用、10 分钟已知不可用、10 分钟监测未知，则观测可用率为 80%，保守可用率为 66.67%，监测覆盖率为 83.33%；三个数同时展示。

> 若另一个已结算的 60 分钟应录窗口只生成 50 分钟基础有效视频，则录像有效覆盖率为 83.33%，**不因 10 分钟故障而改用 50 分钟作分母**。

第二个算例是本批最容易做错的地方：故障不能缩小分母。批次 2 已经保证应录区间不随失败缩小，本批的覆盖率直接继承这一点。

## 八个指标的定义（逐条对齐 PRD 表格）

| 指标 | 分母 | 分子 | 边界 |
| --- | --- | --- | --- |
| 源监测覆盖率 | 期望拉流时长 | 已知可用 + 已知不可用 | 监测未知单列；计划停用/未配置/维护的排除时长同时展示 |
| 观测可用率 | 已知可用 + 已知不可用 | 已知可用 | 主/子流分开算；分母 0 → `no_observation` |
| 保守可用率 | 期望拉流时长 | 已知可用 | 未知**不当作在线**；分母 0 → `not_applicable` |
| 非计划中断 | — | — | 次数、总时长、最长一次、持续中及分类；组件停机与维护单列 |
| 平均恢复时长 | 已结束的非计划中断次数 | 其时长之和 | 未恢复的数量与当前时长单列；无样本 → `no_samples` |
| 录像生成有效覆盖率 | **已结算**应录时长 | 基础有效视频 ∩ 已结算应录 的并集 | 待核验应录时长单列；无应录 → `not_applicable` |
| 录像缺失/损坏 | — | — | 应录未有效覆盖的时长、区间数、最长连续缺口、损坏片数及原因 |
| 历史媒体可访问率 | 仍有保留要求的逻辑录像时长 | 至少一副本可访问的时长 | **已验证下界**；未核验/远端未知单列；不把上传成功历史当当前证据 |

**「已结算」的定义**：义务区间已闭合（有 `ended_at`）。仍在进行中的应录窗口没有结算，不参与覆盖率——否则一个进行中的窗口会随查询时刻变化，同一段历史的数字会漂移。这与 REL-03 里「不用当前计划重新解释过去」是同一纪律。

**「已知/未知」的映射**：`reliability_intervals.state` 的 `healthy`→可用、`unavailable`→不可用、`unknown`→监测未知、`excluded`→排除（不进任何分母，单独展示）。

**主/子流不得互相掩盖**：指标按 `kind` 分别返回（main/sub/recording 各自的数值），**不提供任何跨 kind 的合并值**——PRD 明令「子流正常不能掩盖主流不可用」。API 形状上就没有那个字段，而不是靠调用方自觉。

**同一 source_id 出现在多个通道时按通道上下文展示**，不能把同时刻的观察时长相加虚增设备时长。所以指标一律**按通道**计算，跨通道汇总只做「各通道之和」并把通道数一并返回。

## 范围

### 1. 指标计算（`internal/reliability/metrics.go`）

纯函数 + 一个查询装配器，形态与批次 2 的 `ComputeCoverage` 一致：

```go
type MetricsInput struct {
    Healthy, Unavailable, Unknown, Excluded []TimeSpan
    Faults                                  []FaultSample
    OwedSettled, Unverified                 []TimeSpan
    Recorded                                []TimeSpan
    Damaged                                 []TimeSpan
    Copies                                  []CopyEvidence
}
type Metrics struct { ... }
```

**所有率值输出三件套**：`numerator_ms`、`denominator_ms`、`unjudgeable_ms`，外加 `status`（`ok` / `no_observation` / `not_applicable` / `no_samples`）。PRD 明令「不能只存最终百分比」——结构上就不给你一个裸百分比。

分母为 0 的三种语义要分清（PRD 原文分别点名）：
- 观测可用率分母 0 → `no_observation`（无有效观测）
- 保守可用率 / 覆盖率分母 0 → `not_applicable`（不适用）
- 平均恢复时长无已结束样本 → `no_samples`（无样本）

### 2. 分钟/日汇总（迁移 `0026`）

```sql
CREATE TABLE reliability_minutes (
  channel_id uuid NOT NULL, kind text NOT NULL,
  minute timestamptz NOT NULL,             -- 对齐到分钟
  healthy_ms, unavailable_ms, unknown_ms, excluded_ms bigint NOT NULL DEFAULT 0,
  owed_ms, covered_ms, unverified_ms bigint NOT NULL DEFAULT 0,
  fault_count integer NOT NULL DEFAULT 0,
  computed_at timestamptz NOT NULL,        -- REL-06: 记录计算时间
  PRIMARY KEY(channel_id,kind,minute)
);
CREATE TABLE reliability_daily (
  channel_id uuid NOT NULL, kind text NOT NULL, day date NOT NULL,
  ... 同上各项 ...,
  computed_at timestamptz NOT NULL,
  metric_version integer NOT NULL,          -- REL-06: 版本
  input_watermark timestamptz NOT NULL,     -- REL-06: 输入水位
  PRIMARY KEY(channel_id,kind,day)
);
```

**为什么是汇总而不是快照**：两张表都是**区间事实的投影**，可以随时从 `reliability_intervals` / `recording_obligations` 重算。REL-06 明令「不通过累计快照覆盖原始事实来掩盖故障」——所以汇总行不参与任何判定，只服务长窗口查询性能。

**增量与迟到修订**：扫描「已封口的分钟」（已完全过去），按 `(channel,kind,minute)` upsert。迟到回调或校验修订只影响最近若干分钟，重算覆盖即可；不引入队列，也就不可能阻塞录制（REL-06 的硬要求）。

**输入水位**：`input_watermark` = 该分钟汇总时纳入的最新事实时刻。跨越汇总保留范围查询时据此注明精度（REL-06：不提供已淘汰证据的假链接）。

### 3. 保留分层（REL-06 补齐）

批次 1 已做观测 7 天 / 区间故障义务 365 天。本批补：
- 分钟汇总 **30 天**
- 按日汇总 **730 天**

### 4. HTTP 与契约

- `GET /api/v1/channels/{id}/reliability/metrics?from=&to=` → 八项指标（按 kind 分组）
- `GET /api/v1/channels/{id}/reliability/trend?from=&to=&granularity=day` → 按日趋势
- `GET /api/v1/reliability/summary?from=&to=` → 跨通道（admin），用于总览异常通道与运维页比较

**精度注明**：响应带 `precision`（`exact` / `minute_rollup` / `daily_rollup`），说明窗口跨越了哪一层保留。查询超出明细范围时走汇总并如实标注，不假装是明细。

## 明确不做（批次 4）

- REL-05 页面（通道「可靠性」页签、时间带、趋势图、指标卡、故障表、录像页缺口跳转、总览异常通道、运维页跨通道比较）。
- CSV 导出（需带分母、排除量、待核验量、指标版本与时区）。
- 阈值告警配置（主流连续不可用、应录分片超时、每日覆盖下降、监测停止）。
- `planned`/`manual` 录像模式（PRD REC-02/03/04）仍未实现，本批同样按「不要求」处理。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0026_reliability_rollups.sql` | 分钟/日汇总两表 + 索引 |
| 指标 | `internal/reliability/metrics.go` | 八个指标的纯计算 + 查询装配 |
| 汇总 | `internal/reliability/rollup.go` | 封口分钟扫描、upsert、日汇总派生 |
| 保留 | `internal/reliability/retention.go` | 分钟 30 天、日 730 天 |
| 装配 | `internal/app/run.go`、`exportjobs.go` | 起汇总循环（沿用 retention 的形态） |
| HTTP | `internal/httpapi/reliability.go` + `m1b` + `api:generate` | 三个端点 |
| 前端 | 无（批次 4） | |

## 测试覆盖

- **单测**：
  - **PRD 算例原样**：40/10/10 → 80% / 66.67% / 83.33%。
  - **覆盖率的第二个算例**：60 分钟应录、50 分钟有效 → 83.33%，且分母仍是 60。
  - 分母为 0 的三种语义分别命中 `no_observation` / `not_applicable` / `no_samples`。
  - **未知不当作在线**：保守可用率严格 ≤ 观测可用率（除无未知时相等）。
  - 主/子流分开：一个 kind 全可用、另一个全不可用，断言两者数值互不影响。
  - 并集不重复累加（重叠区间）。
  - 平均恢复时长只算已结束的中断。
- **集成**：
  - 观测 → 区间 → 分钟汇总 → 日汇总的链路一致性（同一窗口明细与汇总结果相同）。
  - 迟到修订：改动最近区间后重算，汇总随之更新且 `input_watermark` 前进。
  - 保留：31 天前的分钟被清理、日汇总保留；窗口跨保留边界时 `precision` 正确降级。
  - 权限：metrics 要 playback；summary 要 admin。
  - 混合排除：停用/未配源/维护时长不进任何分母，只进排除列。

## 验收对照

- **REL-04**：按通道汇总 ✓；`[start,end)` 统一 ✓；展示统计窗口/时间量/观测覆盖/校验级别 ✓；主/子流分别计算且不合并 ✓；切换点按生效时段拆分 ✓（依赖批次 2 的义务区间与来源版本）；并行流不重复累加 ✓（并集）；八个指标逐条落地 ✓；**所有率值带分子分母与不可判定时长** ✓（结构强制）；分母 0 的三种语义 ✓。
- **REL-06**：分钟 30 天 / 日 730 天 ✓；跨保留范围注明精度与可下钻范围 ✓；增量汇总且有界批量 ✓；记录计算时间、版本、输入水位 ✓；不阻塞录制（无队列、纯读事实后写汇总）✓。
- **批次 4 的输入齐备**：页面所需的指标卡、趋势、时间带数据本批全部就绪，只差渲染与 CSV。

## 风险与边界

- **分钟汇总写入量**：32 通道 × 3 kind × 1440 分钟 = 约 14 万行/天。按「只写有观测或应录的分钟」裁剪后实际远低于此；30 天保留下量级可控，且这批行是可重算的投影，丢了也能补。
- **趋势查询别扫明细**：超过分钟保留范围的窗口必须走日汇总，否则一次 90 天查询会扫全部区间行。端点按窗口自动选层并如实标注粒度。
- **不给综合评分**：PRD 明令「不设置无依据的综合『设备评分』」。本批只输出事实指标，任何打分都在错误的一侧。
- **跨通道汇总的语义**：只做同 kind 的时长相加，并把通道数一并返回；不做「平均可用率」（那会把不同观察时长的通道等权，是另一种虚增）。
