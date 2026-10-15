# one-nvr Agent Handoff

这份文件用于快速接手代码任务。先看本文件，再按改动范围阅读 PRD、决策记录和对应模块；README 与部署文档是操作说明的主要来源。

## 产品状态

- 项目面向单站点、本地部署、16–32 个固定通道。Go + PostgreSQL 管理配置、任务和录像索引；ZLMediaKit（ZLM）取流、录像；Web 前端基于 shadcn-admin。
- Frigate 与 OpenList 是可选模块。README 明确标出的未实现功能仍按未实现处理，不能从容器存在推断业务可用（README 已于 2026-10-15 更新：回放/事件/云归档都已交付，仍明确列为未实现的是周计划与手动录像、可靠性历史与统计、异常事件全集、备份恢复、容量水位与配额）。**`docs/功能总结.md` 仍滞后**（标题写「截至 2026-10-11」、云归档节标题仍写「M2.1 进行中」、未实现清单漏列下述 P0），判断范围时以本文件快照与代码为准：**PRD §6.12「可用性、录像完整性与可靠性历史」（REL-01～REL-06，整章 P0）、§6.4 的周计划/手动录像（REC-02/03/04）、EVT-04 异常事件全集、SYS-02 备份恢复、SYS-01 的审计 180 天保留、STO-02/03 水位与配额至今没有实现，也没有计划文件。**
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

- **2026-10-15 CI 连续转红的真因已定位并修复（接手先看这条）**：`432fa79..1494a63` 这 16 个提交的 CI **没有一个通过**，但**红的不是产品，是 e2e 规格没跟上 UI 重构**——三个真实容器 job（`browser-runtime`、`media-browser-runtime`、`joint-media-runtime`）各自卡在**两处陈旧断言**上：
  - **① 存储池添加表单加了折叠开关**（`daf15a5` 把「添加存储池」表单收进 toggle，提交按钮文案改「确认添加」）。旧断言直接 `getByLabel('存储池名称').fill(...)` → 表单没展开、元素不存在 → 超时。**两个规格各一处**：`tests/e2e/m1b-media.spec.ts` 与 `tests/e2e/m1a.spec.ts`（`directoryPoolKeepsPendingZLMStatus`）。修法：先点「添加存储池」展开、填完点「确认添加」。
  - **② `live-video-area` 不再注入内联 `aspect-ratio`**（`de6f97e` 改为「格子定大小 + `object-contain` letterbox」，`live/index.test.tsx` 已同步改成断言不存在，但 e2e 漏了）。旧断言 `toHaveAttribute('style', /aspect-ratio/)` 必失败；现在断言容器可见、**不存在**内联 aspect-ratio、且包围盒非零。
  - **时间线（已逐轮核对 GitHub Actions 记录）**：**`d4cb1d8`（2026-10-14 02:46）是最后一次全绿**；此后 25 轮 Foundation CI **无一通过**（17 failure / 8 cancelled），其中 `432fa79`→`7f1da10` 连续 16 轮全红。最新一轮 `1494a63`（run 38693616912）的三个容器 job 全部卡在**上面第 ① 条**——注解逐条指向 `存储池名称` 定位超时。**这不是"CI 环境抖动"**，别用重跑掩盖。
  - **本机验证（2026-10-15 本次审查实跑）**：`go test -race ./internal/... ./cmd/... ./migrations` 全绿；`tests/integration` **全量 887s 通过**（含被改的 storage/pool/recording/policy 用例）；`vitest --browser.headless` **271/271 全过**（38 文件）；两个改动过的 e2e 规格 `tsc --noEmit --ignoreConfig --strict ...` 无错。**真实容器三 job 只能在 CI 验证**（本机无 Docker）——推送后必须确认 `browser-runtime` / `media-browser-runtime` / `joint-media-runtime` 转绿，不要假定。
  - **教训（写死在这）**：UI 重构改了控件结构或布局契约时，**同批必须改 e2e 与 vitest 两处**——`de6f97e` 改对了单测却漏了 e2e，`daf15a5` 两处都没改，于是连续 16 轮 CI 全红且没人在推送前发现。改前端交互结构（按钮文案、表单折叠、被移除的内联样式）时，先 `grep tests/e2e/*.spec.ts` 找同名断言。
- **2026-10-15 控制台视觉与交互批次（`061ad68..1494a63`，8 个提交，纯前端除一处后端修正）**：无新迁移、无新端点，`docs/api/m1b.openapi.yaml` 只补 `ChannelSummary` 两个已有字段。
  - **后端唯一改动（`21801ba`，语义修正，别改回去）**：**纯绑定/换池（`pool_switch`）不再要求新鲜写入证据**——此前它与 `desired=continuous` 同门禁（要求 api/worker/zlm 三探针全 healthy 且未过期），于是 zlm 证据 30 秒一过，「先绑池、后开录」这个页面主推的流程必然 409 `zlm_write_evidence_unavailable`，等于逼迫通道正在推流才能预配置。现在**新鲜写入证据只门控 `desired=continuous`**（真正落盘的时刻），纯绑定只校验池存在且启用。`source_changes.go` 的判定从 `(desired == "continuous" || kind == "pool_switch")` 收窄为 `desired == "continuous"`。**`/channels/summary` 契约补 `storage_pool_id` 与 `recording_mode`**（`statusTx` 本就产出，透传即可），录像计划页因此免除每行两个状态请求。
  - **一次真正的前端性能修复**：录像计划页 32 行原先各发 2 个状态请求（N+1），`daf15a5/4796531` 之后行数据改由页级一次 `/channels/summary` 供给（写入前仍即时读 live version）；批量绑定改**逐通道顺序执行**（并发写会互踩 `If-Match` 版本，是批量失败的次因），已是目标池的行跳过并计数，逐行给出「N 路已绑定 · M 路已在该池 · 失败 CHxx: 原因」。
  - **视觉体系**：全站深色统一为**纯碳黑 zinc（chroma=0）**，`#050506` 背景 + zinc-950/900 层次，`video-*` 令牌同步去蓝相（`7f1da10`）；随后 `23afd08` 给**浅色模式**做双模适配——**面板浅色化、视频画布保持纯黑影院视口**（UniFi Protect 规范），`live` 瓦片内部在两种主题下都是影院黑（`1494a63` 修的是上一批令牌浅色化后瓦片内部变白底灰字的回归）。时间轴 canvas 调色板按 `resolvedTheme` 切换。
  - **页面级重构**（多为「Protect 质感」）：总览页从硬编码「尚未提供业务数据」改为四张真实指标卡 + 组件状态矩阵（中文名、呼吸灯、探针事实）+ 底部左右双栏（活跃通道巡检 / 最近 AI 事件）——**注意：这补上了 PRD DASH 的一部分，但 DASH-01 要求的「可用率/覆盖率」指标仍不存在**（那是 REL 整章的事，见下方遗留）。存储池卡片加多段容量条与内嵌探针事实；事件页加通道胶囊、4 列画廊、hover 快捷处理与**进行中超时收口**（无 `ended_at` 且开始超过 5 分钟按已结束显示，修掉 MQTT end 丢失导致永挂「进行中」）；录像页 `channel` 参数缺省时**默认选中第一个在线通道**（不再落在停用的 CH01 上）。
  - **本轮审查另查实、尚未修的三处缺口（别当成已做）**：① **`source_observations` 与 `recording_gaps` 只增不删**——`monitor.go` 每 10 秒对每个已配置通道 `Reconcile`，每条路径都无条件 INSERT（`observations.go`），全库 `grep -c delete source_observations` 为 0，32 路约每天 80 万行，且 REL-06 要求的分层保留（明细 7 天/分钟 30 天/事件与缺口 365 天）也不存在。② **`ONE_NVR_OPENLIST_ENABLE=no` 时云归档并未真正暂停**（承接上一条 capability 记录，仍未修）：`internal/app/run.go` 的三个循环与归档端点都无条件运行，`OpenListEnabled` 只在 render/egress/探针/capability 用到；对照检测侧 `FrigateEnabled=no` 时 `d.Detection`/`d.Events` 为 nil、端点 404。后果是 capability 报「功能未启用」而实际仍可注册目标并上传。③ **AI-03 要求「必须展示实际生效参数」（Frigate inertia）未落实**——`internal/detection/` 里没有 `inertia`，前端只有一句注释说明该语义留在检测器。
  - **测试覆盖缺口**：`tests/e2e/` 只有 m1a/m1b/m1b-media/protocol/restart 五份，检测配置、事件中心、归档目标三个页面**没有任何 e2e**（m1a 里只有一处占位端点断言）；`tests/integration/` 无可靠性相关测试。上条教训之外，这也意味着这三个页面的 UI 结构改动目前只有 vitest 兜底。
- **2026-10-14 模块状态如实化（capability 修正）**：`GET /api/v1/capabilities` 与设置页此前把智能检测、云归档都报成 `implementation_pending` /「尚未实现」——那是 M1-A 的占位，在两个模块各自交付了三个批次之后已经不符实，而 PRD §14 明确要求「不能仅因 env=yes 冒充可用」，反向也不能把已交付说成没做。现在四个事实分开报：`enabled`（部署开关）、`implemented`（本版本是否含该模块，**恒为 true**）、`state`（`disabled`/`not_configured`/`healthy`/`unavailable`/`not_available`）、`reason`。`state` 的推导：开关关→disabled；开着但 `detection_configs` 无启用行 / `archive_targets` 无启用目标→not_configured；已配置且依赖健康→healthy（智能检测要求 frigate **与** mqtt 都健康，且**未观测或观测过期都算不健康**——没有证据不等于可用）；已配置但依赖不健康→unavailable 并带组件的 reason。**一个归档目标不可达不让整个云归档模块变红**（PRD §14：其他 WebDAV 仍可用），只要有任一启用目标 last_check 为 healthy 就是 healthy。设置页按 PRD 四态渲染（功能未启用 / 待配置 / 运行中 / 运行异常 / 版本尚未提供），待配置时给出去配置的链接，reason 作为支持用代码小字保留。占位端点 `POST /archive-tasks`、`POST /detection-rules` 保留管理员+模块门禁（未启用仍 409 `feature_disabled`），启用后改为 501 `endpoint_not_available` 并在消息里指向真实端点（`/recordings/archive`、`/archive-targets`、`/detection/rules`），不再谎称功能未实现。**已查实的遗留缺口（未在本次修，别当成已做）**：`ONE_NVR_OPENLIST_ENABLE=no` 时 PRD 要求「停服务并暂停所有云端动作」（PRD §1060/§1100：暂停上传、云回放/下载/导出、远端到期删除），但代码里归档端点（`/archive-targets`、`/recordings/archive`、`/archive-policies`）与三个 worker 循环（`recording.archive_upload`、`runArchiveScheduleSweeper`、`runCloudExpirySweeper`）都**无条件运行**——`OpenListEnabled` 目前只在 render（是否起 openlist 容器）、egress 白名单、运维探针和 capability 里用到。后果：默认 no 的部署仍能注册目标并上传。**注意 capability 侧因此会报「功能未启用」而实际仍可操作**——两者需要一起修（要么让归档动作真的按开关暂停，要么把 state 改成不声称 disabled）。智能检测侧没有这个问题：`d.Detection`/`d.Events` 在 FrigateEnabled=no 时是 nil，端点返回 404。
- **2026-10-14 M3 批次 3：区域、规则与误报标记（AI-02/03/04/05/06）**（计划：`docs/superpowers/plans/2026-10-13-intelligence-batch3.md`）。迁移 `0021`（`channel_zones` / `detection_rules` / `event_rule_hits`，命中表 `UNIQUE(event_id,rule_id)` 即 AI-05 去重栅栏）。
  - **职责边界不变**：one-nvr 只做三件事——把归一化多边形交给 Frigate（渲染进 `cameras.<ch>.zones`）、把 Frigate 的 `entered_zones` 读回来、在事件之上做业务规则。**绝不自算几何相交**（AI-03 prohibition）。规则三类映射 Frigate 语义：`appear`（无区域）/ `enter`（`entered_zones` 含该区域）/ `dwell`（enter + 阈值，即 Frigate loitering）。
  - **判定口径（重要，别改回去）**：Frigate 把每路源流**拉伸**进固定检测帧（`frame_shape = detect.height/width`，640×360），所以归一化坐标对分辨率免疫；`needs_calibration` 的条件是**源流画面比例变化**（换码流配置/传感器旋转/换镜头），不是像素数变化，两侧任一未知时不报。参考几何 `reference_width/height` **由服务端从该通道最近一次成功的 source test 读取**（`currentFrameSize`，按 `detection_configs.stream` 选 main/sub），**不接受客户端提供**，无测试成功时存 0。
  - **画布取帧**：新端点 `GET /api/v1/detection/frame?channel_id=`（configure 权限，`Cache-Control: no-store`）代理 Frigate `/{camera}/latest.jpg?height=360`，即检测器实际分类的那一帧；通道未启用检测 → 409 `detection_frame_not_ready`，检测器不可达 → 503。**不从摄像头抓快照**（另一路流、另一个尺度、视野可能不同）。
  - **新端点**：`GET/POST /detection/zones`、`DELETE /detection/zones/{id}?channel_id=`、`GET /detection/frame`、`GET/POST /detection/rules`、`DELETE /detection/rules/{id}?channel_id=`、`PATCH /event-hits/{id}`（误报标记）。区域删除**级联删除引用它的规则**。规则列表要 `playback`，其余写操作要 `configure`。
  - **停留阈值必须落到检测器**：Frigate 把「停留」实现为区域自己的 `loitering_time`（按检测帧率累计对象在区域内的帧数），并**用它同时门控 `zones` 与 `entered_zones`**。因此一个区域只能有一个停留阈值，且该区域上不能再有「进入」规则（进入会被静默延后）——两个方向都在保存时拒绝（`rule_dwell_conflict` / `rule_enter_on_dwell_zone`），前端编辑器提交前即显示冲突并禁用保存。`renderZones` 把阈值写进 `zones.<名称>.loitering_time`（无停留规则的区域不下发该键）。
  - **事件详情返回 `hits`**（`Event.hits,omitempty`，列表不带），命中原因 `reason` 冻结在命中时刻，规则后续编辑不改写历史。误报标记只记运营反馈，不重训、不改规则。
  - **前端**：检测页每通道行折叠区「区域与规则」——`features/detection/zones.tsx`（画布 + 已存区域列表）、`features/detection/rules.tsx`（规则列表 + 编辑器）、`features/events/index.tsx` 详情抽屉「命中规则」+ 误报标记。`vite.config.ts` 的 `optimizeDeps.include` 新增 `@radix-ui/react-dropdown-menu`（不预打包会导致 vitest 里 Radix 拿到另一份 React，hooks dispatcher 为 null——以后在测试里首次渲染新的 Radix 包都可能踩同一个坑）。
  - **本机验证**：`go test -race ./internal/... ./cmd/... ./migrations` 全绿；`tests/integration` 全量 885s 通过；web `tsc -b --force` / `eslint src` / vitest 239 全过（新增 zones 3、rules 4、events 3、detection 2）。**未做**：通知/Webhook（EVT-05，留批次 4）、实时目标框、越界/计数/人脸/车牌、误报自动调参。
  - **CI 教训（写死在这，别再犯）**：`production-runtime` 的「Frigate 种子属主」这一步自加入起连续多轮变红，**真因不在产品而在该步骤自身，且有两层**：① `::error` 被无条件发出，于是断言全过也会把这步判红；② 更隐蔽的一层——该步的种子故意把 `/data/frigate` 与 `config.yml` 交给 10001 且 0700（这正是它要证明的状态），而这样的目录 runner 用户删不掉，于是 `trap 'rm -rf "$root"' EXIT` 在检查完全成功之后失败，步骤照样以 1 退出。**产品侧从头到尾都是对的**（`config_ownership=10001:10001 600`、`dir_ownership=10001:10001`）。修法：`::error` 只在失败分支发、通过发 `::warning` 携带测量；EXIT trap 的清理改尽力而为（`|| true` 收尾），注释写明「清理不得决定判定」。
  - **CI 诊断基建**：新增 `deploy/production/ci-run.sh`——跑一条验收命令，输出用 `tee` 实时进日志（超时被杀也保留已产生部分），失败时额外把末 25 行压成 `::error` 注解（job 日志要认证才能读，注解不用），通过不注解。已接入 production-runtime 的部署步骤与 browser-runtime 的镜像构建/浏览器单测/三条 e2e。新增 CI 步骤时用它包一层，别再出现只有「exit code 1」的红。
  - **本轮最终 CI**：`b4540b2` **11/11 全绿**（production-runtime 自该检查加入以来首次通过）。
- **2026-10-12 M3 批次 1：事件接收链路与事件中心**（计划：`docs/superpowers/plans/2026-10-12-intelligence-batch1.md`）。迁移 `0018`（events / event_handling / frigate_cameras / event_stream_gaps）。新包 `internal/events`：自实现 MQTT 3.1.1 客户端（零新依赖）、Frigate 事件归一（幂等 + 单调前进 + 未映射记审计）、90 天到期（未处理不延长、受保护重叠例外）、抓拍 HTTP 代理。新端点：`GET/PATCH /events/{id}`、`/events/{id}/snapshot`、`GET/PUT /event-mappings`。前端「事件」一级菜单（卡片+详情抽屉+处理+跳转回放）。**未做**（批次 2/3）：检测配置/区域编辑/规则（AI-01..07）、通知/Webhook（EVT-05）、异常事件全集（EVT-04）、事件录像前录、智能回放跳播、人脸/车牌。**Frigate 容器首次有业务接线**——但只有 FrigateEnabled 时 worker 才起订阅循环。
- **2026-10-11 云归档批次 3（M2.1 收尾）**：播放租约（`recording_playback_leases`，content 登记+心跳端点+三处清理拦截，理由 `lease_held`）、本地安全清理（归档通道独立 48h 窗口 `sites.archive_local_retention_hours` + verified 副本 + 远端可达 gate，无绑定通道维持 days 语义）、仅云端回放（`/content` 回退代理，deleted+无副本仍 404）、云端到期清理（`cloud_expires_at`=结束时间+保留天数，每小时扫描，保护/租约例外，等待上传过期即跳过）。迁移 `0017`。**M2.1 出口条件全部可验证**；剩余：远端全量对账（ARC-07 部分）、云源导出、容量配额（STO-02/03）。
- **2026-10-11 交接补充**：功能总结文章在 `docs/功能总结.md`（按模块功能+逻辑+未实现清单，随批更新）；**下一批是云归档批次 3（M2.1 收尾），计划已定稿在 `docs/superpowers/plans/2026-10-12-archive-batch3.md`**（播放租约、本地安全清理、仅云端 Range 回放、云端到期清理），可直接照此实施。
- **2026-10-11 云归档批次 2（M2.1 第二段）**：通道归档策略与自动上传调度（计划：`docs/superpowers/plans/2026-10-11-archive-batch2.md`）。迁移 `0016`（`archive_policies` PK=channel_id + `archive_schedule_runs` 去重账本）。三种模式：manual/scheduled/immediate；**立即上传经发布事务钩子**（`recording.Service.OnPublished`，失败只记日志）；**定时调度 30s 扫描 + UNIQUE 账本去重**（站点时区每日时刻，单次触发入队 ≤100）。入队内核三路共用（`archive.Service.enqueueUpload`）。前端归档页加「通道归档策略」表。**未做**（批次 3）：本地安全清理+播放租约（ARC-05）、仅云端回放（ARC-04）、云端到期清理（ARC-09）、远端对账、云源导出、配额。
- **2026-10-11 云归档批次 1（M2.1 第一段）**：WebDAV 目标 + 手动上传链路（计划与裁定：`docs/superpowers/plans/2026-10-11-archive-batch1.md`、`docs/M2-decisions.md`）。迁移 `0015`（`archive_targets` 凭据加密三列 + `recording_archive_copies` + `channel_grants.archive`）。`internal/archive/`（WebDAV 客户端 PUT/GetRange/Delete/Check、目标 CRUD、`HandleUpload`）；worker 两个 `recording.archive_upload` 循环；管理页「上传到云端」+「本地+云端」徽标；侧边栏「归档目标」（admin）。**上传权限是独立 grant（`auth.Archive`）**，admin 全链路默认拥有（setup/SetGrants 强制）。**未做**：定时/立即调度、安全清理+播放租约、仅云端回放、云端到期清理、远端对账、云源导出、配额。
- **2026-10-11 导出与证据批次（M2 收尾）**：范围导出全链路交付（计划与裁定：`docs/superpowers/plans/2026-10-11-recording-export.md`、`docs/M2-decisions.md`「导出与证据批次」）。迁移 `0014`（`recording_exports` / `recording_export_artifacts` / `recording_export_sources`）；新端点 7 个（`/recording-exports` 的 analyze/create/list/get/download(ZIP+清单)/artifact content/artifact 保护）+ `POST /recordings/protect-window`（EXP-04 窗口保护，admin）。worker 两个 `recording.export` 消费循环 + 30s 对账；ffmpeg 原码拼接在 probe 包（`Runner.ConcatMP4`，`recording.Probe` 接口加了方法——**改 Probe 假实现要一起补转发**）。前端 `features/recording-exports/`（对话框挂回放选中片段卡「导出」按钮，抽屉挂管理页工具栏「导出记录」），权限走通道级 `export` grant。**未做**：精确裁剪转码、长时段自动拆分、云源导出（M2.1）。
- **2026-10-11 分支收敛（用户裁定，以后直接在 `main` 开发）**：全部工作已合并进 `main`（快进自 `codex/m1c-playback`，含回放修复批次 `7f76a4c`），本地与远端的 `codex/*` 分支已删除。**旧的「本地工作分支 + `codex/m1c-ci` 镜像发布」双历史惯例废止**——此前远端分支与本地是同补丁两套 SHA，那是镜像发布造成的，不是历史损坏；一切以 `main` 单线历史为准。
- **2026-10-11 回放修复批次**（`7f76a4c`，裁定见 `docs/M2-decisions.md`「2026-10-11 修复批次」）：回放索引按 `next_cursor` 翻页取全窗口（每页 500、上限 2000 段、触顶显式警告），未取回的页不再伪装成缺口；`GET /recordings` 分页上限 500/默认 100 在 HTTP 层落地（`paginationLimit`）；倍速跨片段保持（浏览器装载新资源重置 playbackRate，`ready()` 重应用）；未播放位置显示 `—`、时间轴红线首次播放前不渲染；底部文案改「当前通道」；时间轴空态合并到页级。**未做**：录像管理页仍单页 100 条不翻页（摘要为全量统计可对照）。
- **2026-10-11 M2 批次 1：录像文件管理 + 保留策略 + 删除**（决策见 `docs/M2-decisions.md`）：侧边栏「录像回放」改名**「录像」**，下分**「回放 / 录像管理」两页签**（`features/recordings-workspace/`，两面板都保持挂载、隐藏非当前页，切页签保留通道与时间）。新迁移 `0013`（`sites.recording_retention_days` 默认 0=不自动删除、`recording_segments.protected/protected_at` + `state` 增加 `deleted`、`recording_locations.deleted_at/delete_reason`、两个部分索引）。新端点：`GET /recordings/stats`、`PATCH /recordings/{id}`（保护，管理员）、`POST /recordings/batch-delete-local`（管理员、逐项结果 + `released_bytes`）；`GET /recordings` 增加 `source_revision_id`/`pool_id`/`state`/`protected` 筛选，limit 上限 100→500。**删除语义**：先 unlink 后改状态（崩溃→既有对账标 `missing`），`deleted` 与 `missing` 严格区分，删除后 `/content` 返回 **404 not_found**（不是 409）；保留策略跳过受保护与发布不足 10 分钟的片段，**显式删除不受该延迟限制**。管理页只对管理员开放。用户裁决：保留策略**默认关闭**、菜单**合成一个「录像」一级入口 + 两页签**。本批未做：云归档、范围导出、容量压力淘汰与配额、播放租约、时间轴源切换标记（**按源筛选已做**）。迁移执行依赖 go1.27 idiom；`protected` 与 `media` 现在是 `RecordingSegment` 的必填字段，改夹具要一起补。
- 上一轮主题：**摄像头接入（RTSP / ONVIF 手动）+ 录像计划拆分 + 通道列表参数显示**（计划：`docs/superpowers/plans/2026-10-08-camera-flow-rework.md` 批次 1、2；`docs/superpowers/plans/2026-10-09-onvif-manual-add.md` 批次 3；`docs/superpowers/plans/2026-10-09-channel-media-params.md`，均已完成）。
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
- **2026-10-10 界面骨架 shadcn 对齐（批次 A：布局层 + 通用容器；用户指令「全部改」）**：模板自带的界面能力此前未接入布局，仍是手写实现。本批：顶栏右侧接入 `Search` / `ThemeSwitch` / `ConfigDrawer`（删除手写「切换主题」按钮与 `useTheme`），布局接入 `SkipToMain` 与 `NavigationProgress`；侧边栏用户区改为**头像 + 下拉菜单**（触发按钮 `aria-label="用户菜单"`，菜单项「修改密码」（打开既有 Dialog）/「退出登录」，角色显示中文 管理员/操作员/查看者）；ConfigDrawer 全面本地化（主题/侧边栏/布局/文字方向；cookie 取值与行为不变）；`foundation` 的 `Panel`→`Card` 系列、`Notices`→`Alert`（错误 `variant=destructive`、提示 `role="status"` + 强调色，错误/成功语义不变）。**e2e 影响**：m1a/m1b 的「退出登录」「修改密码」必须先点「用户菜单」展开下拉再点 menuitem（Radix `DropdownMenuItem` 是 `menuitem` 角色，不再有同名 button）。**批次 B**（页面层原生控件统一 + 死代码清理）见下条。
- **2026-10-10 页面层控件统一与死代码清理（批次 B）**：页面层不再有原生 `<select>` / `<input type=checkbox>` / 手写 `<button>`（25 / 13 / 8 处），统一走 `foundation/ui.tsx` 新增的 **`SelectField` / `CheckboxField`**（显式 `htmlFor` 关联 + `useId` 兜底 id；空串选项值经 `__empty__` 哨兵往返——Radix 拒绝 `value=""` 的 Item）。**行为要点**：users 创建表单角色改受控并随 `form.reset()` 复位（**Radix Select 不随原生表单重置**）；`<Select name>` 经 Radix 的隐藏原生 select 参与 FormData 提交（既有 `fields(form)` 读取不用改）；live/playback 播放器外壳控件保留原深色 className 与 `aria-label`。**e2e 交互模式变更**：`selectOption()` 全部改写为「点 combobox → 点 `role=option`」（12 处，m1a/m1b/m1b-media）；`getByLabel('时区').toHaveValue` 改 `toContainText`；**checkbox 的 `check()/uncheck()` 无需改**（Playwright 1.59 实测支持 ARIA checkbox：点击 + 校验状态变化）。**死代码清理**：删除 13 个零引用文件（`components/data-table/*` 7 个 + coming-soon / date-picker / learn-more / long-text / password-input（含单测）/ select-dropdown）。**vitest 交互模式**：`userEvent.selectOptions` → `click(combobox)` + `click(getByRole('option'))`；占位模式断言改 `data-disabled`。本机验证：tsc -b --force / vite build / eslint src / vitest（157 全过）。
- **2026-10-10 实时预览媒体信息**（用户要求：分辨率/帧率/是否转码/音频）：实时瓦片底栏在「播放中」显示一行 `2560×1440 · 25fps · 直通 · 音 PCMA`（悬停 title 解释「直通＝未转码」与音频口径），数据来自**浏览器 WebRTC 实时统计**（`RTCPeerConnection.getStats()` 每 2s 采样；`frameWidth/Height` = 实际解码尺寸，`framesPerSecond` 缺失时按 `framesDecoded` 差值折算；codec 表给视频/音频编码）。**「是否转码」如实显示 `直通`**——实时链路从不转码（ZLM WHEP 只做协议打包；不支持的编码走 422 提示改 H.264；P1 硬件转码未实现）。**状态文案调整（用户指示）**：footer 去掉「· 播放中」后缀（播放中显示 `主流`/`子流`；fallback 仍 `主流 · 未配置子流`；其余 `未播放`）。新增 `features/live/media-info.ts`（类型 + 文案），`player.ts` 增加第 5 个构造参数 `media` 回调（停止/重连回 `null`）。**e2e 同步**：m1b-media 的 `'主流 · 播放中'` 断言改 `'主流'`，新增 `live-media-info`（testid）可见性 + `直通` 断言。本机验证：tsc -b --force / vite build / eslint src / vitest（160 全过）。
- **2026-10-10 实时瓦片三处真机修正**（用户截图反馈）：① 码流选择框去掉固定 `w-28`、改**内容自适应**（`w-fit px-2 text-xs`，原来过宽）；② 视频区比例**跟随实际解码分辨率**（复用媒体统计的 `media.width/height` 写 inline `aspect-ratio`，新增 `live-video-area` testid；未播放/未采样时保持 16:9）——非 16:9 摄像头不再有两侧黑边、画面按列宽放大（**不裁剪**）；③ 全屏按钮改**切换**：监听 `fullscreenchange` 并比较 `document.fullscreenElement === 本瓦片`，全屏中按钮变「退出全屏」+ Minimize 图标、点击 `exitFullscreen()`。单测补「比例跟随」断言与「全屏切换」用例（stub `fullscreenElement` 取值器 + `exitFullscreen`）；e2e m1b-media 补 `live-video-area` 的 `aspect-ratio` 断言。**注意**：`live-video-area` / `live-media-info` 这类 testid 在 4/9/16 宫格里**每个瓦片都有**——断言必须收敛到 `getByRole('region', {name: '预览 CHxx …'})` 范围内，否则撞严格模式。
- **2026-10-10 存储池写入证据摩擦修复（用户裁决：「我要的是一切顺利操作，什么叫快」）**：绑定/应用要求 zlm 写入证据 30 秒内新鲜、且必须手动先「检查存储池」→ 用户必须在窗口内连点两步，判为设计缺陷。修法（**服务端零改动**，不削门禁、不加时效）：录像计划页 `run()` 遇 409 `zlm_write_evidence_unavailable` 时自动补一次真实检查并重试一次——`features/storage-pools/pool-check.ts`（新，**放在 storage 域**供两处复用）：`settleJob`（轮询作业到终态/120s 上限）+ `verifyPoolWrite`（POST 检查 → 等作业 → **回读池 checks 确认 zlm healthy**，作业成功≠写入通过）+ `boundOrDefaultPool`（未绑池回退默认池，与后端一致）；批量应用对 `zlm_write_evidence_unavailable` 行按池去重补检查后只重试被拒行，未恢复行显示检查原因；单行接受后的作业也轮询到终态、失败显示原因（补「排队后无下文」缺口）。**P1 同批**：`applyWithPoolRecovery()`（`channels/source-connect.ts`）接入「保存并启用」（`connectSource`）与「测试并启用」（`source-test.tsx`）两条入口。单测补「绑定/批量/摄像头 apply 自动恢复 + 恢复失败给原因」；e2e 首次绑定与切源不再手动预检查（**保留故意回滚那处 `freshPoolProof`**：那是"先证明、后断源"的夹具前置），`command` 与 `rowCommand` 共用 `acceptedCommand`（等重试后的 202、跳过被吞的 409）。**未改**：批量导入的「开启连续录像」入口同属一族。
- 交接时必须先看最新一轮 CI 结论，不要假定通过：真实容器与媒体验收只能在 GitHub CI 运行（本机无 Docker daemon）。**本机 PostgreSQL 17.10 可用**，`tests/integration` 可直接跑：`set -a && . ~/.one-nvr/test-pg.env && set +a && go test ./tests/integration -count=1`。
- **本机 Go 工具链（2026-10-11 起）**：免 sudo 装在 `~/.local/toolchain/go`，用 `export PATH="$HOME/.local/toolchain/go/bin:$PATH"`（会自动按 `go.mod` 取对应版本）。**动 recording/storage/channel 时必须跑全量集成再推**（约 20 分钟；`./tests/integration` 不带 `-run` 全量跑会超时挂起，需分组或加长 `-timeout`）——2026-10-10 有过只分组跑、漏掉 `TestRecordingRecoveryRequiresRecentCompletion` 就推送、CI backend 转红的教训。

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
