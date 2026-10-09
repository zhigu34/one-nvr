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

## 当前交接快照（2026-10-09）

- 当前主题：**摄像头接入（RTSP / ONVIF 手动）+ 录像计划拆分 + 通道列表参数显示**（计划：`docs/superpowers/plans/2026-10-08-camera-flow-rework.md` 批次 1、2；`docs/superpowers/plans/2026-10-09-onvif-manual-add.md` 批次 3；`docs/superpowers/plans/2026-10-09-channel-media-params.md`，均已完成）。
- 用户裁决（2026-10-08，权威，不可自行改动）：录像计划做成**侧边栏一级菜单**并**按通道管理录像方式（手动/定时/事件）且可批量应用**；**「手动」= 现有连续录像**（复用 `continuous` 语义）；**定时与事件只做占位禁用**；**ONVIF 批次 1 只做占位**（2026-10-09 用户新指示：改为「ONVIF 手动添加 + 码流自动填充」，不做局域网扫描，见批次 3）；**「配置保存」与「使用」是两件事**，UI 与文档**不再出现「草稿」字样**，未启用的配置状态叫「已保存，未启用」；**「未启用」分两级**——通道级启用/停用（`channels.enabled`）与配置级已应用/未应用（`current_revision_id` 是否指向它）。
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
- **批次 3**（计划 `docs/superpowers/plans/2026-10-09-onvif-manual-add.md`）：ONVIF 手动添加 + 码流自动填充。
  - 用户口径（2026-10-09，权威）：**不做局域网发现/扫描**（部署是 Docker 桥接网络，组播 WS-Discovery 到不了局域网）；**按 ONVIF 方式手动添加**（保留「添加方式」两个 tab）；**不用手填码流**（主/子流地址由设备返回）。
  - 新增 `internal/onvif/`（SOAP 1.2 + WS-UsernameToken 摘要 + 匿名 `GetSystemDateAndTime` **校准设备时钟**再签发认证请求；`GetDeviceInformation`/`GetCapabilities`/`GetProfiles`/`GetStreamUri`；无代理、禁重定向、限时、限读）。按**局部名**解析 XML，兼容各家前缀。
  - 新增 `internal/channel/source_onvif.go` 的 `ProbeONVIF`：通道 `Configure` 鉴权 + 地址边界（各自独立事务，**不跨网络调用持数据库连接**）→ 5s 预算探测 → 过滤不可用码流 → 审计 `onvif.probed`（只记 ip/onvif_port/profiles/result，**不含凭据**）→ 唯一一处失败映射（操作者可改的 `422`、其余 `503`）。边界或输入被拒**不写审计**（没接触摄像头）。
  - 地址校验抽成 `NetworkPolicy.validateTarget`，`SourceConfig.Validate` 改为调用它 —— 探测与保存共用同一套 `ONE_NVR_CAMERA_CIDRS` 边界与错误码。
  - **不跟随设备给的地址**：`GetCapabilities` 的 Media `XAddr` 与流地址里的主机一律丢弃，只取路径，主机/端口用探测目标（防 SSRF 与越界）。
  - **只落结构化字段**：`GetStreamUri` 返回的整条 `rtsp://user:pass@host:port/path?query` 拆成 `ip / rtsp_port / main_path / sub_path / onvif_port`，凭据丢弃且不进响应（CH-10/CH-11）。
  - 新增 `POST /api/v1/channels/{id}/onvif/probe`（同步 200，每人 20 次/分钟），契约 + `api-types.ts` 同步；前端新增 `features/channels/source-onvif.tsx`，「添加方式」两 tab 转正，探测成功前禁用保存，保存仍走既有 `If-Match` + 测试 + 应用链路（**不绕开**）。
  - 未做：局域网发现、PTZ 控制（只在结果里如实报告能力）、厂商固件/时间/编码批量管理（PRD P2）。**真机已验证**（2026-10-09，大华 `192.168.66.111`：ONVIF 探测 → 保存 → 应用 → 取流整条链路跑通）。
- **2026-10-09 实时预览音频轨竞态修复**（`apps/web/src/features/live/player.ts`）：`ontrack` 曾每次重新赋值 `video.srcObject`；answer 带音频 m-line 时触发两次 `ontrack`，第二次触发 media load algorithm 把 `play()` 打断成 `AbortError`，被误报成「浏览器阻止播放」。修法：每 attempt 只创建并赋值一次 `srcObject`、后续轨只 `addTrack`、`play()` 只请求一次、仅 `NotAllowedError` 映射 `autoplay_blocked`。触发条件是**音频编码被 ZLM WebRTC 接受**（只支持 opus / PCMA / PCMU；PCMA/PCMU/opus 中招、AAC 不中——与 RTSP/ONVIF 接入方式无关）。CI 拦不住此类回归：`tests/media` 合成摄像头只有视频轨（候选下一批：给夹具加 PCMA 音频轨）。已推送 `5914dcb`；**NAS 是否已部署未确认**。
- **2026-10-09 通道管理页：媒体参数 + 接入方式**（计划 `docs/superpowers/plans/2026-10-09-channel-media-params.md`）：列表新增「媒体参数」列（主/子流：分辨率 / 视频编码 / 帧率 / 音频编码，音频按"能否经实时预览出声"着色，琥珀 = 无声；悬停显示观测时间）与「接入方式」列（**`onvif_port` 非空 → ONVIF，空 → RTSP，未配置 → —**）；「摄像头」列精简为只显示 IP（完整地址进 title）。数据来源：**当前生效修订的最近一次成功测试**（`source_tests.result`），零新探测 / 零迁移；音频编码由探测的同一份 ffmpeg 日志解析（`audio_codec`：nil = 旧记录未采集 → 显示「—」，重测补齐；**"" = 确认无音频**）。**旧测试记录没有音频字段**：部署后需对每路重新测试一次才会显示音频编码（视频参数立即可见）。
- **2026-10-10 ONVIF 端口回填修复**（用户真机反馈「ONVIF 添加的摄像头显示为 RTSP」）：ONVIF 方式添加时端口框留空（面板写着「默认 80」）→ 探测按默认 80 成功，但保存只在端口非空时写入 `onvif_port` → 修订里没有 ONVIF 端口 → 列表按判定显示 RTSP。修复：探测成功后把**实际使用的端口**回填进表单（服务端已把默认解析为 80、经响应 `onvif_port` 返回，前端此前未使用该字段）；`ProbeONVIF` 的默认端口改为在入口归一（80），审计如实记实际端口（原来记 0）。**历史数据恢复**：重新「获取码流」→「保存并启用」一次（旧修订按设计不可改，走新修订）。
- **2026-10-10 ONVIF 认证拒绝误报修复**：设备以认证类措辞拒绝探测时（如大华回「The security token could not be authenticated or authorized」，或原因只写在 Code/Subcode 里），旧逻辑识别不到、兜底成「ONVIF 响应无法解析」，误导排查。修复：`authorized()` 改按词干匹配（authoriz\* / authenticat\* / security token / invalid security / credentials），`faultMessage()` 改为收集整个 Fault 子树文本；单测覆盖三种措辞形态（含 Subcode-only）。此后这类失败会明确提示「ONVIF 认证被拒绝，请检查用户名与密码」。
- **2026-10-10 界面骨架 shadcn 对齐（批次 A：布局层 + 通用容器；用户指令「全部改」）**：模板自带的界面能力此前未接入布局，仍是手写实现。本批：顶栏右侧接入 `Search` / `ThemeSwitch` / `ConfigDrawer`（删除手写「切换主题」按钮与 `useTheme`），布局接入 `SkipToMain` 与 `NavigationProgress`；侧边栏用户区改为**头像 + 下拉菜单**（触发按钮 `aria-label="用户菜单"`，菜单项「修改密码」（打开既有 Dialog）/「退出登录」，角色显示中文 管理员/操作员/查看者）；ConfigDrawer 全面本地化（主题/侧边栏/布局/文字方向；cookie 取值与行为不变）；`foundation` 的 `Panel`→`Card` 系列、`Notices`→`Alert`（错误 `variant=destructive`、提示 `role="status"` + 强调色，错误/成功语义不变）。**e2e 影响**：m1a/m1b 的「退出登录」「修改密码」必须先点「用户菜单」展开下拉再点 menuitem（Radix `DropdownMenuItem` 是 `menuitem` 角色，不再有同名 button）。**批次 B 待做**：页面层原生控件统一（select/checkbox/button）+ 死代码组件清理。
- **2026-10-10 页面层控件统一与死代码清理（批次 B）**：页面层不再有原生 `<select>` / `<input type=checkbox>` / 手写 `<button>`（25 / 13 / 8 处），统一走 `foundation/ui.tsx` 新增的 **`SelectField` / `CheckboxField`**（显式 `htmlFor` 关联 + `useId` 兜底 id；空串选项值经 `__empty__` 哨兵往返——Radix 拒绝 `value=""` 的 Item）。**行为要点**：users 创建表单角色改受控并随 `form.reset()` 复位（**Radix Select 不随原生表单重置**）；`<Select name>` 经 Radix 的隐藏原生 select 参与 FormData 提交（既有 `fields(form)` 读取不用改）；live/playback 播放器外壳控件保留原深色 className 与 `aria-label`。**e2e 交互模式变更**：`selectOption()` 全部改写为「点 combobox → 点 `role=option`」（12 处，m1a/m1b/m1b-media）；`getByLabel('时区').toHaveValue` 改 `toContainText`；**checkbox 的 `check()/uncheck()` 无需改**（Playwright 1.59 实测支持 ARIA checkbox：点击 + 校验状态变化）。**死代码清理**：删除 13 个零引用文件（`components/data-table/*` 7 个 + coming-soon / date-picker / learn-more / long-text / password-input（含单测）/ select-dropdown）。**vitest 交互模式**：`userEvent.selectOptions` → `click(combobox)` + `click(getByRole('option'))`；占位模式断言改 `data-disabled`。本机验证：tsc -b --force / vite build / eslint src / vitest（157 全过）。
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
