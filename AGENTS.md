# one-nvr Agent Handoff

这份文件用于快速接手代码任务。先看本文件，再按改动范围阅读 PRD、决策记录和对应模块；README 与部署文档是操作说明的主要来源。

## 产品状态

- 项目面向单站点、本地部署、16–32 个固定通道。Go + PostgreSQL 管理配置、任务和录像索引；ZLMediaKit（ZLM）取流、录像；Web 前端基于 shadcn-admin。
- Frigate 与 OpenList 是可选模块。README 明确标出的未实现功能（例如录像内容回放、智能事件业务、云归档业务）仍按未实现处理，不能从容器存在推断业务可用。
- 通道号和通道身份固定。更换摄像头修改来源修订，不应丢失通道权限、录像策略或历史录像。

## 项目地图

- `cmd/`、`internal/`、`migrations/`：Go 服务、业务模块和数据库迁移。
- `apps/web/`：React/TypeScript 管理界面、API 类型与浏览器单测。
- `compose.yaml`、`deploy.sh`：根目录正式部署入口。
- `deploy/production/`：生产镜像、硬件探测、CI/隔离验收脚本和开发入口 `dev.sh`。
- `tests/`：数据库集成、媒体和真实浏览器端到端验收。
- `docs/PRD.md`：产品范围；`docs/M1-B-rulings.md` 与 `docs/M1-B-decisions.md`：已裁定的行为及验收边界；`docs/M1-B-validation.md` 与专题验证文档：实际验证证据。

## 开发约定

- 改功能前先核对相关 ruling/decision，保留已有安全边界、权限校验、审计和并发版本检查。通道配置变更应沿用现有 source revision、测试证明和 `If-Match` 流程；不要另造旁路直接启用媒体源。
- 改 API 时同时检查 Go handler/service、OpenAPI 契约、迁移和 Web 生成类型。API 类型生成脚本位于 `apps/web/scripts/generate-api.mjs`。
- Web 开发使用 `deploy/production/dev.sh` 提供的项目命令；根目录部署使用 `./deploy.sh`，不要以裸 `docker compose up` 代替部署初始化流程。
- 镜像和系统依赖按 `docs/image-versions.md` 锁定。修改版本、网络、权限或存储行为时，同步更新部署文档与对应验收。
- 摄像头密码、`.env`、私有运行目录和现场录像都是敏感数据。不得把它们的值复制进日志、测试 fixture、截图、提交或 handoff；使用虚构 fixture。
- 测试必须使用隔离数据库、Compose 项目和合成摄像头，不能指向用户的 NAS 数据或真实录像。Docker/真实媒体验收优先走 GitHub Actions；单元测试或 mock UI 不能替代端到端媒体验收。

## 开发流程

1. **接手任务**：查看 `git status`、当前分支和最近提交，保留已有改动；阅读用户最新要求、相关 PRD/裁定及被改模块，确认本次验收结果。
2. **定范围**：找到当前实现、API/数据流和现有测试，列清需改变的行为及不能回归的约束。涉及多个层时，标出 Go/API、迁移、Web、部署和文档的对应改动。
3. **实现并覆盖回归**：先写能复现问题的测试，再做最小实现；沿用现有权限、审计、请求幂等键、版本条件和回滚路径。不要只改 UI 文案掩盖后端状态问题。
4. **验证**：先运行受影响模块的定向测试，再运行本文件“常用验证”里适用的检查。改取流、录像或部署时，等对应真实容器 CI job 完成；失败就查看具体 job 日志，修复后重跑相关检查。
5. **交付**：检查完整 diff、敏感信息和 `git status`，同步必要的决策/操作文档；报告改了什么、实际通过了哪些命令或 CI、还有哪些现场项没验。只在用户当前或既有授权覆盖时推送/更新 PR；不擅自合并或操作用户的生产站点。

## 常用验证

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

按改动选择必要检查；媒体、录像连续性、证书或部署入口变更还要查看 `.github/workflows/foundation-ci.yml` 中对应真实容器验收 job。报告通过情况时写明具体命令、CI run/commit 和测试范围；没有跑过的现场设备或容量测试要明确标作未验证。

## 当前交接快照（2026-10-08）

- 当前主题：**摄像头接入流程与录像计划拆分 + 通道属性与启停**（计划：`docs/superpowers/plans/2026-10-08-camera-flow-rework.md`，批次 1、2 已完成，批次 3 = ONVIF 真实现未做）。
- 用户裁决（2026-10-08，权威，不可自行改动）：录像计划做成**侧边栏一级菜单**并**按通道管理录像方式（手动/定时/事件）且可批量应用**；**「手动」= 现有连续录像**（复用 `continuous` 语义）；**定时与事件只做占位禁用**；**ONVIF 只做占位**；**「配置保存」与「使用」是两件事**，UI 与文档**不再出现「草稿」字样**，未启用的配置状态叫「已保存，未启用」；**「未启用」分两级**——通道级启用/停用（`channels.enabled`）与配置级已应用/未应用（`current_revision_id` 是否指向它）。
- **批次 1**（提交 `cb7bd9a`）：
  - 侧边栏按功能分组为 **监控 / 配置 / 系统**（`components/layout/data/sidebar-data.ts`）；`app-sidebar.tsx` 与 `command-menu.tsx` 都改为**整组为空则不渲染**，可见性统一走声明式 `everyRole`（command-menu 此前是硬编码 url 白名单）。
  - 新增侧边栏一级菜单**「录像计划」**（`/recording-plan`，`features/recording-plan/`）：按通道选 关闭录像 / 手动（连续录像）/ 定时（禁用占位）/ 事件（禁用占位），含存储池绑定与「检查存储池」，支持勾选多通道批量应用。
  - 通道配置页 Tab 收敛为**「连接配置 / 历史与诊断」**；删除「录像设置」Tab、`连接诊断与历史配置应用` 折叠层、`RecordingPolicyControls` 与 `RecordingsIndex`（文件已删除）。
  - `connectSource()` **不再自动检查存储池、不再读取录像策略**；`first_recording_mode` 固定 `none`（接入只取流）。
  - `source-form.tsx`：新增「添加方式」RTSP 手动（可用）/ **ONVIF 发现（禁用占位）**；`onvif_port` 移出表单但**原样保留已有值**；按钮拆为「保存并启用」（主，DOM 在前，回车默认触发）与「保存」（只写配置）；去掉 `order-last`。
  - `channel-status.tsx`：`ChannelStatus` 只渲染主流/子流；`unknown + observation_missing` 显示为**「未测试」**、`stale` 显示为**「观测过期」**。
  - 新增 **`PUT /api/v1/recording-policies`**：单事务内为 N 个通道各入队一个 job，**逐行鉴权与版本检查**，被拒行只回 `{rejected, error_code}` 且不回滚已接受行；基础设施错误则整批中止。
- **批次 2**（迁移 `0011_channel_group.sql`）：
  - `channels.channel_group`（新列）+ **`channels.enabled` 终于有写入口**（该列自 `0005` 起只被 `recording/scheduler.go`、`monitor.go`、`live/service.go`、`source_status.go` 读取）。`channel.UpdateInput` 改为指针字段，`Update` 用 `coalesce` 只改传入项；审计按属性逐条写 `channel.renamed` / `channel.regrouped` / `channel.disabled` / `channel.enabled`。
  - 停用语义：只停取流与录像，**不改 `current_revision_id`、不改录像策略、不删历史**（集成测试有断言）；约 30 秒内生效；前端两处入口都走 `ConfirmDialog`。
  - **`GET /api/v1/channels/summary`**：把「1 次列表 + 每路 1 次 status 轮询」换成一次请求，含 分组 / 权限 / 源摘要 / 三种状态 / `last_error` / `bitrate_kbps` / `updated_at` / `current_revision_id`。实现上抽出 `statusTx`（无鉴权的推导内核）供 `GetStatus` 与 `Summaries` 共用，**保证单路与批量不会分叉**；三种 kind 的观测合并为一次查询。
  - 通道列表补 分组列 / 分组筛选 / 批量分组 / 码率 / 最近错误 / 更新时间，以及启用/停用按钮；配置页「基本信息与连接」面板含 名称 + 分组 + 停用。
  - 时间显示复用 `features/playback/zone.ts` 新增的 `formatMinute`（YYYY-MM-DD HH:mm，站点时区；时区未就绪显示 `—`）。
  - 契约：`m1a` 的 `Channel` 补两字段、`PATCH /channels/{id}` 请求体三字段改可选；`m1b` 新增 summary 端点与 schema。**`UpdateChannelInput` 不再强制 `channel_name`**，这是有意的契约放宽。
  - **检测状态按未实现处理**：`ChannelSummary` 不含检测状态，也不给占位值。
- 批次 3（未做）：ONVIF 局域网发现与能力探测（CH-06），占位入口届时转正。
- 交接时必须先看最新一轮 CI 结论，不要假定通过：真实容器与媒体验收只能在 GitHub CI 运行（本机无 Docker daemon）。**本机 PostgreSQL 17.10 可用**，`tests/integration` 可直接跑：`set -a && . ~/.one-nvr/test-pg.env && set +a && go test ./tests/integration -count=1`。

## 上一轮交接快照（2026-10-07，M1-C）

- 当前主题：M1-C 历史录像回放。M1-B 的固定通道接入、录像与索引留在 `codex/m1b-channel-recording`（草稿 PR [#2](https://github.com/zhigu34/one-nvr/pull/2)）。
- M1-C 已完成读路径、回放工作区与真实容器验收：`GET /api/v1/recordings/{id}/content` 由 501 变为受授权的单区间 Range 交付——授权与解析同事务、五类失败语义（404/409/503）、只服务单区间、审计先于第一个媒体字节；前端新增 `/recordings` 回放工作区（时间轴、缺口提示、±10 秒、0.5/1/2/4 倍速、跨分片续播、即时回放入口），侧边栏新增「录像回放」并改为声明式 `everyRole` 可见性。检索时间**不写死时区**，一律按 `GET /api/v1/site` 返回的站点 IANA 时区解释与显示，换算集中在 `apps/web/src/features/playback/zone.ts`。决策与边界见 `docs/M1-C-decisions.md`，验收矩阵与证据见 `docs/M1-C-validation.md`。
- 本地开发分支 `codex/m1c-playback`，提交 `fc6abf3`（发布到 GitHub 对应提交 `11846e0`，远端分支 `codex/m1c-playback`；跟踪远端用本地分支 `codex/m1c-ci`）。
- CI 两轮均全绿：`f86cf7e` 的 [37655395677](https://github.com/zhigu34/one-nvr/actions/runs/37655395677)（读路径与工作区本体）与 `11846e0` 的 [37660099874](https://github.com/zhigu34/one-nvr/actions/runs/37660099874)（时区修复与回放专用断言，11 个 job）。接手时仍应看最新一轮的结论，不要假定通过。
- 未完成：两台 NAS 上的真机部署与现场播放验证（需用户执行，CI 通过不代表现场完成）；回执 artifact 的具体数值未下载核对；16–32 路并发播放容量与云端统一播放仍不在承诺范围；是否为 M1-C 开 PR 尚未决定。
- 本地没有可用的 Docker daemon，也没有真实摄像头：真实容器与媒体验收只能在 GitHub CI 运行。单元测试和 mock 过的 UI 不能替代端到端媒体验收。
- `tests/e2e` 不在任何 tsconfig 的 include 内，CI 不对它做类型检查。改动这些规格时必须手动过一遍类型，例如：
  `cd apps/web && npx tsc --noEmit --ignoreConfig --strict --target ES2020 --module ESNext --moduleResolution Bundler --lib ES2023,DOM,DOM.Iterable --types node --skipLibCheck ../../tests/e2e/m1b-media.spec.ts`
- 早前记录的「保存并连接」一键接入（提交 `0e55fb8` / 发布 `b59e613`，CI run `37596104311` 11 个 job 全绿）是 M1-B 的现场验证入口；该按钮在 2026-10-08 更名为**「保存并启用」**，编排逻辑未变。
