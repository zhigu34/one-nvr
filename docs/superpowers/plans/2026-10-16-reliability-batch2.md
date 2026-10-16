# M4 可靠性批次 2：应录区间与录像完整性证据（REL-03）

续 `2026-10-16-reliability-batch1.md`。批次 1 交付了「**流是否可用**」的事实层（观测 → 区间 → 故障）。本批交付另一半：「**本该录多少、实际录了多少**」——即 REL-03 的应录区间，以及建在其上的完整性证据。

批次 1 的收尾注释里已经写明本批为什么不能直接开工：

> **应录区间与录像完整性证据**（REL-03）：需要**策略版本历史**（今天 `recording_policies` 只有当前值，`source_switches` 记了 `old_mode`/`desired_mode` 但不构成完整时间线），是独立的一块。

## 先看清现状：已有的与缺的

### 已有的（可直接用）

| 事实 | 载体 | 本批如何用 |
| --- | --- | --- |
| 录像段（起止、通道、源版本、运行） | `recording_segments`（`state IN ('ready','deleted','missing','damaged',…)`） | 完整性的**分子** |
| 录像运行（起止） | `recording_runs`（`purpose='continuous'`） | 应录但没出片的交叉证据 |
| 通道状态区间（main/sub/recording 各自 state） | `reliability_intervals`（批次 1） | 排除项与「应录但失败」的归因 |
| 源切换窗口 | `source_switches`（`old_mode`/`desired_mode`/`finished_at`） | 切换缺口与「切换期间原策略仍生效」 |
| 维护窗口 | `maintenance_windows`（批次 1） | 排除项 `maintenance` |

### 缺的（本批要建）

1. **策略版本历史**。`recording_policies` 只有当前 `mode` + 一个 `version` 计数器，`version` 全库**零读者**。REL-03 明令「**不用当前计划重新解释过去**」——把现在的 `continuous` 套到上个月，等于伪造分母。
2. **应录区间**。没有任何表记录「这段时间应当录」。它是策略版本 × 通道启用状态 × 维护/停用排除项 × 源切换的推导结果。
3. **完整性证据**。应录区间与实际段之间的差额没有落库，也没有归因。

## 范围

### 1. 策略版本历史（迁移 `0025`）

```sql
CREATE TABLE recording_policy_versions (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  version bigint NOT NULL,           -- 与 recording_policies.version 对齐
  mode text NOT NULL CHECK(mode IN ('none','continuous','planned')),
  event_recording boolean NOT NULL DEFAULT false,
  effective_at timestamptz NOT NULL, -- 该版本开始生效的时刻
  reason text NOT NULL,              -- policy_apply | source_switch | rollback | setup
  actor_id uuid REFERENCES users(id),
  switch_id uuid REFERENCES source_switches(id),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(channel_id, version)
);
CREATE INDEX recording_policy_versions_time ON recording_policy_versions(channel_id, effective_at DESC, id DESC);
```

**关键设计：`effective_at` 与「写入时刻」分开。** 与 `maintenance_windows.created_at` 同理：策略是在某个事务里落地的，观察者看到的顺序由 `effective_at` 决定。半开 `[effective_at, next.effective_at)`，与区间表一致（REL-04）。

**写入口只有一处**：`internal/recording/source_switch.go` 的 `finishSwitch`——它是全库唯一的 `UPDATE recording_policies`（已核：另两处是 `site/setup.go` 与 `site/service.go` 的首次 INSERT）。在那里顺带 INSERT 一行历史。`policy_apply`、`source_switch`、`rollback` 都走这条路径，所以一处钩住全覆盖。

**首次 INSERT 也要记**：`site/setup.go` / `site/service.go` 建通道时插 `mode='none'`，同事务补一条 `version=1, mode='none', reason='setup'`。**不记的话，建站之前/之后的边界无法区分**。

**既有数据回填**：迁移里为每个已存在的 `recording_policies` 行补一条 `version=当前 version, effective_at=<该行无历史，取不可知>`。诚实的做法是**不猜时刻**：回填行的 `effective_at` 取 `channel.created_at`（下界，也就是"自通道存在起就可能是这个策略"）并把 `reason` 标成 `backfill`。这样查询时能看出「这个版本的时刻不可信」，而不是假装知道。

**反过来也要防**：`backfill` 行没有可信的 `effective_at`，所以**用它算出的分母必须标注精度**（PRD REL-06：「统计跨越明细保留范围时注明精度」）。做法是在 `integrity` 输出里带 `policy_precision`：`exact`（区间完全落在有历史版本覆盖的时间段内）或 `assumed`（用到了 backfill 行）。

### 2. 应录区间（迁移 `0025`）

```sql
CREATE TABLE recording_obligations (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  source_revision_id uuid,             -- 该时段生效的源版本（REL-03 要求记录）
  policy_version bigint NOT NULL,      -- 形成该义务的策略版本
  -- 义务的三种来源，与 recording_policies.mode 对应（planned 尚未实现）
  source text NOT NULL CHECK(source IN ('continuous','planned','manual')),
  -- 排除：明示不要求录像的时段。与 reliability_intervals.exclusion 同词汇。
  exclusion text CHECK(exclusion IS NULL OR exclusion IN ('disabled','not_configured','maintenance','cleared')),
  started_at timestamptz NOT NULL,     -- 半开 [started_at, ended_at)
  ended_at timestamptz,
  -- 形成该义务时的证据：切换缺口、源会话等。REL-03 要求能回溯"为什么这么算"。
  evidence jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);
CREATE UNIQUE INDEX recording_obligations_one_open ON recording_obligations(channel_id) WHERE ended_at IS NULL;
CREATE INDEX recording_obligations_channel_time ON recording_obligations(channel_id,started_at DESC,id DESC);
CREATE INDEX recording_obligations_window ON recording_obligations(started_at,ended_at);
```

**为什么按通道而不是按 kind**：REL-03 谈的是录像义务，而录像只有一路（主流派生的 `recording`）。这与批次 1 的 `reliability_intervals`（按 main/sub/recording 三个 kind）不同——那个是**流可用性**，这个是**录像要求**。

**推导规则**（PRD REL-03 逐条落地）：

| 情形 | 应录要求 | exclusion |
| --- | --- | --- |
| `mode='continuous'` 且通道启用且有源 | **是** | NULL |
| `mode='continuous'` 但录像服务停机/源故障/存储阻断 | **是**（不缩小分母） | NULL |
| `mode='none'` | 否 | `disabled` |
| 通道未配置源（`current_revision_id IS NULL`） | 否 | `not_configured` |
| 通道停用（`channels.enabled=false`） | 否 | `disabled` |
| 落在维护窗口内 | 否，但**单独展示** | `maintenance` |
| `mode='planned'`（PRD REC-02，未实现） | 否 | 占位：本批按 `none` 处理并标注 |
| 源切换期间，原策略仍生效的时段 | **是** | NULL + `evidence.switch_id` |

**「不缩小分母」是本批最重要的一条**：录像服务停机、源故障、存储不可写期间，**仍然开应录区间**。原因写在 REL-03 原文里——否则分母会随失败一起缩小，可用率反而变好看，而那正是要防的事。

**推导挂在哪：独立的义务对账循环，不挂观测流。**

第一版设计想挂在 `observations.go` 的 `observation()`（批次 1 折 interval 的地方），但核对 `monitor.go` 的扫描后就否掉了：那个扫描是

```sql
SELECT c.id FROM channels c WHERE (c.enabled AND c.current_revision_id IS NOT NULL) OR EXISTS(...)
```

——**停用或没配源的通道根本不在扫描范围里**，`Reconcile` 不会被调用，也就没有观测可挂。而「通道停用」「未配源」恰恰是 REL-03 要求**显式记录排除**的两种情形。挂在观测流上，这两类排除永远写不出来。

改为一个与监控同级的对账循环（`internal/app/run.go` 起，形态照 `runRecordingMonitor`）：每轮遍历**所有**通道（不限启用），从当前事实推导它此刻的应录状态，与开放义务不一致则闭合旧的开新的。这个做法的好处正是它比挂钩写入点更稳：

- **一处覆盖全部转换**：策略变更（`policy_apply`/切换/回滚）、通道启用停用（`channels.Update`）、配源/清源（`apply`/`clear`）、维护窗口登记，四类写入点互相独立，逐个挂钩一定漏；重算则天然全收。
- **自愈**：worker 重启、迁移回填、以后新增的写入点都不需要记得通知这里。
- **与批次 1 同构**：`CloseOpenOnStart` 已经建立了「重启时先结算再采样」的先例，对账循环沿用同一纪律。

代价是边界时刻由轮询决定（比策略实际生效时刻晚至多一轮），所以义务行同时记 `policy_version`，而 `recording_policy_versions.effective_at` 是精确的——读路径要用精确边界时可以据此收敛。这与批次 1「闭合在最后一次支持旧状态的观测、把不确定带一起给出」是同一套口径。

**切换缺口单独记**：`source_switches` 有起止，应录区间在切换窗口内**不中断**（切换期间原策略仍生效），但把 `switch_id` 写进 `evidence`，读路径可据此把这段单独列出。这与 `recording_gaps` 里挂 `switch_id` 的那些行互为佐证。

### 3. 完整性证据

不改表，**按需计算**（读路径 SQL）：给定 `[from,to)`，对每个通道：

- **分母** = 该窗口内 `recording_obligations` 中 `exclusion IS NULL` 的总时长（按半开区间求并集，重叠不重复累加——REL-04 明令「并行的两路流或重复文件不重复累加时长」）。
- **分子** = 与 `recording_segments`（`state IN ('ready','deleted')`——`deleted` 是**已录后被删**，录制本身发生过；`missing`/`damaged` 不算）的交集时长。
- **缺口** = 分母 − 分子，按来源归因：`recording_gaps` 的 reason（源故障/存储/低空间）、`reliability_intervals` 的 recording kind 状态、无对应记录则 `unknown`。
- **待核验** = 片段尚未 `ready`、或区间跨越保留边界无法判定精度的部分，**单列**（REL-05 明令「待核验时长单列」）。

**判定单元是「段」而不是「秒」**：`recording_segments` 的边界来自 ZLM 的实际分片，而 `start_at`/`end_at` 有 `time_evidence`（`completion`/`recovered`/`provisional`）标注精度。本批**按时间求交**（不按段数），但把 `provisional` 证据的段时长单列进待核验，不混进分子。

### 4. HTTP 与契约（页面留批次 3）

- `GET /api/v1/channels/{id}/reliability/obligations?from=&to=` → 应录区间列表（playback 权限，与 intervals 一致）。
- `GET /api/v1/channels/{id}/reliability/integrity?from=&to=` → 完整性汇总（分母/分子/缺口/待核验/归因明细/精度）。
- `GET /api/v1/recording-policy-history?channel_id=`（admin，配置权限）→ 策略版本历史，供排查「为什么这个月分母变了」。

**本批不产生百分比之外的任何率值**：完整性只给四个时长（应录/有效/缺口/待核验）与归因，不给「可用率」。真正的率值要等 REL-04 统一口径（主/子流分开、按 source_id 下钻、校验级别），本批给的是它的输入。

## 明确不做（批次 3）

- **可靠性页面**（REL-05）：通道页签、时间带、趋势图、CSV、阈值配置 UI。
- **REL-04 的指标口径**：主/子流分别计算、按 source_id/配置版本下钻、分钟/日汇总表（REL-06 后两列）。
- **`planned` 与 `manual` 模式**（PRD REC-02/03/04）至今未实现，本批在数据模型里留出词汇但与现有行为一致地按「不要求」处理，**不假装支持**。
- **HW-03/04 检测容量报告**：另一章。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0025_obligations.sql` | `recording_policy_versions` + `recording_obligations` + 既有策略行回填 |
| 策略历史 | `internal/recording/source_switch.go` | `finishSwitch` 里在 UPDATE 之后追加历史行 |
| 策略历史 | `internal/site/setup.go`、`internal/site/service.go` | 首次 INSERT 时补 `version=1` 历史行 |
| 义务推导 | `internal/reliability/obligations.go` | 义务折叠（与 intervals 同形态的 upsert）、排除项判定、切换证据 |
| 接入 | `internal/app/run.go` | 起义务对账循环（遍历全部通道，不限启用） |
| 接入 | `internal/recording/obligations.go` | 从通道/策略/维护/切换事实推导单通道的当前应录状态 |
| 完整性 | `internal/reliability/integrity.go` | 窗口内并集求交、归因、精度标注 |
| 保留 | `internal/reliability/retention.go` | 义务区间与策略版本并入 365 天层（**只删已结束**） |
| HTTP | `internal/httpapi/reliability.go` | 三个端点；契约 + `api:generate` |
| 测试 | 见下 | |

## 测试覆盖

- **单测**：义务折叠（同模式续期、模式变更闭旧开新、排除项切换）；并集求交（重叠区间不重复累加）；归因映射；`backfill` 行的精度标注。
- **集成**：
  - **策略版本按生效时刻切分**：`continuous → none → continuous`，断言三段时间线正确，**且用当前策略重算历史得到不同的（错误的）结果**——这是 REL-03 的禁止项。
  - **停用通道不产生应录**：`channels.enabled=false` 期间开 `disabled` 排除区间，不计入分母。
  - **维护窗口单独统计**：维护期内不开无排除的义务，或开 `maintenance` 排除；断言不计入非计划缺口。
  - **录像失败不缩小分母**：构造池不可用 → 断言义务区间**继续**（无 exclusion），完整性把这算作缺口而非「不要求录」。
  - **切换期间保持应录**：切源期间断言义务不中断、`evidence` 带 `switch_id`。
  - **`deleted` 段算有效**：删除后的段不进分母扣减（录制发生过）；`missing`/`damaged` 算缺口。
  - **`provisional` 段进待核验**：不混进有效分子。
  - **窗口裁剪**：查询窗口小于义务区间时，只计交集部分。
  - **回填精度**：只有 `backfill` 版本的通道，`policy_precision='assumed'` 且输出带原因。
  - **保留**：超过 365 天的已结束义务被清理，开放义务与有媒体引用的策略版本保留（REL-06「来源身份与必要版本元数据在仍有媒体/健康记录引用时保留」）。

## 验收对照

- **REL-03**：按策略历史版本形成应录区间 ✓；连续/计划/手动逻辑取并集 ✓（`planned`/`manual` 留词汇、不假装支持）；录像服务停机、源故障、存储阻断仍属应录 ✓（**本批核心**）；明确的停用/未配置/计划外/维护分别展示 ✓；源切换期间保留应录并记录切换缺口 ✓；记录应录起止、策略版本、source_id、源版本/会话与排除原因 ✓；**不用当前计划重新解释过去** ✓（策略版本历史 + 生效时刻）。
- **REL-06**（部分）：应录与缺口纳入 365 天保留 ✓；精度标注（`exact`/`assumed`）✓。
- **REL-04 的输入齐备**：本批给分母与分子及其不确定带，率值计算留批次 3。

## 风险与边界

- **策略版本会有多少行**：只在策略实际变更时写一行，正常站点每月个位数；32 路 × 每天一次变更 × 365 天 ≈ 1.2 万行，可忽略。
- **义务区间行数**：与 `reliability_intervals` 同量级（每次状态/策略变化开一行），批次 1 的估算适用（百万级/年）。
- **回填诚实性**：迁移无法知道历史策略的真实生效时刻，所以**不猜**——标 `backfill` 并让查询侧降精度。任何声称「知道建站前的策略」的实现都是在编数据。
- **`planned`/`manual` 的占位边界**：数据模型有词汇，但推导逻辑与现有产品行为一致地按「不要求录像」处理，并在输出里如实标注为未实现，避免将来页面把占位读成功能。
- **不产生率值**：与批次 1 同样的理由——率值必须先有完整事实与统一下钻口径。
