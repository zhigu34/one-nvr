# 摄像头接入流程与录像计划拆分 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan in the current session, batch by batch. 每个批次完成后提交一次（一批一次提交）。

**Goal:** 把「添加摄像头」与「录像管理」彻底分开：录像方式迁到独立的侧边栏一级菜单「录像计划」，通道配置页只负责连接；连接按 RTSP / ONVIF 区分添加方式；保存与使用解耦，提供不测试不启用的纯保存；侧边栏按功能分组。

**Architecture:** 前端拆出 `features/recording-plan/` 独立工作区，通道配置页的 Tab 收敛为「连接配置 / 历史与诊断」，删掉「连接诊断与历史配置应用」折叠层让测试与应用常显。后端不做破坏性改动：`PUT /channels/:id/recording-policy` 与 `PUT /channels/:id/storage-pool` 保持不变，仅新增一个批量端点 `PUT /recording-policies`（单事务内为 N 个通道各入队一个 job）。通道级启停与分组列留到批次 2（含迁移），ONVIF 真实现留到批次 3。

**Tech Stack:** Go、PostgreSQL、React/TypeScript/shadcn-admin、TanStack Router/Query。

**Spec:** [PRD](../../PRD.md) CH-01（连接测试与离线草稿）/ CH-02（通道列表）/ CH-04（启停独立）/ CH-05（凭据）/ CH-06（ONVIF）；[M1 控制面设计](../specs/2026-10-03-m1-control-plane-design.md) §数据表 `channels`、§API `GET/PATCH /channels/:id`；[M1-B 计划](2026-10-04-m1b-channel-recording.md) Global Constraints。

## 用户指示（2026-10-08，权威，不可自行改动）

1. **录像计划页做成侧边栏一级菜单**，不挂在存储池或录像回放下面。
2. 该页**按通道管理录像方式：手动、定时、事件，可批量应用**。
3. **ONVIF 本批只做占位，后续再加功能**。
4. **「配置仅保存」按「未启用」处理，UI 与文档里不再出现「草稿」字样**。
5. **「配置保存」与「使用」是两件事**：保存这条摄像头信息是独立能力，能否使用是另一回事。

三点裁决（用户已选，均取推荐项）：

- **「手动」= 现有的连续录像**（复用 `continuous` 语义，不改数据模型）。
- **定时 / 事件本批只做占位禁用**，标注「后续开放」（定时属 PRD M2、事件属 M3）。
- **「未启用」按通道级与配置级分开**：通道级启用/停用（`channels.enabled`）+ 配置级已应用/未应用（`current_revision_id` 是否指向它）；**「保存」只写配置，绝不动正在运行的源**。

## Global Constraints

- **不回归 M1-B/M1-C**：连接、测试、应用、清空、批量导入、回放六条既有链路的行为与断言不得改变，除本计划明确移出的项。
- **测试证据不变**：测试结果有效期、码率证据窗口、存储池三方写入证据的既有语义不得修改；「测试并启用」仍以有效证据为前置。
- **保存不动运行中的源**：纯保存只创建一条 `source_revisions` 记录（`POST /channels/:id/source-revisions`），不排任务、不改 `current_revision_id`、不改 `channels.enabled`、不改录像策略。这一点与 PRD CH-01「允许保存离线草稿，但不将未测试草稿自动替换正常运行的源」一致（UI 措辞用「已保存，未启用」，不用「草稿」）。
- **未实现能力不显示为可启用**：ONVIF、定时录像、事件录像一律显式禁用并标注后续开放，不得做成可点但报错。
- **权限不变**：录像方式写操作仍要求目标通道 `Configure`；存储池读取仍仅管理员；批量端点必须逐通道鉴权，不允许「一次鉴权全批放行」。
- **契约四层同步**：改 API 必须同时改 Go handler/service、`docs/api/*.openapi.yaml`、并重新生成 `apps/web/src/lib/api-types.ts`（CI 会 `git diff --exit-code`）。
- **不明文**：新增响应不得带摄像头凭据、完整 RTSP URL 或存储池绝对路径。
- **一批一次提交**：批次内所有任务完成后提交一次，提交信息中文、说明改了什么与为什么。

## Review Focus

- 纯保存之后：通道仍在原源上正常取流与录像，历史与策略均未被改动；界面上这条配置显示「已保存，未启用」。
- 「测试并启用」缺失有效证据时必须禁用并说明原因，不得静默跳过测试直接应用。
- 录像方式迁到新页后：旧页不再出现任何录像控件；从通道列表能一眼看出某通道当前录像方式。
- 批量应用：部分通道失败时逐行给出结果，成功行不回滚、不重放；通道版本冲突按既有 409 语义暴露。
- ONVIF / 定时 / 事件三处占位：不可聚焦、不可提交、文案明确「后续开放」。
- 侧边栏分组后：非管理员看不到空分组标签（分组内一项都没有时整组不渲染）。

## 批次划分

| 批次 | 内容 | 迁移 | 提交 | 状态 |
| --- | --- | --- | --- | --- |
| **1** | 侧边栏分组 + 录像计划页 + 配置页拆 Tab + 连接流程去录像耦合 + 连接表单重排 + 状态语义 + 批量录像端点 | 无 | 1 次 | ✅ `cb7bd9a` |
| **2（本计划详述）** | 分组列 + 通道级启用/停用（`channels.enabled` 写入口）+ 列表一次请求拿全（分组/码率/最近错误/更新时间，消 N+1） | `0011_channel_group.sql` | 1 次 | ✅ |
| 2 补 | CI 暴露的真实媒体验收适配 + 标签/竞态修复 + 媒体验收失败可诊断 | 无 | 2 次 | ✅ `57bfa51` `06d98a4`（远端 `d18aca0` `a2505c7`） |
| 3 | ONVIF 真实现：局域网发现 + 能力探测 + 自动填 RTSP（CH-06） | 视需要 | 1 次 | 未做 |

## 本拆分带来的两处行为变化（有意，需知悉）

1. **切换源时不再自动刷新存储池证据。** 后端一直要求：当通道正在连续录像时，源切换的入队要校验存储池有 3 份未过期的健康检查（`requestChangeTx`）。此前 `connectSource()` 会替操作者顺手跑一次池检查，现在这条检查归属录像计划页，所以**在录像中的通道上换源，需要先确认存储池证据新鲜，否则应用会以 `zlm_write_evidence_unavailable` 失败**（提示语已明确指向"请先运行存储池检查"，录像计划页每行都有「检查存储池」）。首次接入（尚无录像策略）不受影响。
2. **纯保存不再顺带测试。** 「保存」只写 `source_revisions`，不排任务；要验证必须显式点「测试取流」。这正是用户要求的"保存与使用分开"，代价是首次接入多一次点击。

---

# 批次 1

Files: `apps/web/src/components/layout/data/sidebar-data.ts`、`apps/web/src/components/layout/app-sidebar.tsx`、`apps/web/src/features/recording-plan/*`（新增）、`apps/web/src/routes/_authenticated/recording-plan/index.tsx`（新增）、`apps/web/src/features/channels/*`、`internal/channel/*`、`internal/httpapi/*`、`docs/api/m1b.openapi.yaml`

## Task 1: 侧边栏按功能分组

- [x] `sidebar-data.ts` 的 `navGroups` 由单一 `one-nvr` 组改为三个功能分组：
  - **监控**：总览 `/`、实时预览 `/live`、录像回放 `/recordings`
  - **配置**：通道管理 `/channels`、录像计划 `/recording-plan`、存储池 `/storage-pools`
  - **系统**：用户与权限 `/users`、系统设置 `/settings`、运维与审计 `/operations`
- [x] `app-sidebar.tsx` 过滤后为空的整组不渲染：现在只过滤 item，非管理员会看到只剩标签的空分组 `系统`。
- [x] 既有声明式可见性规则不变（`everyRole` 为真者对所有角色可见，其余仅管理员）。
- [x] 新增项「录像计划」标 `everyRole: true`（与通道管理一致）；页内存储池选择仍仅管理员可见。

## Task 2: 录像计划页与路由

- [x] 新增路由 `routes/_authenticated/recording-plan/index.tsx`（`createFileRoute('/_authenticated/recording-plan/')`）。
- [x] 新增 `features/recording-plan/index.tsx`：
  - 复用 `GET /api/v1/channels?limit=100` 与每通道 `GET /channels/:id/recording-policy`、`GET /channels/:id/source/status`（取 `storage_pool_id`）。
  - 每行：通道号、通道名称、当前录像方式、存储池、操作。
  - 录像方式四档：**关闭录像**（`none`）、**手动**（`continuous`，副标注「连续录像」）、**定时录像**（占位禁用）、**事件录像**（占位禁用）。
  - 存储池选择仅管理员可见；启用「手动」而通道无存储池时给出明确提示。
  - 勾选多通道 → 批量应用录像方式（Task 7 的端点；Task 7 未落地前先按逐通道请求，落地后切到批量）。
- [x] 页面文案不出现「草稿」；「手动」的说明写明「= 连续录像」。
- [x] 测试 `features/recording-plan/index.test.tsx`：方式选择、占位禁用不可提交、无存储池时的提示、权限过滤。

## Task 3: 通道配置页拆出录像

- [x] `features/channels/configure.tsx`：Tabs 由三项收敛为「连接配置 / 历史与诊断」，删除 `recording` Tab、`RecordingPolicyControls`、`RecordingsIndex` 及其相关查询（`recording-policy` 若不是其它逻辑所需则一并移除）。
- [x] `features/channels/recording-policy.tsx`：主体迁入 `features/recording-plan/`；确认无其它引用后删除原文件与 `recording-policy.test.tsx`（或随迁）。
- [x] `features/channels/recordings-index.tsx`：仅 `configure.tsx` 引用（已全项目核对）；本批先移除引用，文件保留待批次间确认后处理。回放页 `/recordings` 已覆盖按通道浏览录像。
- [x] 补一个从配置页跳到录像计划页的入口（带通道参数），让「录不录去哪设」有明确去向。

## Task 4: 连接流程去掉录像耦合

- [x] `features/channels/source-connect.ts`：删除自动存储池检查（现 112–130 行）。录像前提不属于连接流程。
- [x] 同文件：`first_recording_mode` 不再是替用户决定的隐藏行为，改为**显式规则**——添加摄像头只接入取流（`first_recording_mode='none'`），录像方式一律去录像计划页设定；文案要写明这一点。
- [x] `features/channels/source-test.tsx`：删除「首次普通录像」下拉、「请先绑定存储池」提示，以及「连续录像所需码率证据过期」那句；保留测试结果展示与「保存并测试 / 测试并启用」。
- [x] 回归：`source-connect.test.ts`、`source-test.test.tsx`、`configure.test.tsx` 随改动更新。

## Task 5: 连接表单重排

- [x] `features/channels/source-form.tsx`：
  - 表单顶部新增「添加方式」二选一：**RTSP 手动**（可用）/ **ONVIF 发现**（禁用占位，标注后续开放）。
  - `onvif_port` 从 RTSP 字段区移入 ONVIF 方式下（占位状态下不渲染或渲染为禁用）。
  - 提交按钮拆为 **「保存」**（不测试、不启用）与 **「保存并测试」**，从折叠区移出、常显。
  - 去掉 `order-last`：它让主按钮在视觉上落到底部，而 DOM 顺序在高级设置之前，导致键盘 Tab 顺序与视觉顺序不一致。
  - `高级连接设置` 保留（RTSP 端口、传输方式、摄像头操作、用户名清空、密码处理确实属高级项）。
- [x] `features/channels/configure.tsx`：
  - 「基本信息」Panel 并入连接区顶部，不再单独占一个 Panel。
  - 拆掉 `连接诊断与历史配置应用` 折叠层，测试结果与「测试并启用」常显。
  - 「应用配置」改名「测试并启用」。
- [x] 纯保存路径：调用 `POST /channels/:id/source-revisions` 后**直接结束**，不调 test、不调 apply；状态提示为「已保存，未启用」。

## Task 6: 状态语义补「未测试」

- [x] `features/channels/channel-status.tsx`：通道头部去掉「录像」灯（不与主流/子流并列）；录像状态只在录像计划页呈现。
- [x] 状态档位明确为：未配置 / 未测试 / 正常 / 降级 / 不可用 / 已关闭。「未测试」对应「有修订但从未产生观测」，与「未配置」（无修订）区分开——现在两者都会落到 `unknown`，用户无法判断。
- [x] 若需要区分「未测试」与「观测过期」，按 `source_status.go` 现有 `reason` 字段在前端细分，不改后端返回结构。

## Task 7: 批量应用录像方式

- [x] `internal/httpapi/router.go`：新增 `PUT /api/v1/recording-policies`。
- [x] `internal/httpapi/channels_recording.go`：batch handler，逐通道取 `expected_version`，单事务内为每个通道入队一个 `policy_apply` job。
- [x] `internal/channel/source_changes.go`：`SetPolicies`（或等价实现），逐通道 `RequireChannelTx(Configure)`；任一通道鉴权失败只影响该行，不整批回滚。
- [x] `internal/channel/source_types.go`：批量请求与逐行结果 DTO。
- [x] `docs/api/m1b.openapi.yaml`：新端点与 schema；`x-one-nvr-implementation` 标 implemented。
- [x] 重新生成 `apps/web/src/lib/api-types.ts`（`node apps/web/scripts/generate-api.mjs`）。
- [x] 前端录像计划页接入批量端点，展示逐行结果。

## 批次 1 校验

- [x] `npx vite build` → `tsc -b` → `eslint`（前端加路由后必须按此顺序，先 build 生成 `routeTree.gen.ts`）。
- [x] `go build ./...`、`go test ./internal/channel/... ./internal/httpapi/...`。
- [x] `node apps/web/scripts/generate-api.mjs` 后 `git diff --exit-code apps/web/src/lib/api-types.ts` 必须无差异。
- [x] 真机/容器验收由 GitHub CI 承担（本机无 Docker daemon）。

## 批次 1 落地偏差（执行中修正，均已按实际代码复核）

- **`configure.tsx` 保留锚点而非改用 `Link`**：`/channels` 路由带 `validateSearch`，`Link` 会强制要求 `search`（TS2741）；且该文件的单测不挂路由，`Link`/`useNavigate` 会抛 `useRouter must be used inside a <RouterProvider>`。原代码用 `<a href>` 是有意的，继续保持；「前往录像计划」用 `<Button asChild><a>`。
- **表单主按钮放在 DOM 前面**：隐式提交（在输入框里按 Enter）触发的是**表单中第一个 submit 按钮**。把「保存」放在前面会让回车变成"只保存"。因此顺序为主按钮「保存并启用」在前、「保存」在后，保持此前已验证的回车行为。
- **`onvif_port` 改为原样保留**：`CreateDraft` 直接把 `in.Config` 落库，而表单已不再渲染该字段。若不显式带回旧值，一次普通编辑就会把导入进来的 ONVIF 端口静默清成 NULL —— 界面不得丢弃它不拥有的数据。
- **`command-menu.tsx` 一并修正**：它的可见性此前是硬编码 `item.url === '/' || item.url === '/channels'`，与侧边栏的 `everyRole` 不一致；分组后还会渲染空标题，故改为与侧边栏同一套声明式过滤。
- **新增 `fail` 的错误映射**：`storage.ErrMediaProof` 是普通 `errors.New`，此前会落到兜底分支成为 `503 dependency_unavailable`，把"跑一次存储池检查即可"说成"服务不可用"。现映射为 `409 zlm_write_evidence_unavailable`，与新批量端点的逐行错误码一致。
- **删除了两个组件文件**（`recording-policy.tsx`、`recordings-index.tsx` 及其测试）：已全项目 grep 确认只被 `configure.tsx` 引用；前者迁入 `features/recording-plan/`，后者由回放页 `/recordings` 覆盖。
- **`connectSource` 的 `'test'` 分支删除**：表单不再提供「保存并测试」，该分支成为不可达代码；`SaveConnection.action` 收敛为 `'connect' | 'save'`，`'save'` 在 `configure.tsx` 里于调用 `connectSource` 之前处理。
- **本批次未跑真实容器/媒体 CI**（本机无 Docker daemon）：`vite build`、`tsc -b`、`eslint src`（0 error 0 warning）、`vitest run`（152 通过 / 26 文件）、`go build`、`go vet`、`go test` 均通过，但推送后必须看 GitHub CI 结论。

---

# 批次 2：通道属性与启停、列表信息补全

Files: `migrations/0011_channel_group.sql`（新增）、`internal/channel/{service,source_status,source_types}.go`、`internal/httpapi/{foundation,router}.go`、`docs/api/{m1a,m1b}.openapi.yaml`、`apps/web/src/features/channels/{index,configure,channel-status}.tsx`（+ 新增 `status-reasons.ts`）、`apps/web/src/features/playback/zone.ts`、`tests/integration/{channel,source_status}_test.go`

## Task 1: 分组列与写入口

- [x] 迁移 `0011_channel_group.sql`：`channels.channel_group text NOT NULL DEFAULT '' CHECK(char_length(...)<=64 AND ... !~ '[[:cntrl:]]')`。
- [x] `channel.Channel` 补 `ChannelGroup` 与 `Enabled`；`List` 的 SELECT 增补两列。
- [x] `channel.UpdateInput` 改指针字段（`ChannelName` / `ChannelGroup` / `Enabled`），`Update` 用 `coalesce($n::type,列)` 只改传入项，仍保留 `version=version+1 WHERE version=$expected` 与 `RequireChannelTx(Configure)`。
- [x] 审计按实际发生的属性逐条记录：`channel.renamed` / `channel.regrouped`（带 `{"channel_group": …}`）/ `channel.disabled` / `channel.enabled`。
- [x] 空输入（三个字段都为 nil）返回 422；分组超 64 字符返回 422；陈旧版本返回 409。

## Task 2: 通道级启用/停用

- [x] `channels.enabled` 终于有了写入口（该列自 `0005` 起只被读取：`recording/scheduler.go`、`monitor.go`、`live/service.go`、`source_status.go`）。
- [x] 停用只停取流与录像：不改 `current_revision_id`、不改录像策略、不删历史（集成测试断言 `current_revision_id` 仍为 NULL 且无源被创建）。
- [x] 前端两处入口都加二次确认（`ConfirmDialog`），并明确"约 30 秒内生效、历史录像保留"。

## Task 3: 列表一次请求拿全（消 N+1）

- [x] `source_status.go` 抽出 `statusTx(ctx, tx, ch)`（不做鉴权，只做推导），`GetStatus` 改为「鉴权 + statusTx」。
- [x] 三种 kind 的观测由**一个** `DISTINCT ON` 语义的查询取回（索引 `source_observations(channel_id,kind,observed_at DESC,id DESC)` 保证首行即最新），替代原先的 3 次单 kind 查询。
- [x] 新增 `SourceService.Summaries`：单事务内取可见通道 → 逐通道 `statusTx` → 一次查询取全部源摘要（ip/main_path/sub_path）→ 一次查询取全部最新有效码率。**复用 statusTx 保证与 `GetStatus` 不会分叉。**
- [x] 新增 `GET /api/v1/channels/summary`；`ChannelSummary` 含 业务属性 + 权限 + 源摘要 + 三种状态 + `last_error` + `bitrate_kbps` + `updated_at` + `current_revision_id`。
- [x] 前端 `features/channels/index.tsx` 由「1 次列表 + 每路 1 次 status 轮询」改为**1 次 summary 轮询**；补 分组列、分组筛选、码率、最近错误、更新时间列。
- [x] 时间显示复用 `features/playback/zone.ts` 新增的 `formatMinute`（YYYY-MM-DD HH:mm，站点时区；时区未就绪显示 `—`，不猜）。

## Task 4: 契约与批次 1 遗留

- [x] `m1a.openapi.yaml`：`Channel` 补 `channel_group`/`enabled` 并加入 required；`PATCH /channels/{id}` 请求体改为三字段可选（`minProperties: 1`），不再强制 `channel_name`。
- [x] `m1b.openapi.yaml`：新增 `ChannelSummary` / `ChannelSummaryPage` 与 `/api/v1/channels/summary`。
- [x] 重新生成 `apps/web/src/lib/api-types.ts`。

## 批次 2 校验

- [x] `vite build` / `tsc -b` / `eslint src`（0 error 0 warning）。
- [x] `vitest run`：**156 通过 / 26 文件**（含新增：列表单请求、分组筛选、启用二次确认、批量分组逐通道带版本、`formatMinute`）。
- [x] `gofmt -l`（改动文件）干净；`go build` / `go vet` 通过。
- [x] **真实 PostgreSQL 集成测试**（本机 PG 17.10 可用，`TEST_DATABASE_URL` 指向 `one_nvr_test`）：`tests/integration` 全量通过，含新增 `TestChannelAttributesAreIndependentOfMedia` 与 `TestChannelSummariesMatchStatusAndRespectScope`。

## 批次 2 落地偏差（执行中修正，均已按实际代码复核）

- **迁移号是 `0011` 不是 `0010`**：`0010_live_sessions.sql` 已存在（M1-C 实时预览时新增）。计划里写的 `0010_channel_group.sql` 会与既有文件重名。
- **`Status` 的三种 kind 观测合并为一次查询**：原计划只要求抽 `statusTx`，但 summary 会对每路各调一次，3 次单 kind 查询会让 32 路产生 96 次查询。合并后每路 3 次（channels 行、首次录像判定、观测），且索引方向与 `ORDER BY kind, observed_at DESC, id DESC` 一致。
- **`Summaries` 的富集查询用 grant join 而不是 id 数组**：`WHERE channel_id = ANY($1)` 需要 `[]uuid` 的编码/强转，改用 `JOIN channel_grants g ON g.channel_id=… AND g.user_id=$1` 后既没有数组类型问题，也保证调用者授权外的通道不可能贡献数据。
- **`last_error` 只报真实故障**：只有 `unavailable` / `degraded` 才作为错误上报；`disabled` 与 `not_configured` 是操作者选择的结果，报成"错误"会误导。
- **`bitrate_kbps` 取「最新一条 valid 采样」**，无采样时为 `null` 且前端显示 `—`，不把未知当 0。
- **加了二次确认**：停用会中断录像，属破坏性操作，列表页与配置页都走 `ConfirmDialog`，而不是一个可误触的开关。
- **`reasonLabel`/`STATUS_REASONS` 抽到独立文件 `status-reasons.ts`**：从组件文件导出非组件会触发 `react-refresh/only-export-components`（批次 1 已在这个规则上踩过一次）。
- **检测状态按未实现处理**：`ChannelSummary` 不包含检测状态，也不给占位值——PRD CH-02 要求它，但检测属未交付里程碑，只能缺省不报，不能编。
- **`Channel.UpdateInput` 改指针的连带修改**：`tests/integration/channel_test.go` 三处 `ChannelName: "x"` 需改为 `strPtr("x")`。

## 批次 1+2 的 e2e 规格适配（CI 暴露，必须同步）

真实浏览器验收（`tests/e2e/*.spec.ts`）直接驱动 UI，批次 1 的界面改动没有同步更新规格，导致 CI 的 `browser-runtime`、`media-browser-runtime`、`joint-media-runtime` 三个 job 失败。适配内容：

- `m1a.spec.ts`：`保存名称` → `保存基本信息`，`名称已保存` → `基本信息已保存`。
- `m1b.spec.ts`：`仅保存并测试` → `保存`（并改断言文案为「已保存，未启用」）；移除对 `连接诊断与历史配置应用` 折叠层的点击（诊断区常显）；`应用配置` → `测试并启用`；`保存并连接` → `保存并启用`。
- `m1b-media.spec.ts`：
  - `saveAndTest` 重写：`connect=true` 走一键「保存并启用」；`connect=false` 改为两步——「保存」（只写修订）+「测试取流」（常显面板），再手动「测试并启用」。
  - 页签 `摄像头连接` → `连接配置`。
  - 存储池绑定与录像方式从 `录像设置` 页签迁到 `/recording-plan`：按行定位（`getByTestId('policy-row').filter({hasText:'CH01'})`）+ `CH01 存储池` / `CH01 录像方式` + 行内「绑定」「应用」。新增 `rowCommand` 辅助函数（`command` 依赖全局按钮名，策略页每行都有同名按钮，会撞严格模式）。
  - `应用配置` → `测试并启用`。
  - 删除对已移除的通道内录像索引（`录像结束时间` / `检索录像索引` / `可用` 单元格）的断言；索引改由回放工作区覆盖（该文件末尾的 M1-C 段落已经在断言 `片段` 按钮与真实字节范围）。
  - **行为变化必须体现在规格里**：切换源时若通道正在连续录像，后端仍要求新鲜的存储池写入证据，而连接流程不再替用户预检，因此这类切换改为「保存并测试 → 刷新池证据 → 测试并启用」两步（`freshPoolProof` 紧贴 apply）。
- `apps/web/tests/playwright.ts`：补导出 `type Locator` 供规格使用；`tests/e2e` 不在任何 tsconfig 内，按既有配方逐个手动过类型（已过）。
- **本机无法运行这些规格**（无 Docker / 无真实摄像头），只能由 CI 判定。

## 两个媒体 job 的真实失败原因（已定位并修复）

`browser-runtime` 转绿后，`media-browser-runtime` 与 `joint-media-runtime` 仍失败（run 37799026420 / `3cd9b45`）。判定过程与结论：

- **签名**：失败步骤只走了 **66~94 秒**，而绿灯运行该步骤是 **6m50s / 23m40s**；两个 job 的产物（`media-test-results/*.json`）**完全没有产出**。产物缺失 + 秒级失败 ⇒ 脚本在产出收据之前就退出了，其中一项（media-browser）在**准备阶段**。
- **时间常数**：`m1b-media.spec.ts` 设了 `test.use({actionTimeout: 15000})`，而各次失败耗时 = 准备耗时（浮动）+ **固定 15000ms**，说明失败点是一次元素等待超时，不是断言值不符。
- **根因**：批次 1 把「子流路径」改成「**子流路径（可留空）**」（与既有 `分组（可留空）` 同一风格），但只同步了 `m1a`/`m1b` 两个规格，漏了 `m1b-media.spec.ts:59` 的 `getByLabel('子流路径', {exact: true})`。`Field` 渲染的标签就是 `label` 原文，`exact: true` 不容忍多出的括号，于是 `fill` 等到 15000ms 超时，`saveAndTest`（首次在 `line 129` 调用）失败，`set -e` 让脚本立刻退出 —— 收据自然没有。**只有这一个规格引用该标签，也正好只有跑它的这两个 job 失败**，因果闭环。
- **同类全量核对**：把 5 个规格里所有 `getByLabel/getByRole/getByText/getByTestId` 字面量与 `apps/web/src` 里的 `aria-label`/`label` 字面量做了一次全量比对，`子流路径` 是**唯一**的精确不匹配项（其余 miss 是 `CH${...} 录像方式`、`测试状态：通过` 这类模板拼接的误报）。
- **附带修掉一个竞态**：批次 1 把「仅保存并测试」拆成「保存」+「测试取流」两次点击后，保存响应到达时页面未必已把测试面板绑到新修订，可能点到旧修订（`expect(pathname.endsWith('/source-revisions/'+revision.id+'/test'))` 会因此失败）。改为先等页面报出保存结果（该提示在 `setSelected` **之后**才渲染），再点「测试取流」，把竞态变成确定顺序。
- **顺带确认的两件事**（都不需要改）：`PolicyRow` 的 `If-Match` 用 `status.data.version` / `policy.data.version`，二者都是 `channels.version`（`GetPolicy` 取 `c.version`、`statusTx` 取 `c.version`），与旧实现一致；测试证据有效期 **5 分钟**，夹一次存储池检查足够，而存储池证据 30 秒且 `freshPoolProof` 贴着 apply，顺序正确。

## 让媒体验收失败可从公开注解定位

`actions/jobs/<id>/logs` 需要仓库管理员权限（403），而 job 的 **check-run annotations 可以匿名读取**（本次正是靠它确认产物缺失）。因此 `deploy/production/test-media-e2e.sh` 现在：

- 按阶段推进（`stage=compose-config/postgres/permissions/camera/media-init/media-launcher/stack-up/entry/browser/joint-runtime/receipts`），失败时用 `::error::` 注解报出阶段名；
- 浏览器输出经 `tee` 落到 `/tmp/one-nvr-media-browser.log`，失败时把 Playwright 自己的失败块（测试标题、`Error:`、call log、`at …spec.ts:NNN`）最多 12 行、每行截断 240 字符写成注解 —— 下次失败不用再猜；
- 原先四处**没有任何输出**的 `exit 1`（camera 地址、hook 对端地址、worker 身份、入口不可用）补上原因；
- 注解只含固定阶段名与 Playwright 报错文本，不含源地址、凭据、媒体 key 或页面内容。

# 批次 3（概要，本批不实施）

- [ ] 新增 `internal/onvif`：局域网发现（WS-Discovery）与设备能力探测（GetCapabilities / GetProfiles）。
- [ ] 新增扫描端点与前端「ONVIF 发现」方式：发现 → 选中设备 → 用探测到的 RTSP 地址与凭据预填 → 仍走既有的测试/启用链路。
- [ ] 占位入口届时转正；未通过能力探测的设备不得显示 PTZ 等未确认按钮（PRD CH-06）。
