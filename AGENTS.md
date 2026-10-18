# one-nvr Agent Handoff

这份文件用于快速接手代码任务。先看本文件，再按改动范围阅读 PRD、决策记录和对应模块；README 与部署文档是操作说明的主要来源。

## 产品状态

- 项目面向单站点、本地部署、16–32 个固定通道。Go + PostgreSQL 管理配置、任务和录像索引；ZLMediaKit（ZLM）取流、录像；Web 前端基于 shadcn-admin。
- Frigate 与 OpenList 是可选模块。README 明确标出的未实现功能仍按未实现处理，不能从容器存在推断业务可用（README 已于 2026-10-15 更新：回放/事件/云归档都已交付，仍明确列为未实现的是周计划与手动录像、可靠性历史与统计、异常事件全集、备份恢复、容量水位与配额）。**`docs/功能总结.md` 仍滞后**（标题写「截至 2026-10-11」、云归档节标题仍写「M2.1 进行中」、未实现清单漏列下述 P0），判断范围时以本文件快照与代码为准：**PRD §6.12「可用性、录像完整性与可靠性历史」已交付完毕——REL-01/02（观测→区间→故障）、REL-03（应录与完整性）、REL-04（八项指标口径）、REL-05（通道可靠性页签、时间带、指标卡、故障表、跨通道比较、总览异常通道、CSV 导出、四类阈值告警）、REL-06（7/30/365/730 天分层保留与汇总）均已实现，率值带分子分母与精度标注**；该章剩余的收尾项只有：录像工作区「从缺口跳对应故障」未接线、趋势图以表格呈现未用折线、告警通知未接入 `notify` 层（站内/外部渠道的投递未接）。此外 §6.4 的周计划/手动录像（REC-02/03/04）、EVT-04 异常事件全集、SYS-02 备份恢复、SYS-01 的审计 180 天保留、STO-02/03 水位与配额至今没有实现，也没有计划文件。
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

**本机快速反馈（首选，约 35 秒）**——本机不需要 Docker 就能跑完类型、lint 和单测：

```bash
./deploy/production/verify.sh          # API 类型漂移 + tsc + eslint + vitest + go 单测 + vet，约 18-35s
./deploy/production/verify.sh --full   # 追加 tests/integration（本机实测约 15 分钟）
```

脚本会自己找到 PATH 外的 node（`/opt/homebrew/opt/node@*`）与 go（`~/.local/toolchain/go`），并在结尾给出逐项 PASS/FAIL 与耗时。**改代码时就跑这个**，不要为了一个类型错误等 CI 往返。

**它必须真的跑完 7 项**（API 类型 / tsc / eslint / vitest / go 单测 / vet / gofmt）。若输出的 summary 只有两项、耗时 1 秒，说明脚本又被人改坏了 —— 这个已经发生过一次：`234043a` 在重写文件时把五条 `run` 调用和 `integration()` 整体删掉，此后近一小时的开发都在「假绿」下进行。**看到的项数不对就先修脚本。**

**改 `internal/app/run.go`（进程装配）时，除了 `go test` 还要实跑一次真二进制** —— 本机没有 Docker，`Run()` 的启动与关停路径没有任何测试执行，REL 批次 1 的 worker 启动 panic 与 WaitGroup 计数错就是这样漏到现场的。至少跑到「进程起来并能干净退出」：起 worker、看日志有 `process started`、`kill -TERM` 后 0 秒退出且 exit code 0。`internal/app/run_waitgroup_test.go` 会静态核对 `background.Add` 与回报 `Done` 的协程数，别绕过它。

CI 只覆盖本机做不到的部分（Docker、真实 ZLM、真实分片、真实浏览器），并按改动路径裁剪：纯文档推送只跑 ~1 分钟的 Compose/shell 校验；纯前端推送跑 `frontend` + 两个浏览器 job；Go/迁移/部署改动才跑完整媒体验收。详见 `.github/workflows/foundation-ci.yml` 顶部注释。发布前要一次跑全，用 `workflow_dispatch` 并置 `full_suite=true`。

需要 Docker 的验收（本机无 daemon，只能走 CI）：

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

按改动选择必要检查；媒体、录像连续性、证书或部署入口变更还要查看 `.github/workflows/foundation-ci.yml` 中对应真实容器验收 job。报告通过情况时写明具体命令、CI run/commit 和测试范围；没有跑过的现场设备或容量测试要明确标作未验证。

## 当前交接快照（2026-10-09）

- **2026-10-18 NAS 无法 `git pull` 部署已修（运维记录，不是代码改动）**。症状是 `git fetch` 反复失败，错误在三种之间跳：`GnuTLS recv error (-110)`、`HTTP/2 stream not closed cleanly`、`Failed to connect to github.com port 443 after 133395 ms`。**根因是 DNS 单点**：NAS 上 `github.com` 只解析到 `20.205.243.166`，而这个 IP 从该网络不可达（`curl` 卡在 `Trying` 之后，20 秒建不起连）。
  - **诊断方法（可复用）**：分别探测 `api.github.com` / `codeload.github.com` —— 两者 0.1 秒返回 200/301，只有 `github.com` 不通，说明不是整体断网而是那一个 IP 被阻断。再用 `curl --resolve github.com:443:<ip>` 试其它 GitHub IP，`140.82.112.3` 0.23 秒连通。
  - **修法**：在 NAS 仓库里 `git config http.curloptResolve github.com:443:140.82.112.3`。这把解析钉在仓库局部，不动 `/etc/hosts`、不影响整机其它程序。
  - **同时清掉了历史分叉**：此前几轮 NAS 连不上 GitHub 时，我是用 `git apply` 打补丁再本地提交的，于是 `main` 上积累了 5 个「(NAS apply)」提交，与远端内容相同但 SHA 不同，`pull` 因此拒绝。**对齐前先验证 `git diff --name-only HEAD origin/main | wc -l` 为 0**（确认两棵树完全一致），再 `git reset --hard origin/main`。现在 NAS 与远端是单线历史，`git pull --ff-only` 可直接用。
  - **钉单个 IP 不够**：可达集合会漂移（同一小时内 `20.205.243.166` 先不通后通，`140.82.112.3` 先通后不通），钉死的配置迟早再次卡住，而且**失效的 pin 会让 `git pull` 挂两分钟以上才报错**。
  - **最终方案是 NAS 上的 `~/deploy-one-nvr.sh`**（不在仓库里，避免污染工作区）：① 先清掉可能失效的 `http.curloptResolve`，用 `timeout 60 git pull --ff-only` 试一次普通拉取；② 失败才逐个探测 `20.205.243.166 / 140.82.112.3 / .113.3 / .114.3 / .116.3 / 20.27.177.113`，钉住第一个能通的再拉。**网络正常时它就是一次普通 pull，探测分支不会执行。**
  - **部署命令**：`ssh zhigu34@192.168.66.90` → `~/deploy-one-nvr.sh` → `sudo -n bash ./deploy.sh`。`deploy.sh` 没有可执行位要用 `bash`，且必须 `sudo`，否则没有 docker socket 权限。
  - **SSH 端口始终是通的**（`ssh -T git@github.com` 能完成握手，只是 NAS 没有部署密钥），必要时可改用 SSH remote 彻底绕开 HTTPS。


- **2026-10-18 录像计划页交互重构（用户实施，提交 `6c4e9c0`）**：把「每行两个未保存草稿 + 两个按钮」改成「模式下拉即应用 + 绑定移到存储页 + 周计划胶囊摘要」。
  - **存储池绑定从行内移走**（新 `storage-pools/pool-binding.tsx`）：每行原本有一个池下拉和「绑定」按钮，与模式下拉并列，操作者要自己判断「我要改的是哪一个」。池的问题是**每个池问一次**（这些字节去哪），不是每通道问一次。行内现在是只读胶囊（池名 + ↗ 链接）；绑定在存储页按池管理，列出该池服务的通道，逐通道顺序 PUT（同批量约定：并发会互踩版本），`zlm_write_evidence_unavailable` 时自动补一次真实检查再重试。**没有新端点、没有新服务端状态**——只是同一个房间的另一道门。
  - **`none`/`continuous` 由下拉直接应用**，不再有「应用」按钮：一个可见的模式切换还要再点一次确认，读起来像两个控件在争同一件事。**`planned` 是例外**：选中即展开编辑器，编辑器里的「保存并启用定时录像」把**整周计划与模式切换一次提交**（先 PUT 计划、再走切换任务），所以通道不会处于「有计划但没人读」的半配置态。
  - **`week-pills.tsx`**：planned 行显示七个方块（有计划的绿色、没有的暗色），悬停给出该日时段；只有全周时段一致时才附一行文字摘要。`mode-format.ts` 统一模式词汇（`none` 关闭录像 / `continuous` **全天录像** / `planned` 定时录像），此前选择器、批量栏、通道配置页各有一份且 continuous 被叫成「手动（连续录像）」。
  - **未配置源通道默认折叠**成一行（列出通道号），点击展开；**选中未配置通道时自动展开**——否则「我看到什么」与「批量运行什么」会不一致。
  - **定时录像的储量修复链改了实现**：批量遇 `zlm_write_evidence_unavailable` 时不再走 `verifyPoolWrite`，改为 POST `/storage-pools/{id}/test` 后轮询池的 `zlm` 检查结果（`pollPoolCheck`，90 秒上限），逐池去重。
  - **验证**：`verify.sh` 7/7 PASS；前端 52 文件 479 测试全过；NAS 已部署（`98975b1`，本地补丁提交，内容与远端 `6c4e9c0` 相同）。**现场页面未逐项点过**——浏览器会话失效需要重新登录，凭据不在本机。


- **2026-10-18 定时/事件录制批次 2 已交付：周计划（REC-02）**。批次 1 交付手动窗口，本批让 `planned` 从占位变成真模式。
  - **计划是规则，不是展开的时刻表**（迁移 `0031`，`recording_schedules`）。存展开后的时刻需要有个任务不断生成未来，而每次重启/停机都会在展开里留下空洞——那些空洞看起来像"计划停机"。规则可以在任意时刻求值，包括进程启动前就已经开始的窗口。
  - **时间是站点本地墙钟，不是 UTC**。08:00 对当地人就是 08:00，含 DST 切换。求值内核 `schedule_rule.go` 是纯函数（`ScheduleActiveAt` / `SplitOvernight` / `MergeOvernightWindows` / `ScheduleWindowsOverlap`），9 个单测覆盖半开区间、跨午夜、周日→周一回绕、DST、以及「两个整天不得合并成一个无尽窗口」。
  - **跨午夜在存储里是两行、在界面上是一段**。每行都能对单一本地日期求值，这才让「此刻是否在窗口内」是一次索引查询而不是跨日 JOIN。编辑器里输入 22:00–02:00，`SplitOvernight` 拆成周一 22:00–1440 + 周二 00:00–120，`MergeOvernightWindows` 还原成一段；**合并必须拒绝 `start=0` 的全天行**，否则每天 00:00–24:00 会塌成一段。
  - **`unionMode` 返回的是「决定」而不是「回显存储的模式」**（本批最关键的修正）。planned 通道在窗口外必须返回 `none`；回显 `planned` 会让调度器走启动录像分支，于是**窗口外也全天录像**，而界面还显示着周计划。为此 `obligationFor` 单独保留 `PolicyMode`：`unionMode` 把「窗口外的 planned」折成 none 是对的，但义务要能区分「操作者关了录像」和「现在不在计划内」——两者都是排除，解释却不同。
  - **`planned` 与 continuous 走同一套门禁**：需要存储池、需要新鲜写入证据、证据过期时同样自动重新验证。计划决定的是「什么时候录」，不是「字节落不落到池里」。
  - **`source_switches.desired_mode` 的 CHECK 漏了 planned**（迁移 `0032`）。0005 里 `old_mode` 已允许 planned 而 `desired_mode` 不允许——一个能作为「旧模式」被记录、却永远不能作为「新模式」被请求的模式。点「应用」时被 CHECK 拒绝，API 把它当成未识别错误返回裸 503。**修法**：放宽约束。**发现它的方法是本轮顺带加的「未知错误记日志」**——日志里有请求路径和 SQLSTATE，而之前浏览器上只有「服务暂时不可用」。
  - **API 构造的是一个没有 siteID 的裸 `recording.Service`**（`router.go`），所以任何依赖 `s.siteID` 的读路径都会以 `invalid input syntax for type uuid: ""` 失败并表现为 503。`siteZone` 改为**通过通道查站点时区**，两个进程共用一份实现。两个集成测试（`TestScheduleEndpointWorksWithTheAPIBareService` / `TestScheduleWriteWorksWithTheAPIBareService`）用 API 同样的构造方式钉住它——**已验证把实现改回 `s.siteID` 会 FAIL**。
  - **新端点**：`GET/PUT /channels/{id}/recording-schedule`（GET 要 playback、PUT 要 configure），响应带站点时区与**服务端自己算的 `active`**——编辑器不重算，否则浏览器时钟会让同一时刻在两个地方显示不同答案。契约与 `api-types.ts` 已同步。
  - **前端**：`schedule-format.ts`（纯函数，12 个单测）+ `schedule-editor.tsx`（按周日列表格、每天可加时段、可「复制到其它日期」）。编辑器**只在 `mode === 'planned'` 时出现**——在 continuous 通道上显示一个会被忽略的计划，读起来像 bug。整周一次 PUT 保存：一个被拒绝的周必须不半应用。
  - **现场验收（NAS，`https://192.168.66.90:8443`）**：设 CH01 为定时录像 + 周日 08:00–18:00（当时 17:49）→ 策略 `planned`、切换任务 `succeeded`、义务区间 `source=planned` 无排除、录像观测 `healthy/recent_completed_segment`、实际落盘；把时段改到周一后 → 自动停录、run 变 `stopped`、义务变 `planned + disabled`、观测 `disabled`。**两个方向都对**。
  - **验证**：`verify.sh` 7/7 PASS；全量集成见下条；已部署 NAS（约束 `desired_mode` 已含 planned，现场确认）。
  - **REC-02 的两种复制都做了**：复制到其它日期（把一天的时段铺到全部七天）与**复制到其它通道**（编辑器底部多选目标通道，逐个 PUT 各自的整周，逐通道报告结果）。跨通道复制按顺序写，与批量绑定同理：并发写照会互踩版本，而且每写一条都有自己的审计行。
  - **未做**：批次 3 事件录制（REC-09/10，需要 ZLM 滚动分片缓冲，参数要真机验证）。


- **2026-10-17 定时/事件录制批次 1 已实现：调度器取并集 + 手动录像窗口（REC-03/04）**。用户要求「继续定时、事件录制功能开发」，拆三批：**① 调度器并集 + 手动录像（本批）**、② 周计划（REC-02）、③ 事件录制（REC-09/10）。
  - **手动录像不是一种 mode，而是一个有起止的窗口**（迁移 `0030`，`recording_manual_windows`）。这是本批最重要的结构决定：`recording_policies.mode` 是设置，手动录像是操作者的一次请求。把请求写进设置会让两者互相覆盖——REC-04 要求「策略编辑不得终止进行中的手动录像」，只有当它们存在不同表里时才天然成立。新表带部分唯一索引 `recording_manual_windows_one_active`（一通道一活跃窗口），重复启动把旧窗口关成 `cancelled` 再开新的，历史仍能看出操作者按过两次。
  - **`expires_at` 在 INSERT 时用 `clock_timestamp()+make_interval(mins => $3)` 算死**，不在读路径按 `now+minutes` 推导——否则一个开了一小时的窗口仍会声称自己有完整时长。`remaining_ms` 是读时算的展示值，不落库。
  - **`unionMode(policyMode, manualActive)` 是 REC-04 的唯一实现点**（`internal/recording/scheduler.go`）。`none` 是「没有任何理由录像」的**缺省值，不是一个会取消其他理由的 mode**：操作者在 mode=none 的通道上按录像必须真的录，运行中改策略也不得中断。调度器与义务推导（`obligations.go` 的 `loadObligationFacts`）**共用这个 helper**——两者若对「现在该不该录」有分歧，每个完整性数字都会以无人察觉的方式变错。
  - **义务侧 `manual` 转正**：此前 `planned`/`manual` 一起被当作「未实现词汇」记为 `exclusion=disabled`。现在 `manual` 是 `source="manual"` 的**应录**区间（分母要算上），只有 `planned` 仍标记 `mode_not_implemented`。判成「不要求录像」会把一次失败的手动录像藏进排除项，并把该通道报成完全覆盖。
  - **权限与审计**：启动/停止要 `configure`，查询要 `playback`；`recording.manual_started` / `recording.manual_stopped` 入审计。`StopManual` 幂等（没在录也返回成功——期望状态已成立）。
  - **`GET` 无窗口时返回 404 `manual_recording_absent`，不是 `200 null`**（`ActiveManual` 透传 `ErrManualWindowMissing` 而非吞掉）。前端据此把「空闲」与「读不到」分开：只有这个 code 才渲染成未录像；其他失败显示「状态未知：…」而不是假装空闲——后者会诱使操作者对一个状态未知的通道盲写。
  - **前端**：`features/recording-plan/manual-recording.tsx`，与录像方式下拉**分开**的控件（10/30/60/120 分钟），运行中显示红色胶囊 + 剩余时间 + 停止，15 秒轮询。
  - **测试**：`tests/integration/manual_recording_test.go` 9 个（不改策略 / mode=none 也产生应录 / continuous+manual 只开一条义务 / 连按两次只留一个活跃窗口 / 停止幂等 / 到期 sweep 记 `expired` / 分钟数边界 / **HTTP 层无窗口必须 404** / **手动窗口必须让该池有资格被 probe**）。前端 14 个（含 3 个新的控件测试）。
  - **现场验收暴露并修掉一个死锁（提交 `fbb10d6`，这是本批最重要的一条经验）**：手动录像一启动就报 `zlm_write_evidence_unavailable`，永远起不来。链条是——① 起录像要求 **zlm 写入证据新鲜**；② 这份证据只能由一次真实录像的回调或 **idle-pool probe** 产生；③ probe 的候选查询写死 `rp.mode='continuous'`（在「连续录像是唯一录像理由」的年代正确），于是**没有任何通道 continuous 的站点永远不 probe**；④ 证据永不刷新，手动录像永远被拒。这就是「新需求被旧假设锁死」——`unionMode` 只改了调度器和义务，probe 是第三个读「有没有人要录像」的地方，漏了。
    - **修法**：抽出 `recordingDemandSQL(poolExpr)` 一处定义（continuous **或** 活跃手动窗口），候选扫描与逐池复核都用它；`unionMode` 是同一条规则在 Go 侧的表达。
    - **顺带修可观测性**：`scheduler.go` 原先在 `Start` 返回 `ErrMediaProof` 时裸 `return err`——不写观测、不给原因，义务说「该录」而 UI 说「未知」。现在映射成 `unavailable / pool_evidence_stale`。同时给 4 个后台循环的 WARN 补上 `error`（它们此前只喊「unavailable」不带原因，这次排查就是被它卡住的）。
    - **现场复验通过**：probe 把 zlm 证据刷成 `zlm_media_verified`；新建 run 进入 `recording`；观测从 `first_completion_pending` 转 `healthy / recent_completed_segment`；CH01 落盘 1 段；`policy_mode` 全程 `none` 未被改写；到期窗口 `ended_at` 精确等于 `expires_at`、`end_reason='expired'`。
  - **验证**：`verify.sh` 7/7 PASS（19s）；全量 `tests/integration` **1025s 通过**；已部署 NAS（`fbb10d6`）并完成现场验收。
  - **未做（批次 2/3）**：周计划 REC-02（`planned` 仍是占位）、事件录制 REC-09/10。

- **2026-10-16 现场发现 CH08 子码流反复重建（两轮）**。第一轮（提交 `811447e`）只解决一半：把 keyframe 抖动放大成 40 秒重建循环这条"软件侧故障循环"砍掉（229/h → ~70/h），子码流仍被打掉。第二轮定位到**真正的根因不在 worker、在 ZLM 协议**：`isMediaOnline` 接口对**没有消费者在拉流**的 proxy 也会返回 `online=false`。**主流不受影响**（Frigate / 实时预览 / 回放 多个客户端持续消费字节，ZLM 持续判 online=true），**子码流周期性被判 offline**（平时没人连，只有 worker 在拉 proxy 做采集/录像，**不消费 ZLM 输出的字节**）。**修法在协议层**（`internal/media/zlm/operations.go` 的 `Inspect`）：`isMediaOnline` 报 false 时再调一次 `getMediaInfo` 验证 —— proxy 还有 tracks、`bytesSpeed >= 0` 就**别**报 `ErrStreamAbsent`。`fetchSnapshot` 提取出来供两路复用（online=true 直接走；online=false 走验证再决定；老 `getMediaInfo` 出错时仍按 isMediaOnline 判定）。`ErrStreamAbsent` 的语义从此干净：**"proxy 真的没了"** 而非 "ZLM 缓存它没在被人拉"。
  - **第一轮（`811447e`）仍然有效，不要回滚**：`videoFrames` 去 `track.Ready`（keyframe 抖动不再误判 stalled）+ `streamGapGraceSamples=3` 宽限 + `newPublicationFixture` wire 上 `reliability.Service`（之前传 `nil` 让 `observation()` 在 `reliability_intervals` 那行提前 return，掩盖了所有宽限测试）。这一轮砍的是「软件侧把单次抖动放大成 40 秒重建循环」；这一轮砍的是「软件侧信任了一个有 bug 的 ZLM 信号」。
  - **新增单测** `TestInspectTreatsIdleButProducingProxyAsPresent`：mock `isMediaOnline=false` + `getMediaInfo` 返回 1 条 track + 128 字节/秒，断言 `Inspect` 不报 `ErrStreamAbsent`、正确返回 snapshot。两个老测试 `TestInspectAbsenceRequiresSuccessfulOnlineResponse` / `TestInspectConfirmsOfflineBeforeWaitingForMediaDetails` 自动通过（它们的 mock 让 `getMediaInfo` 出错/超时，走的是「`isMediaOnline` 报 false 且无法交叉验证 → `ErrStreamAbsent`」分支）。
  - **本机验证**：`go test -race ./internal/media/zlm/...` 全绿；`go test -race ./internal/... ./cmd/... ./migrations` 全绿；`verify.sh` 7/7 PASS（20s）；`tests/integration` 全量 **966s 通过**。
  - **现场要再观察两件事**（等用户部署 + 重启 worker 容器后）：
    1. CH08 子码流 `sub` 区间速率应跌到接近 0/h（0-3 个/h，应是真实 RTSP 重连事件，不再 1-5 分钟一次反复）。
    2. worker 容器日志每 30 秒还在喊的两条 WARN —— `snapshot archive scan unavailable error="ERROR: column p.path does not exist (SQLSTATE 42703)"` 与 `export reconciliation unavailable` —— **与 CH08 无关**，是另外两个真 bug，等这轮收完一起修。
  - **「为什么只有这一路」的完整答案**：CH08 子码流在 worker 拉了 proxy 之后没有任何客户端消费 ZLM 输出的字节。ZLM 周期性扫描发现这个 proxy 没人拉就报 `online=false`。**任何子码流都会有这个问题**，但 CH08 是 32 路里**唯一一路 worker 在拉但没人消费**的子码流 —— 其它 31 路要么主流+子流都关掉、要么主流被多客户端消费捎带把子流 proxy 也维持住了。**用户问「为什么只有这一路」的答案，是这一路的 ZLM 拓扑（worker 拉 + 无消费客户端）唯一**。
  - **用户两个真问题（写死在这）**：「你说主流正常，子码流掉线这符合逻辑吗」—— 主流 100% + 子流 70% 一起出现就**不是摄像头问题**，是软件侧对两个 kind 用了不一致的判定口径。修在协议层而非 recorder 是因为这是 ZLM 协议本身的语义，不是 recorder 单独的策略。**这是设计教训**：单看一个 kind 的可用率永远看不出问题，**两个 kind 的对照**才能发现。
  - **`videoFrames` 不再要求 `track.Ready`**（`internal/recording/bitrate.go`）——维度（width/height）已经能区分视频轨与数据/元数据轨，再叠加 `Ready` 就是在「画面最忙的那一刻」把数据丢掉。新增 4 个单测钉住口径（含「计数器在涨但 `Ready=false` 必须算」一条）。这是本机任何集成测试都触不到的——只有「keyframe 比缓存先到」的真实码流才会暴露。
  - **`streamGapGraceSamples = 3`（约 30 秒）吸收短暂 absent**（`internal/recording/scheduler.go` + `runtime_source.go`）——同一个观察点出现 1 个 `stream_present_pending` 区间，不再立即拆会话；**只观察、不动会话**。第 4 个采样仍 absent 才走 `stopPhysical` + `recoverRuntimeSource` 旧路径。计数从 `reliability_intervals.observed_count` 读，**存进区间表而不放在内存里**——worker 重启不丢，「consecutive」也只有这一个定义点。
  - **测试：新增 2 个、改 2 个**。新增 `TestRecordingRecoveryToleratesBriefStreamAbsence`（1 个 absent 采样 → `unknown/stream_present_pending`、不重建、`run.state` 仍为 `recording`）与 `TestRecordingRecoveryReconnectsAfterSustainedAbsence`（4 个 absent 采样 → 重建且仍同一 session）。改 `TestRecordingRecoverySourceCreatesNewGenerationAndRun` 与 `TestRecordingRecoveryIdentifiesFirstFrameFailureWithoutSecrets` 跑 4 次 `Reconcile` 跨过宽限期——它们原本写的是「1 次采样→立刻重建」语义，那是旧实现的口径，**留旧断言就是悄悄把 bug 写进测试**。`newPublicationFixture` 顺带 wire 上 `reliability.Service`（之前传 `nil`，让 `observation()` 在 `reliability_intervals` 那行提前 return——这就是上一轮自测怎么都重现不了宽限被跨越的真因）。
  - **本机验证**：`verify.sh` 7/7 PASS（22s）；`go test -race ./internal/... ./cmd/... ./migrations` 全绿；`tests/integration` 全量 **963s 通过**（动 `runtime_source.go` / `scheduler.go` / 验证路径，跑全量）；`TestRecordingRecovery*` 共 15 个全过。**未做**：现场部署与 CH08 实测（等用户重启容器后回放子码流——预期区间从 229/h 跌到 ~9/h，画质参数保留现在的 VBR+中等 1024Kbps 640x480@15）。
  - **没找到的「为什么只有这一路」的真因**：CH08 是 32 路里**唯一一路子码流开启 H.264 最高质量 + VBR「智能编码」**——该模式下 keyframe 比 sub 缓存先到的概率最高；其它 31 路要么是固定 QP、要么是更低分辨率。「HEVC」是 7 天前的旧探测数据，**早就不是当前实际编码**，不要据此推断。
- **2026-10-17 M4 可靠性批次 4 已交付：呈现层与导出（REL-05）**（计划：`docs/superpowers/plans/2026-10-16-reliability-batch4.md`；后端 `efa9a4c`、通道页签 `487043b`）。**§6.12 至此功能齐备**：事实、应录、指标、汇总、界面、导出、告警七层打通。现场验收（NAS 浏览器）确认数据与计算正确，界面部分未在现场逐项点过。
  - **告警与故障是两张表，这是本批的设计核心**：故障由**观测**派生，告警由**配置**派生。改一个阈值只能改变未来的行为——若把告警写进 `fault_events`，今天新建的规则会显得像在解释上个月。集成测试 `TestAlertsAreNotFaultsAndDoNotRewriteHistory` 直接断言「评估阈值不改变故障表行数」。
  - **四类阈值**（主流连续不可用 / 应录分片超时 / 每日覆盖下降 / 监测停止）**按各自节奏评估**：即时类每 30 秒（跟采样同频），周期类读**已结算**的日汇总（今天还在写，对不完整的一天报警会每天早上响、下午消）。
  - **两个方向性细节（易错）**：覆盖率是**下限**（低于阈值才触发，与所有时长规则相反）；「未要求录像」不等于「录像迟到」，没有应录义务的通道不参与超时评估，否则留了不录通道的站点上这个告警恒响。
  - **去重靠部分唯一索引** `reliability_alerts_one_firing`：持续越限是**一条**告警刷新取值 + 峰值，不是每轮一条；恢复关闭并**保留记录**（恢复留历史），复发是新的一条。`notified_at` **只由投递路径写**——发送前标记会在发送失败时丢告警，发送后标记才让「已通知」等于「已送达」。
  - **规则默认不配**：PRD 说阈值「可配置」而非「默认开启」，一个自作主张开始报警的版本会在一周内被静音。规则 upsert（firing 告警引用规则行，重建会孤立历史），删除规则级联删其告警。
  - **CSV 按 PRD 逐条带齐**：分母、排除量、待核验量、指标版本、时区，**逐行标注精度**（一个文件可能跨不同保留层）；脱敏由测试断言（不得出现 `/storage`、IP、`pool`、`password`）。
  - **前端三条硬要求做进结构里，不靠自觉**：`percentage()` 返回**联合类型**（`percent` | `reason`），调用方无法把「无有效观测」渲染成 0%；主/子流**每个 kind 一个卡片**，代码里不存在合并入口；`precisionNote()` 在非 exact 时必给可读解释（含「不可下钻到秒」这类后果）。
  - **两个刻意的视觉决定**：**排除时间用灰色而非绿色**（停用/未配置/维护不是好消息，涂绿会让停用通道读成完全可用，单测断言它永不含 emerald）；时间带的开区间以**窗口右端**收尾而非 `Date.now()`（否则同一张图每次渲染都不同——这既修了 lint 的 purity 报错，也是对的语义）。
  - **跨通道比较不给排名、不给平均可用率**：按**观察时长**排序（观察最少的不会因数字好看排前），`sample_sufficient=false` 的行标注「样本不足」而非参与比较；PRD 明令禁止无依据的综合设备评分，而平均不同分母的比率会等权一个观测一小时的通道与一个观测一个月的通道。
  - **总览异常通道卡**：读跨通道 summary，只列**观察充分**且有中断的通道；**读失败时整卡不渲染**——在证据缺失时说「0 异常」正是这张卡最不能做的事。
  - **顺带补齐 OpenAPI 响应 schema**：这些端点的响应此前从未被前端消费，契约里只有路径没有 schema，生成器产不出类型。补了 Rate/IntervalTotals/Metrics/TrendPoint/ChannelSummary/Alert/AlertRule/Interval/Integrity/Fault 并绑到端点。
  - **本机验证**：后端集成新增 10 个用例（CSV 内容与脱敏、四类命中/不命中、去重、恢复与复发、未应录不评估、未知规则拒绝、admin 权限）全过；前端新增 37 个单测（格式化 24 + 页面 13），全量 41 文件 320 测试通过；`verify.sh` 7/7 PASS；全量集成套件另跑（见下）。
  - **未做**：录像工作区「从缺口跳对应故障」（本批用通道页签的故障表 + 运维页承接，缺口与故障的直接跳转未接线）；可靠性页面的**趋势图**（`/trend` 端点已就绪并接线，但图表用表格呈现，recharts 折线留后续）；`planned`/`manual` 录像模式（PRD REC-02/03/04 仍未实现）。
- **2026-10-16 批次 3 的现场验收（NAS + 浏览器实测）发现并修掉两处缺陷（提交 `21be018`）**。这是「本机测试 + 现场部署」路线的第一次真正收获：**两处都不是本机测试能发现的**。现场环境：NAS `https://192.168.66.90:8443`，16 路（7 在线 / 2 停用 / 8 未配置），全部通道 `mode=none`，区间历史仅 2.6 小时（全新部署）。
  - **① 日汇总的过滤条件写反（`trend` 与跨通道 `summary` 恒为空）**：`FinalizeDaily` 只汇总「早于分钟保留窗口（30 天）」的分钟，理由是「一天在即将被清理时才定稿」——因果关系搞反了。结果是近期一天都产生不了日汇总行，而两个端点都读这张表，**健康站点上也永远返回空数组**。修法是去掉年龄过滤：日汇总行在分钟存在期间被**持续维护**，分钟被清理后自然停止变化（此前已累积完整）；进行中的当天也纳入（趋势图必须能显示今天）。
    - **为什么测试没拦住**：我的覆盖测试特意用「40 天前」的数据验证「分钟→日汇总」推导，**那恰好是唯一能通过错误过滤的时间段**。这不是覆盖不足，是**覆盖选中了 bug 刚好允许的样本**——写测试时要问「这个样本是任意选的，还是我不知不觉挑了唯一能过的那一个」。
  - **② 固定回看窗口会跳过积压**：`RollupSealedMinutes` 旧签名是「从 since 走到 now」，传久远时间戳会静默逐分钟走完全程（每格一事务）——生产循环传 `time.Time{}` 没事，第一个传真实旧时间戳的测试挂死 900 秒。先改为显式 `RollupMinutes(from, to)`，再发现「固定 2 小时回看」同样是错的：**worker 停机超过 2 小时就会永久漏掉那段**，而 NAS 恰好只有 2.6 小时历史，这个假设当场不成立。最终改为**水位驱动** `RollupPending`：起点取「最新已汇总分钟」与「最早存在的区间事实」中的较新者，每轮切一片；早于分钟保留窗口的直接跳过（事实已清理，汇总它就是编数据）。
  - **现场已确认正确的部分**（同轮浏览器测试，这些是真实数据的独立证据）：迁移 0025/0026 已应用；`metrics` 按 main/sub/recording 逐个返回且率值带分子分母与状态；正确识别 7 在线/2 停用/8 未配置；`obligations` 写入 `exclusion=disabled`（全站 mode=none）且 `integrity` 把 3600000ms 全判为排除、`owed_ms=0`——即「不要求录像」而非「录像失败」，语义正确；`mean_time_to_recovery` 报 `no_samples` 而非 0%；`precision` 报 `assumed`（迁移回填的策略版本，符合预期）。
  - **教训**：**汇总类代码必须用「当前时间附近的数据」验收**。我的测试数据全部刻意放在保留窗口边界（40 天前）以验证分层，结果绕过了整条近端路径。现场一句「trend 是空的」立刻暴露。**新增汇总/保留相关代码后，要跑一次真实站点并看近期窗口。**
  - **复验时又发现第二处缺陷（提交 `3504563`），比第一处隐蔽**：`observation()` 已把 `disabled`/`not_configured` 映射成 exclusion，**却仍把原始 state 字符串写进区间表**，而该表 CHECK 只允许 `healthy/unavailable/unknown/excluded`。于是录像关闭（`mode=none`）时**每一次采样都在数据库层被拒、插入全部失败**——这台 NAS 全部 16 路都是 `mode=none`，也就是说它的 **recording 历史从来没有成功写入过一条**，而对外只表现为 `metrics` 里一个 `no_observation` 字符串。
    - **为什么没被发现**：`no_observation` 与「本来就没事实」从外部完全一样；集成测试里 recording 通常走 healthy/unavailable 分支，**`disabled` 这条最常走的路反而没有覆盖**。这是「测试数据造得比现场规整」的另一面。
    - **修法**：`foldTx` 入口归一化一次（`storedState`），之后比较与两处写入都用表能表达的词汇；映射**全覆盖**——无法识别的状态归 `unknown` 而不是交给 CHECK 去拒，因为分类不了的样本仍是「观测到了」的证据。`disabled`/`not_configured`/`degraded` → `excluded`，原因仍由 exclusion 列承载。
    - 回归测试 `TestRecordingKindRecordsItsStates` 折入 recorder 真实发出的两态，**已验证去掉修复即 FAIL（复现现场 SQLSTATE 23514）、带修复 PASS**。
  - **现场复验确认修复生效（同一轮浏览器测试）**：日汇总水位化后 `trend` 有 1 个日点（watermark 23:19）、跨通道 `summary` 有 32 行（16 路 × 2 kind）；`intervals` 16 条、`obligations` 1 条、`policyHistory` 1 条、`faults`/`maintenance` 空（本站确无故障与维护）；6 个页面（总览/通道/录像/事件/录像计划/运维）加载无错误；总览显示「6/16 路在线 · 2 停用 · 8 未配置」，与 `/channels/summary` 的 API 数字一致。
- **2026-10-16 M4 可靠性批次 3 已交付：指标口径与汇总层（REL-04 + REL-06 后半）**（计划：`docs/superpowers/plans/2026-10-16-reliability-batch3.md`，提交 `d822793`）。批次 1/2 交付事实（区间、故障、应录、完整性），本批交付**率值本身**与让长窗口可行的汇总层。**REL-05 页面/CSV/阈值告警留批次 4**——率值必须先算对，而算对与否可独立验证。
  - **验收锚点是 PRD 自己的算例**（不是另发明的口径），两个都原样写进单测：① 60 分钟窗口内 40 可用/10 不可用/10 未知 → 观测可用率 80%、保守可用率 66.67%、监测覆盖率 83.33%；② 60 分钟应录只生成 50 分钟 → 覆盖率 83.33% 且**分母仍是 60**。第二个是本批最容易做错处：故障绝不能提高分数。
  - **率值结构上不给裸百分比**：每个 `Rate` 带 `numerator_ms`/`denominator_ms`/`unjudgeable_ms`/`status`。REL-04 明令「不能只存最终百分比」——靠结构强制，不靠调用方自觉。
  - **三种空分母语义分开**（PRD 分别点名，塌缩成 0 会把「什么都没观测到」报成「全下线」）：`no_observation` / `not_applicable` / `no_samples`。
  - **主/子流不合并**：API 按 kind 逐个返回，**没有**跨 kind 合并值——「子流正常不能掩盖主流不可用」是关于答案形状的要求。
  - **保守可用率 ≤ 观测可用率**恒成立（未知只对站点不利），单测钉住。**排除时长进展示不进分母**。
  - **中断指标的口径区分（易错，已验证）**：总数/最长**含**仍在进行的中断——否则会出现「总中断 0 分钟、最长中断 30 分钟」这种荒谬；**平均恢复只算已结束的**，因为进行中的中断还没有恢复时长。这条是先写错测试、回看 PRD 才纠正的。
  - **历史媒体可访问率是已验证下界**：未核验的副本算未知、远端不可达不算物理丢失、上传成功历史不当当前证据；同一段的多副本不重复计时长。
  - **汇总层（REL-06）**：分钟 30 天 / 日 730 天，**均为区间事实的投影，没有任何判定读它**——这就是「不用累计快照覆盖原始事实」的落地：两者不一致是投影 bug，不是新事实。行带 `computed_at`/`metric_version`/`input_watermark`。汇总循环无队列、只读事实写汇总，不可能阻塞录制。
  - **三层自动选层 + 如实标注精度**：短窗口读区间（`exact`）、长窗口读分钟汇总（`minute_rollup`）、超区间保留读日汇总（`daily_rollup`）、超所有层报 `unavailable` **而不是报零**（零会被读成「没有中断」）。
  - **新端点**：`GET /channels/{id}/reliability/metrics`（playback，按 kind 逐个）、`GET /channels/{id}/reliability/trend`（playback）、`GET /reliability/summary`（admin）。跨通道比较带 `observed_ms` 与 `sample_sufficient`：90 天窗口里只观测 10 分钟的通道不是最可靠而是观测最少，**不给无依据的综合评分**（REL-05 明令）。契约与 `api-types.ts` 已同步。
  - **本批修掉三处实际缺陷（全是新测试发现的，记下来别再犯）**：① **精度分支方向写反**——`!from.Before(now.Add(-DailyRetention))` 在窗口很新时也为真，导致每个窗口都判成「超出所有层」并提前返回空结果；改为按 `age := now.Sub(from)` 比较。② **分钟汇总的边界取整**（最关键的一处）：窗口不对齐分钟时直接 `SUM(整个分钟)` 会把窗口之前的时间算进去，与明细路径给出**不同的答案**——而「两层必须一致」正是切层的全部依据。改为只汇总整分钟、两端不完整的分钟读区间事实；集成测试用「同一窗口分别走两层必须相等」钉住。③ SQL 细节：`FILTER` 必须紧贴聚合函数且在 `::bigint` 之前；日汇总里关联子查询不能引用分组列，改为独立 CTE。
  - **本机验证**：单测 21（指标 13 含两个 PRD 算例 + 完整性 9）、集成 10 全过；`verify.sh` 7/7 PASS；**另实跑真二进制**——worker 在已迁移 schema 上启动、义务与汇总循环都真的写出行（分钟行来自真实区间）、`SIGTERM` 0 秒干净退出 exit 0。
  - **未做**：REL-05 页面（通道「可靠性」页签、时间带、趋势图、指标卡、故障表、录像页缺口跳转、总览异常通道）、CSV 导出、阈值告警配置、`planned`/`manual` 模式——留批次 4。
- **2026-10-16 M4 可靠性批次 2 已交付：应录区间与录像完整性证据（REL-03）**（计划：`docs/superpowers/plans/2026-10-16-reliability-batch2.md`，提交 `414dee7`）。批次 1 交付「流是否可用」（主/子流），本批交付「本该录多少、实际录了多少」。**难点不在计算而在分母的来源。**
  - **`recording_policies` 只有当前值，而 REL-03 明令「不用当前计划重新解释过去」**——把今天的 `continuous` 套到上个月，等于每次改设置都重写历史。故新增 `recording_policy_versions`（迁移 `0025`）记录每个 mode 版本及 `effective_at`。
  - **策略历史写入口只有一处**：`recording/`source_switch.go` 的 `finishSwitch`（全库唯一的 `UPDATE recording_policies`），所以 policy_apply / 切换 / 回滚一次覆盖；`site/setup.go` 与 `site/service.go` 建站补 `version=1`——不补的话「建站之前」与「建之后没要求」无法区分。**改策略写入时不要绕过这两处。**
  - **回填不猜时刻**：迁移为既有策略行补的标 `backfill`，`effective_at` 取通道创建时间作**下界**；读路径据此降精度为 `assumed`（REL-06 要求跨保留边界注明精度）。任何声称知道建站前策略真实生效时刻的实现都是在编数据。
  - **义务区间由独立对账循环推导，不挂观测流（这条改过，别改回去）**：第一版想挂在 `observations.go`（批次 1 折 interval 处），核对发现 `monitor.go` 的扫描是 `WHERE c.enabled AND c.current_revision_id IS NOT NULL OR EXISTS(...)`——**停用或未配源的通道根本不在扫描范围**，而这两类排除恰是 REL-03 要求显式记录的。改为每 10 秒遍历**全部**通道重算（`recording.ReconcileAllObligations`）：策略/启停/配源/维护四个写入点互相独立，逐个挂钩必漏，重算天然全收且能自愈。
  - **不缩小分母（本批最重要的一条）**：录像服务停机、源故障、存储阻断期间**仍然开应录区间**。否则分母随失败一起缩小、可用率反而好看。`obligationFor` 的判定顺序即规格（未配源 → 停用 → 维护 → continuous 应录 → 其他不要求），注释里写死了每一步的理由。
  - **义务必须平铺时间轴，与 interval 折叠故意不同**：interval 闭合在「最后一次支持旧状态的观测」（避免把中断算短）；**义务闭合在本次对账时刻**。因为义务是分母，按前者闭合会留下一个对账周期宽的空洞，那瞬间既不属于应录也不属于排除，完整性根本无从计入。精确变更时刻仍可从 `effective_at` 恢复。
  - **完整性只给四个时长，不给百分比**：应录/有效/缺口/待核验，`covered+missing+unverified=owed` 恒成立（单测钉住）。REL-04 要求每个率值带分子分母、不可判定时间、校验级别与时区——一个裸百分比正是本章要防的东西。
  - **`deleted` 段算有效、`provisional` 段算待核验**：删除是保留策略的决定而非录像失败（否则老媒体一去完整性就掉）；provisional 的边界是推断的，不能作证也不能算失败。
  - **`planned`/`manual` 留词汇不假装支持**：PRD REC-02/03/04 未实现，推导按「不要求录像」处理并在 evidence 标 `mode_not_implemented`。
  - **新端点**：`GET /channels/{id}/reliability/obligations`（playback）、`GET /channels/{id}/reliability/integrity`（playback）、`GET /recording-policy-history`（admin）。契约与 `api-types.ts` 同步。
  - **保留**：义务并入 365 天层；**策略版本不清理**——义务引用它，删了义务就无法解释。
  - **本批修掉两处实际缺陷（都是新测试发现的）**：① `ListObligations`/`gapReasons`/`segmentSpans` 的 `COALESCE(ended_at,$n)` 让 PostgreSQL 无法推断参数类型（SQLSTATE 42P18），加显式 `::timestamptz`；② `ListPolicyVersions` 传了 `from` 却未在 SQL 引用，同样触发 42P18——改为真正使用它（窗口起点仍生效的版本必须返回）。
  - **本机验证**：单测 12（完整性算术 9 + 精度 3）、集成 17 全过；`verify.sh` 7/7 PASS；**另实跑真二进制**——worker 在已迁移 schema 上启动、对账循环真的写出义务（CH01/CH02 各一条，排除项正确）、`SIGTERM` 0 秒干净退出 exit 0。
  - **未做**：可靠性页面（REL-05）、REL-04 指标口径与分钟/日汇总、`planned`/`manual` 模式——留批次 3。
- **2026-10-16 现场部署暴露三处问题，已修并推送（`f63295e`、`3695b7f`、`1f779bc`）**：REL 批次 1 推送后用户在 NAS 部署，**worker 容器起不来**。追下去是「本机测试 + 现场部署」这条新验收路线本身的缺口 —— 本机没有 Docker daemon，`Run()` 的装配路径从来没被任何测试执行过（单测只覆盖各模块内部逻辑），所以接错线要等容器起不来才发现。三处：
  - **① 启动 panic**：`reliability.New(db, recordings.Sources.Auth)` 写在 `recordings.Sources = channel.NewSources(...)` **之前** —— 此时 `Sources` 是 nil，取 `.Auth` 直接段错误（`run.go:139`）。已后移。
  - **② `CloseOpenOnStart` 原本同步且致命**：失败即 `return`，整个 worker 不启动，且它排在健康服务器之前。可靠性是对媒体链路的**观测**，不能成为录像的前提。改为在 monitor 协程内先 settle 再进循环，失败只 `Warn`。**这条是设计口径，别改回去。**
  - **③ 关停路径 WaitGroup 计数错（现场版本就有，且修 ① 时我一度改成了另一个错值）**：该块 `Add(2)+Add(3)=5` 但只有 4 个协程回报 → `Wait()` 永不返回、SIGTERM 后挂死到被强杀；我第一版修复改成 3 对 4 → 关机时 `panic: sync: negative WaitGroup counter`。两个方向都静默且延迟暴露，现已写死为 `Add(4)` 并附注释说明两个方向各自怎么坏。
  - **新增护栏 `internal/app/run_waitgroup_test.go`**：用 `go/parser` 读 `run.go`，把 `background.Add(n)` 乘以所在循环迭代数，与带 `background.Done()` 的 `go` 语句数比对。**已验证它对着现场版本 FAIL（38 vs 37）、对着 REL 之前的 `c14645b` 与修复后的当前树 PASS。** 循环边界不是字面量时报 note 而非猜测。以后往 `Run()` 里加协程，这个测试是唯一会拦住漏 `Add` 的东西。
  - **顺带修掉一个更严重的问题：`deploy/production/verify.sh` 被 `234043a` 误删了半张脸**。那个提交只在做 gofmt 护栏，却在重写文件时删掉整段 —— api/tsc/eslint/vitest/go_unit 五条 `run` 调用与 `integration()` 函数全部消失。**从那之后（含整个 REL 批次 1 的开发过程）verify.sh 只跑 `go vet` + `gofmt` 两项、约 1 秒「全绿」收尾，`--full` 会调用不存在的函数直接报错。** 它让人以为验证过了，而实际什么都没验 —— 这正是脚本本身要防的那类事。已恢复为 7/7 PASS（21 秒）。顺带把护栏里 `sha256sum`（coreutils 才有，macOS 原生没有）换成 `shasum -a 256`，并给 `xargs gofmt -l` 补 `|| true`（无匹配输入时退出码非零会在 `set -e` 下中断整个脚本）。
  - **实跑验证（本机，真实配置 + 测试库）**：worker 启动正常并持续运行，`SIGTERM` 后 **0 秒干净退出（exit 0）**；另构造第一版错误修复的二进制证明它确实 `panic: negative WaitGroup counter`。**全量集成套件 918s 通过**（`go test ./tests/integration -count=1`，动过 `monitor.go`/`run.go` 故跑全量）；`verify.sh` 7/7 PASS。
  - **教训**：「本机测试 + 现场部署」路线下，**本机跑不到的那部分（进程装配、容器启动）必须由「跑一次真二进制」来补**，不能只靠 `go test`。本轮 worker 的启动与关停都实跑过才算数。
- **2026-10-16 M4 可靠性批次 1 已交付（REL-01/02 + REL-06 持久化部分）**（计划：`docs/superpowers/plans/2026-10-16-reliability-batch1.md`）。PRD §6.12 整章此前零实现，本批先让**事实**完整可复算；**指标（REL-03/04）与页面（REL-05）留批次 2/3，本批不产生任何率值**。
  - **为什么新建表**：`source_observations` 是「当前状态」不是历史（30 秒 TTL、只取最新一行，答不了「上月在线多久」）；`recording_gaps` 是**录制**中断证据、由录像状态机驱动，不覆盖 REL-01 谈的主/子流可用性。故新增 `reliability_intervals`（事实层）、`fault_events`、`maintenance_windows`（迁移 `0024`）。
  - **区间由观测的变化点产生**（不按固定窗口切分），半开 `[started_at, ended_at)`；同 state+reason 续期、变则闭旧开新。**闭合时刻用「最后一次仍支持旧状态的观测」而不是「发现故障的那次采样」**——否则每次中断都少算一个采样周期。`first/last_observed_at` 一起返回，承载 REL-01 要求的「轮询无法精确到亚秒」这个不确定带。
  - **监测未知显式记录**：启动时先闭合所有开放区间、把停机时段记成 `monitoring_gap`，**再开始采样**；运行中超过 2 个采样周期无观测同样开 gap。停机时间不会被填成在线。
  - **故障由区间状态转变驱动**：`unavailable` 开故障、`healthy` 自动关闭记 `recovery=auto`；**`unknown` 不开故障**（证据缺口 ≠ 设备故障）；排除状态（停用/未配置）不开故障。同一连续故障更新原行（部分唯一索引 `fault_events_one_open` 做栅栏）。
  - **八大类归因保守**：断流只归 `upstream_unavailable`，**绝不细分到摄像头硬件损坏**（REL-02 明令）；未知归 `unknown`，不自造第九类。
  - **根事件只聚合不改写**：同轮同类故障挂到一个组件级根事件，业务影响按**集合并集**算（8 路同时断 5 分钟 = 5 分钟而非 40 分钟）。子故障保留各自行，归错也不影响单通道数据。
  - **归因修订不改写原始值**：`initial_reason`/`initial_category` 永不改，确认结果追加 `revisions` 并与 `confirmed_*` 并列；`EffectiveCategory()` 决定报表按哪个计数。这是防「事后把故障改登记成维护来静默提高可用率」的机制。
  - **保留（REL-06 第一阶段）**：观测 7 天、区间与故障 365 天，分批删除，**只删已结束的行**——持续中的中断是很老的当前事实，不能当过期历史删。
  - **顺带修一处既有风险**：`monitor.go` 原先在数据库错误时 `return err` **退出整个采样循环**（三处：通道扫描、逐行 Scan、码率清理）。数据库抖一下采样就停，反而制造出 REL-01 要求我们发现的监测缺口。已改为记日志 + 等下一轮。
  - **新端点**：`GET /channels/{id}/reliability/intervals`（playback）、`GET /faults`（带 channel_id 要 playback、跨通道要 admin）、`PATCH /faults/{id}`（admin 确认原因）、`GET /faults/{id}/impact`、`GET/POST /maintenance-windows`（admin）。契约与 `api-types.ts` 已同步。
  - **本机验证**：集成新增 22 个用例（区间折叠 6、故障 8、维护与并集 3、保留 1、端点 2、协作 2）全过；`verify.sh` 7/7 PASS。

- **2026-10-16 两处既有缺口已修（本机取证 → 修复，未经 CI）**：
  - **`source_observations` 只增不删**：全库 0 处删除，而 `monitor.go` 每 10 秒对每个已配置通道写 main/sub/recording 三行（实测统计口径）：**每通道每天约 2.6 万行**，8 路每月约 600 万行、32 路每月约 2500 万行，且无任何保留策略。这些行**携带 30 秒 TTL、只服务「最新状态」查询**（唯一读者 `channel/source_status.go` 取每通道每 kind 的最新一行），历史价值为零。修法：`recording.SweepObservations` 按 `ObservationRetention`（24 小时）分批删除，挂进既有 `RunRetention` 循环；同时新增迁移 `0023` 的 `source_observations(observed_at)` 索引——**原有索引以 `channel_id` 开头，删除条件只用 `observed_at`，用不上它**，不加索引会全表扫。**删除不会改变 UI 结论**：某 kind 无行时 `statusTx` 落到显式的 unknown 分支（有集成测试钉住），不会把「没观测」读成「健康」。
  - **`recording_gaps` 核实后判定不需要清理**（与上一条区分开）：它有去重（`NOT EXISTS ... end_at IS NULL`，同一通道同时只留一条未闭合缺口），所以是「每故障一条」而非每 tick 一条；而且它是**证据**——REL-02/05 故障归因与故障表的原料，今天只是尚无读者（全库只有 INSERT/UPDATE）。**不要在 REL 批次之前删它。**
  - ~~`ONE_NVR_OPENLIST_ENABLE=no` 时云归档并未真正暂停~~ **这一条是误判，已按用户纠正撤回**（详见上面 capability 那条的现行口径）：OpenList 是模块开关而非功能开关，`=no` 时归档功能照常可用。当时加的 `archiveFor()` 门禁与 8 个 nil 守卫已全部撤除。
- **2026-10-16 快照卡住的**第三种**成因已修（用户截图复现：已结束但图仍 pending）**：前一条修的是「MQTT 丢 end → 事件永不开闭」。用户随后截图一张**已正常结束**（时长 1m00s、状态未处理）却显示「无抓拍图像」的卡片——那是**另一条路径**，上一轮没覆盖：
  - **成因**：Frigate 的 `end` 消息里 `has_snapshot` 用的是**该时刻的值**，而快照文件可能稍后才落盘；**发完 end 后 Frigate 把事件从 `events_in_process` 删除、不再发任何 update**（源码 `maintainer.py`：`del self.events_in_process[...]`）。于是 one-nvr 永久停在 `ended_at` 非空 + `snapshot_state='pending'`。
  - **为什么无人修复**：三条路径的筛选条件都把它排除——对账要 `ended_at IS NULL`、24h 兜底要 `ended_at IS NULL`、归档 sweep 要 `snapshot_state='ready'`。**「已结束 + pending」全库没有任何修复者**，且因为它有结束时间和时长、看起来"完整"，也没有别的机制会去检查它。
  - **修法**：对账候选扩展为「未结束」**或**「已结束且 `snapshot_state='pending'`」；后者的年龄门槛按 `ended_at` 算（那是检测器停止汇报的时刻）。
  - **关键设计：以图为准，不信标志位。** 用户的原话是「我看 frigate 里都是有的」，而 Frigate 自己的代码注释就说 `has_snapshot` 可能过期——所以当事件已结束且标志位说"没有"时，**直接请求那张图**（`GET /api/events/{id}/snapshot.jpg`）：200 → 补成 `ready`（归档 sweep 随之取走）；404 → 才判定无图并终态为 `gone`；其他响应（检测器挣扎中）→ 保持 pending 下轮再问，**不拿「问不出来」当「没有」**。
  - **前端**：新增第三支显示——已结束且 pending 显示「**抓拍待同步 · 稍后自动核对**」（灰点，非故障样式），与「抓拍未到达 · 事件未正常结束」（琥珀，事件未闭合）和「无抓拍图像」（确认无图）三者互不冒充。
  - **测试**：集成新增 3 个（标志位说 false 但图取得到 → 补回 ready；图确实 404 → 终态 gone；已完成的事件不再被询问）。vitest 新增 1 个（三态区分）。
- **2026-10-16 事件丢失结束消息的根因已修（现场实测 13/50 卡住）**：现场取证发现最近 50 条事件中 13 条 `snapshot_state=pending` 且全部 `ended_at=null`（最老的挂 2.5 小时），事件页因此出现四分之一卡片的「无抓拍图像」黑块。
  - **根因链**：MQTT `end` 消息丢失后，upsert 再也等不到补齐 `ended_at`/`has_snapshot` 的 update；归档 sweep 只挑 `snapshot_state='ready'`，pending 事件永不被抓取；`internal/events/run.go` 只订阅 MQTT、**没有任何对 Frigate API 的补账机制**。前端 5 分钟收口（`ONGOING_MAX_AGE_MS`）本身按设计工作，只是后端数据永远补不上——**此前 2026-10-14 那次只修了显示层**（AGENTS 旧条目「修掉 MQTT end 丢失导致永挂进行中」指的仅是前端文案），数据层一直没修。
  - **顺带查实的第二个缺陷**：`StaleOpenThreshold`（24h，注释写明「上游从未关闭的事件应在此时点后正常过期」）自加入起**声明后从未被任何代码引用**，且清理 SQL 要求 `ended_at IS NOT NULL`、`expires_at` 也只在有 end 时计算——所以无结束时间的事件**永远不会被清理，只增不删**（与 `source_observations` 同类问题）。
  - **修法**：worker 新增对账循环（`internal/events/reconcile.go`，5 分钟一跳）：对 `ended_at IS NULL AND started_at < now()-10min` 的事件调 Frigate `GET /api/events/{id}`（已核对 0.17.2 源码，响应含 `end_time`/`has_snapshot`/`zones`/`data.score`），复用**同一个 upsert** 补齐——为此把 Ingest 的写入抽成 `internal/events/upsert.go` 的 `eventUpsert`，两条路径共用一份 SQL，不能分叉。检测器答 404 或整体不可达时，由 `ReconcileStaleOpen` 按 24h 兜底关闭（总算接上了那个死常量）。**回填不覆盖已有事实**：仍走单调 upsert，end 不后退、score 只升、ready 不降级。
  - **前端**：区分两种「无抓拍」——`endedWithoutUpstreamClose`（有开始、无结束 → 「抓拍未到达 · 事件未正常结束」+ 琥珀警示图标，并说明系统会向检测器核对）与「无抓拍图像」（正常结束、检测器本来就没存）；时长胶囊对前者显示「结束未知」而非笼统的「已结束」。占位从满幅纯黑改为 `bg-zinc-900/40` 浅底纹 + 小图标——13 个黑块对画廊观感的伤害是现场反馈的重点。抽屉「结束」行同步改为「未收到结束消息（系统会向检测器核对）」。
  - **未做**：Frigate 事件 API 的补账只针对 ended_at 为空的事件；`has_snapshot=false` 但已结束的事件不会被反复重试（属正常终态）。「仅看有抓拍」筛选未做——判定为先提供更清晰的语义区分，若现场仍觉嘈杂再加。

- **2026-10-16 M3 批次 4 已交付：通知（EVT-05）+ 抓拍归档（M3 已完成）**。用户裁决「不再跑 CI，本机测完直接部署验证」，所以本批的验收依据是**本机测试 + 现场部署**，不是 CI。
  - **通知的分层是设计核心**：先把渠道无关的通知变量定死（`internal/notify/message.go`），渠道只做「把变量带出去发送」。适配器接口只有 `Kind()` + `Send(Message)`，注册表按 kind 构造；**新增渠道 = 写一个适配器并注册，不改变量层、不改事件与规则代码**（`TestANewChannelKindNeedsOnlyAnAdapter` 用虚构 kind 钉住这条）。首批 `inapp`（通知行即投递）与 `webhook`（POST 消息本体 + `X-OneNVR-Delivery`/`-Event`/`-Signature` 头，签名覆盖实际发送的字节）。
  - **三条「不撒谎」的语义（别改回去）**：进行中的事件不伪造结束时间（正文写「进行中」）；抓拍取不到就不给链接；站点时区未知时省略本地时间只留 UTC。另外回放链用**通道 ID 而非编号**（`initialChannel` 是拿去和 `channels.items[].id` 比对的）。
  - **冷却首次真正执行**：此前 `cooldown_seconds` 只做范围校验、全库无读取点，`notified_at` 从未写入。现在冷却在入队时判定、**只压制外部渠道**；**站内提醒不受冷却**（用户口径「只要知道有这个事件」，AI-05 也禁止丢记录）。`notified_at` 语义定死为「已通知」而非「已送达」。
  - **一次命中只通知一次**：`UNIQUE INDEX notifications_one_per_hit (hit_id) WHERE hit_id IS NOT NULL` 是栅栏，`Raise` 用 `ON CONFLICT DO NOTHING`；命中 INSERT 改用 `RETURNING id` 拿回重复更新时的原有命中 ID。
  - **抓拍归档**：worker 拉检测器快照写进通道绑定池 `snapshots/<通道号>/<站点本地日期>/<事件ID>.jpg`，读路径**本地优先**、上游清理后仍可看。未绑池报 `pool_unbound`；写入失败或 0 字节直接删文件并记 `snapshot_incomplete`——截断的图看着像证据却不是。到期随事件清理，先删文件后清引用。
  - **前端**：系统设置页「通知渠道」面板（密钥 write-only，类型列表来自服务端注册表）；规则编辑器**目标类别改多选**（后端本来就是数组，此前单选导致「人+车」要建两条规则）+ **规则可绑定通知渠道**；事件页按用户口径改为**以回看为主视觉**——「查看录像」从 hover 提到常驻按钮，卡片上的处理快捷按钮移除（功能一个没少，仍在抽屉与工具栏批量操作里），抽屉新增「通知」区展示每次投递的五态与 HTTP 状态码。
- **2026-10-16 本批修掉两个既有缺陷（都是本机集成测试发现的，不是新引入）**：
  - **`credentialAAD` 字段白名单缺 `secret`**：`internal/secrets/credentials.go` 只允许 `username`/`password`/`import_draft`，而通知渠道用 `"secret"` 封签名密钥 → 每次注册渠道都返回 `credential_unavailable`。**这个若不修，通知功能在真实部署里根本无法配置**。已加白名单项（AAD 里字段名参与绑定，所以单独命名而非复用 `password`，避免把两种凭据变得可互换）。
  - **Frigate 事件无区域时无法入库**：`internal/events/service.go` 的 zones 为 nil 时 `json.Marshal` 产出 `null`，而 upsert 里的 `jsonb_array_length(null)` 报 SQLSTATE 22023 ——「不在任何区域内的检测」会整个入库失败。自 `e0ab2a5`（M3 批次 1）起就存在。已改为 marshal 空数组。
  - **`jobs.Run` 是 worker 的常驻循环**（无 job 时 `sleep 1s` 继续），测试想要有界排空必须自己驱动 `Claim`/`Execute`——用 `jobs.Run` 会挂到超时。
- **2026-10-15 CI 连续转红已修复，当前全绿**：`432fa79` 起 Foundation CI 连续失败，**红的不是产品功能，是 e2e 规格没跟上 UI 重构**——每修好一处，测试就往下推进一段、露出下一处，共 5 处。**`8168205` 已 12/12 全绿**（run 38732529387，28.0 分钟），是 `d4cb1d8` 之后第一次全通过。**注意：此后用户决定不再依赖 CI，改为本机测试 + 现场部署验证。**
  - **① 存储池添加表单加了折叠开关**（`daf15a5` 把表单收进 toggle，提交按钮文案「添加存储池」→「确认添加」）。旧断言直接 `getByLabel('存储池名称').fill(...)` → 表单未展开、元素不存在 → 超时。两处：`m1a.spec.ts`（`directoryPoolKeepsPendingZLMStatus`）与 `m1b-media.spec.ts`。**已修**（`11b305d`）。
  - **② `live-video-area` 不再注入内联 `aspect-ratio`**（`de6f97e` 改为「格子定大小 + object-contain letterbox」；该提交同步改了 `live/index.test.tsx` 却漏了 e2e）。**已修**（`11b305d`）：断言容器可见、无内联 aspect-ratio、包围盒非零。
  - **③ 录像页标题行已删除**（`7f64173`「删录像页顶部标题行」移除了 `/recordings` 的 h1），规格仍在等 `getByRole('heading',{name:'录像'})`。**已修**（`cd2ffe3`）：改用 `playback-console` 作锚点。
  - **④ `/content` 响应监听器注册太晚**——`7f1da10` 的「无 channel 参数时自动选中第一个在线通道」+ `8e6ad16` 的 auto-park `useEffect` 使得授权读取在**页面加载期间**就发生；监听器原先挂在校验之后，捕不到任何请求，`contentResponses` 恒为 0（而视频确实解码）。**已修**（`88481cc`）：监听器移到 `page.goto('/recordings')` 之前。
  - **⑤ 删除生命周期块被 `lease_held` 拦下（真因：测试自己留下的孤儿租约）**。**已修**（`53267f0` 诊断能力 + `8168205` 根因）。两个层面：
    - *表象*：`m1b-media.spec.ts` 报 `SyntaxError: Unexpected token ' ftypis...`——`/content` 在删除未生效时返回 MP4 字节，而规格无条件 `JSON.parse(contentBody)`，崩溃把「哪条理由拦下了删除」一起吞掉。改为按 `Content-Type` 判定再解析，并把每项的 `reason` 带进断言期望值（失败时直接显示 `in_state/reason`）。
    - *根因*：rangeProof 用页面内 fetch 对 `/content` 发了 **4 次**请求，而 `content.go` 的 `OpenSegment` **每次成功打开都登记 90 秒播放租约**（ARC-05）。这些请求没有播放器管理——播放器只释放自己 entry 那个片段的租约——于是 `historical` 上留了一条无人释放的租约；「停止」按钮释放的是播放器所在片段（auto-park 停在**最新**片段，而 `historical` 是更早的），删不掉它。删除检查顺序又是**租约先于保护**，所以被 `lease_held` 拦下。
    - *为什么以前绿*：`d4cb1d8` 的这段结构完全相同，是**时序侥幸**——租约 90 秒到期，而中间的导出任务（90 次 1 秒轮询等成功）恰好等过去了；`8e6ad16` 引入首段自动加载、`21801ba` 之后周边时序变化，这点余量就没了。**这不是产品回归**：活跃租约优先于删除（ARC-05）是刻意语义，且 `tests/integration/archive_lifecycle_test.go` 已覆盖。修法是测试自己别制造悬挂状态（显式释放），另在「停止」后 best-effort 等一次 `DELETE /lease` 落定。
  - **教训（写死在这）**：UI 重构改了控件结构或布局契约时，**同批必须改 e2e 与 vitest 两处**——`de6f97e` 改对了单测漏了 e2e，`daf15a5` 两处都没改，于是连续 16 轮 CI 全红而无人发现。改前端交互结构（按钮文案、表单折叠、被移除的内联样式、改动加载时序）时，先 `grep tests/e2e/*.spec.ts` 找同名断言。**媒体规格尤其脆**：依赖真实容器、耗时 30–45 分钟、只在 CI 可跑，本机无法复现——改动播放器/录像页行为时，媒体规格要当产品契约一起看。**另外：测试直接调 `/content` 会留下租约**（每次打开都登记 90 秒），凡是在同一片段上后续要删除/清理的规格，必须显式释放。
  - **流程教训**：这类「修一层露一层」的追查应当在**每层修完就给用户一个选择**（继续追 / 先记录 / 交回），而不是一路追到第 5 层才发现可能需要产品决策。本轮前 4 处是明确无争议的过期断言，第 ⑤ 处需要判定「是断言陈旧还是产品回归」——事后证明仍是测试问题，但当时的判断点是对的。
  - **本机验证（2026-10-15 实跑）**：`go test -race ./internal/... ./cmd/... ./migrations` 全绿；`tests/integration` **全量 887s 通过**；`vitest --browser.headless` **271/271 全过**；`verify.sh` 6/6 PASS；改动过的 e2e 规格 `tsc --noEmit --ignoreConfig --strict ...` 无错。
- **2026-10-15 CI 反馈提速（`fc0fc42`）**：见上文「常用验证」——新增本机 `verify.sh`（18 秒跑完类型/lint/单测），CI 按改动路径裁剪（纯文档 ~1 分钟、纯前端 ~10 分钟、Go/部署改动才跑完整 28 分钟），并新增 `validate` 前置 job 与 `full_suite` 手动开关。
- **2026-10-15 控制台视觉与交互批次（`061ad68..1494a63`，8 个提交，纯前端除一处后端修正）**：无新迁移、无新端点，`docs/api/m1b.openapi.yaml` 只补 `ChannelSummary` 两个已有字段。
  - **后端唯一改动（`21801ba`，语义修正，别改回去）**：**纯绑定/换池（`pool_switch`）不再要求新鲜写入证据**——此前它与 `desired=continuous` 同门禁（要求 api/worker/zlm 三探针全 healthy 且未过期），于是 zlm 证据 30 秒一过，「先绑池、后开录」这个页面主推的流程必然 409 `zlm_write_evidence_unavailable`，等于逼迫通道正在推流才能预配置。现在**新鲜写入证据只门控 `desired=continuous`**（真正落盘的时刻），纯绑定只校验池存在且启用。`source_changes.go` 的判定从 `(desired == "continuous" || kind == "pool_switch")` 收窄为 `desired == "continuous"`。**`/channels/summary` 契约补 `storage_pool_id` 与 `recording_mode`**（`statusTx` 本就产出，透传即可），录像计划页因此免除每行两个状态请求。
  - **一次真正的前端性能修复**：录像计划页 32 行原先各发 2 个状态请求（N+1），`daf15a5/4796531` 之后行数据改由页级一次 `/channels/summary` 供给（写入前仍即时读 live version）；批量绑定改**逐通道顺序执行**（并发写会互踩 `If-Match` 版本，是批量失败的次因），已是目标池的行跳过并计数，逐行给出「N 路已绑定 · M 路已在该池 · 失败 CHxx: 原因」。
  - **视觉体系**：全站深色统一为**纯碳黑 zinc（chroma=0）**，`#050506` 背景 + zinc-950/900 层次，`video-*` 令牌同步去蓝相（`7f1da10`）；随后 `23afd08` 给**浅色模式**做双模适配——**面板浅色化、视频画布保持纯黑影院视口**（UniFi Protect 规范），`live` 瓦片内部在两种主题下都是影院黑（`1494a63` 修的是上一批令牌浅色化后瓦片内部变白底灰字的回归）。时间轴 canvas 调色板按 `resolvedTheme` 切换。
  - **页面级重构**（多为「Protect 质感」）：总览页从硬编码「尚未提供业务数据」改为四张真实指标卡 + 组件状态矩阵（中文名、呼吸灯、探针事实）+ 底部左右双栏（活跃通道巡检 / 最近 AI 事件）——**注意：这补上了 PRD DASH 的一部分，但 DASH-01 要求的「可用率/覆盖率」指标仍不存在**（那是 REL 整章的事，见下方遗留）。存储池卡片加多段容量条与内嵌探针事实；事件页加通道胶囊、4 列画廊、hover 快捷处理与**进行中超时收口**（无 `ended_at` 且开始超过 5 分钟按已结束显示，修掉 MQTT end 丢失导致永挂「进行中」）；录像页 `channel` 参数缺省时**默认选中第一个在线通道**（不再落在停用的 CH01 上）。
  - **本轮审查另查实、尚未修的三处缺口（别当成已做）**：① **`source_observations` 与 `recording_gaps` 只增不删**——`monitor.go` 每 10 秒对每个已配置通道 `Reconcile`，每条路径都无条件 INSERT（`observations.go`），全库 `grep -c delete source_observations` 为 0，32 路约每天 80 万行，且 REL-06 要求的分层保留（明细 7 天/分钟 30 天/事件与缺口 365 天）也不存在。② **`ONE_NVR_OPENLIST_ENABLE=no` 时云归档并未真正暂停**（承接上一条 capability 记录，仍未修）：`internal/app/run.go` 的三个循环与归档端点都无条件运行，`OpenListEnabled` 只在 render/egress/探针/capability 用到；对照检测侧 `FrigateEnabled=no` 时 `d.Detection`/`d.Events` 为 nil、端点 404。后果是 capability 报「功能未启用」而实际仍可注册目标并上传。③ **AI-03 要求「必须展示实际生效参数」（Frigate inertia）未落实**——`internal/detection/` 里没有 `inertia`，前端只有一句注释说明该语义留在检测器。
  - **测试覆盖缺口**：`tests/e2e/` 只有 m1a/m1b/m1b-media/protocol/restart 五份，检测配置、事件中心、归档目标三个页面**没有任何 e2e**（m1a 里只有一处占位端点断言）；`tests/integration/` 无可靠性相关测试。上条教训之外，这也意味着这三个页面的 UI 结构改动目前只有 vitest 兜底。
- **2026-10-14 模块状态如实化（capability 修正）**：`GET /api/v1/capabilities` 与设置页此前把智能检测、云归档都报成 `implementation_pending` /「尚未实现」——那是 M1-A 的占位，在两个模块各自交付了三个批次之后已经不符实，而 PRD §14 明确要求「不能仅因 env=yes 冒充可用」，反向也不能把已交付说成没做。现在四个事实分开报：`enabled`（部署开关）、`implemented`（本版本是否含该模块，**恒为 true**）、`state`（`disabled`/`not_configured`/`healthy`/`unavailable`/`not_available`）、`reason`。`state` 的推导：智能检测——`FrigateEnabled` 关→`disabled`；开着但 `detection_configs` 无启用行→`not_configured`；已配置且 frigate **与** mqtt 都健康→`healthy`（**未观测或观测过期都算不健康**——没有证据不等于可用）；已配置但依赖不健康→`unavailable` 并带组件的 reason。云归档——**不看任何开关**（见下方纠正），无启用目标→`not_configured`，任一目标健康→`healthy`，全部不可达→`unavailable`。**一个归档目标不可达不让整个云归档模块变红**（PRD §14：其他 WebDAV 仍可用），只要有任一启用目标 last_check 为 healthy 就是 healthy。设置页按 PRD 四态渲染（功能未启用 / 待配置 / 运行中 / 运行异常 / 版本尚未提供），待配置时给出去配置的链接，reason 作为支持用代码小字保留。占位端点 `POST /archive-tasks`、`POST /detection-rules` 保留管理员+模块门禁（未启用仍 409 `feature_disabled`），启用后改为 501 `endpoint_not_available` 并在消息里指向真实端点（`/recordings/archive`、`/archive-targets`、`/detection/rules`），不再谎称功能未实现。**这一条曾被误读并导致一次错误修复，用户已纠正（2026-10-16），口径如下**：`ONE_NVR_OPENLIST_ENABLE` 是**模块开关**（起不起自带的 OpenList 容器、是否探测它），**不是功能开关**——它不像 `FrigateEnabled` 那样与功能耦合。**云归档的底座是「挂载的 WebDAV 可用」，而 OpenList 只是众多 WebDAV 目标里最方便的那个默认项**（`internal/archive/webdav.go` 的注释即写明「OpenList 与普通 WebDAV 在这里都只是 HTTP」）。所以 `=no` 时：自带容器不起、不探测，但**归档功能照常可用**，用自己挂的群晖/威联通 WebDAV 一样能归档。
  - 曾按旧口径把 `=no` 解读为「必须暂停所有云端动作」，于是让归档服务在开关关时为 nil、8 个端点返回 404 —— **那是拿掉了用户本来就有的能力**，已全部撤回。
  - 正确做法：`capability.archive` 的 `enabled` **恒为 true**（不绑开关），`state` 只按目标推（无目标→`not_configured`；有健康目标→`healthy`；全部不可达→`unavailable`）；`Require("archive")` 恒放行；归档服务与三个 worker 循环（上传消费、定时扫描、云端到期）无条件运行；`Archive` 的 nil 守卫已删除（nil 不再代表「模块关闭」，留着只会把接线错误伪装成 404）。
  - 智能检测侧才是功能耦合：`d.Detection`/`d.Events` 在 `FrigateEnabled=no` 时为 nil、端点 404，因为检测与事件完全建立在 Frigate+MQTT 之上（capability 的 `unavailable` 判定要求 frigate **与** mqtt 都健康）。
  - 自带 OpenList 的健康**只在运维页按组件显示**，不影响归档模块状态——这正是 PRD §14.5「不把整个归档模块置灰」的要求；「一个目标不可达不让模块变红」同理。
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
