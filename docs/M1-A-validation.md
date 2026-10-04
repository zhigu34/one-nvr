# M1-A 开发验证记录

日期：2026-10-04。开发分支 `codex/m1a-foundation`。这是逐步更新的工程记录，**M1-A 尚未交付**；不能据此认为正式版已能看/录。

已验证的 Task 1 基线：

- M0 原有 Python 测试 35 项通过；未修改 M0。首次受沙箱回环端口限制失败，允许回环绑定后完整重跑通过。
- 官方 Go 1.27.1 darwin/arm64 临时工具链，SHA256 `ee215d57e0ec269c60cc9ceca68e6bda321ba9ee5afe24f4b0988703c2d87d12`；配置/UUID/健康接口先观察缺失实现失败，再实现并通过 `go test -race ./...`。
- `.env` 仅按字面值解析，不 source/eval；可选模块默认关闭，校验协议/入口/RTC 冲突和媒体 IP。数据库不可用时 readiness 为 503，不暴露数据库凭据。
- API、Worker、admin 三入口交叉编译 Linux amd64 成功。Task 1 当时 API/Worker 只有进程生命周期和数据库健康检查；后续功能见各任务记录。
- shadcn-admin 固定 commit `e16c87f213a5ba5e45964e9b67c792105ec74d26` 实际源码及 MIT 许可导入。pnpm 10.12.4；本地 Node 24.15.0；固定 lock 安装、OpenAPI 类型生成、build、lint 通过。生成器 openapi-typescript 7.13.0。
- OpenAPI 尚未实现的业务端点标为 `planned`；前端仍包含上游演示页，只是构建基线，不作为正式产品页面验收。
- 用真实 Docker Compose CLI 对测试清单执行 `config --quiet` 通过；shell 语法校验通过。

待验证：

- 本机没有 Docker daemon。Task 1–3 的固定 Node/Go/PostgreSQL 容器检查与应用/gateway 镜像构建已由下方 GitHub CI 通过；Nginx TLS、真实浏览器与模块部署仍未运行。镜像构建成功不代表正式栈运行验收完成。
- Task 4 已通过下方 GitHub CI；Task 5 目录池完成本机验证，本批容器回归待发布；Task 6–9 证书、网关应用、真实前端和正式部署继续实现。
- 16–32 路容量、真实录像连续性、RTC 连通、Intel GPU 解码/推理；AMD 推理按用户要求暂不作为退出门槛。

正式部署仍全部使用 Docker，不要求两台部署机安装 Go/Node/PostgreSQL。临时宿主工具只用于当前开发验证。私密 M0 真机报告不纳入提交或镜像构建上下文。

Task 2 真实数据库验证：

- PostgreSQL 17.11 Homebrew 官方 GHCR 二进制包 SHA256 `1fbc3c17f3da21f29363a6a942aa9db176e5d808d2c39e86f225ee9f665147db`，仅临时副本重定位安装路径/动态库，测试库只绑定回环；随后 GitHub CI 使用正式固定 Bookworm 镜像完成隔离数据库测试。
- 8 项真实 DB 集成测试通过：并发迁移/校验和、事务回滚、幂等参数冲突、过期租约 fencing、最终尝试崩溃终结、审计秘密拒绝、迁移前/校验和异常不就绪、重试退避与并发领取、续期失败取消执行器。
- 测试发现最终尝试崩溃会停留 running，已修正为持久 failed，并验证没有可领取任务时清理事务仍提交。
- `go test -race ./...`、Linux amd64 三入口交叉编译、`go vet ./...` 通过。admin 提供显式 migrate；API/Worker 就绪检查同时核对已执行迁移。
- Worker 通用执行器已实现；尚未注册业务处理器或启用实际池/证书/归档任务。

Task 3 认证与授权验证：

- 一次性 setup、16/32 槽位初始化、初始化管理员显式全通道授权；持久站点身份/主密钥/CSRF 密钥及初始化令牌只生成缺失文件，损坏/符号链接拒绝，不覆盖重置。
- Argon2id 64 MiB/3 次/并行度 1，执行并发限制；SHA256 会话摘要、30 分钟闲置/12 小时绝对期限。登出、改密、禁用、降权和改通道授权撤销会话。
- HTTP API 已接入 setup/status、setup、login/logout/me/change-password、users、channel-grants。登录与 setup 先从 setup/status 获取签名预认证 Cookie/CSRF；登录后的修改使用会话 CSRF。PUBLIC_URL 决定 Secure，客户端转发头不能覆盖；响应 no-store，普通 DTO 无密码哈希/会话摘要/私密密钥。
- 最后管理员保护、并发降权、资源 404/动作 403、管理员无通道授权也不能绕过、审计插入失败回滚、错误 JSON 400/字段 422/超限 413/严格 If-Match、失败登录无秘密审计均验证。修正了自身授权成功后误报 401，以及带加号/前导零版本被接受的问题。
- 完整 Go race 测试通过，其中 19 项真实 PostgreSQL/HTTP 集成测试；Linux amd64 编译、vet、更新后的前端类型与 build 通过。HTTP 测试运行真实 handler 与数据库，不等于实际 TLS 握手或浏览器验收。
- 前端尚未接入这些 API；实际 gateway Cookie/登录、镜像启动和浏览器流程留在 Task 7–9，不把上游演示认证作为通过证据。

CI 环境选择：用户指定 GitHub Actions。新增 Foundation CI，固定 Docker 运行 Go/真实 PostgreSQL 17.11/Node 前端/M0 回归，再构建 Linux amd64 应用与网关。checkout 固定 v4.2.2 commit。工作流仅具 contents:read，暂不推送发布镜像或部署到真机。

Task 1–3 独立审查与修复：

- 查看者配置权限在授权写入与实际操作处均拒绝；降为查看者时事务清除 configure 授权。
- 账号创建、改权、禁用、登出、改密与入口协议切换共用事务授权锁，撤权完成后旧身份无法提交账号创建。
- 无效用户名不能耗尽账户限流条目；客户端限流、过期清理和有界淘汰保留有效用户登录能力。Task 7 接入代理时需使用网关覆写且仅信任该代理的客户端地址，避免共用限流桶。
- 新增 0002 迁移持久记录入口协议。HTTP/HTTPS 切换撤销全部旧会话；单纯证书续签不触发撤销。
- 四项回归均先复现失败再通过；审查者独立重跑通过，未发现新增 Critical/Important。完整非缓存 `go test -race -count=1 ./...` 通过，其中 23 项真实 PostgreSQL/HTTP 集成测试耗时 64.216 秒；vet 与 Linux amd64 三入口编译通过。

CI 首轮记录：[run 37178220691](https://github.com/zhigu34/one-nvr/actions/runs/37178220691)。M0 35 项回归和 Go 单元 race 检查通过；前端依赖安装通过，类型生成调用失败。根因为 pnpm 10.12.4 对脚本命令前的分离 `--store-dir /pnpm/store` 参数解析错误，把目录当作命令；本机相同 pnpm 已复现。改用 `npm_config_store_dir` 后类型生成无差异。

CI 修复后：[run 37178341246](https://github.com/zhigu34/one-nvr/actions/runs/37178341246) / commit `dd1fe30b7163c44482b374c19d600a22096ae7c7`，完成且 conclusion=success。backend（含 23 项真实 PostgreSQL/HTTP race 回归与 vet）、frontend（固定 Node 24.21.0 下安装、类型漂移、build/lint）、m0-regression（35 项）、images(app)、images(gateway) 五个任务均成功。源码通过 GitHub 连接发布，远端 Git tree 与本机待发布树逐次核对一致；[草稿 PR #1](https://github.com/zhigu34/one-nvr/pull/1) 不作为正式发布。

Task 4 实现与本机验证：

- 固定通道名称编辑与授权范围列表；16→32 只追加槽位、保留原 ID/编号/授权，新通道授权只给执行扩容的管理员。槽位初始化录像模式 none、事件录像 false；本阶段不接收摄像头地址或启动录像。
- 站点名称/时区按 If-Match 事务更新与审计；嵌入 598 个 IANA 名称及 Go 时区规则，公开参考列表提供中文常用标签与当前时间，初始化前可读，不含实际站点配置。
- Worker 10 秒采样，观察 30 秒过期。PostgreSQL 核对迁移就绪；API/gateway/ZLM/启用的 Frigate/OpenList 检查真实 HTTP 响应，MQTT 完成 MQTT 3.1.1 带凭据 CONNECT/CONNACK。拒绝管理接口跳转，不保存上游响应或原始错误。固定私有服务名及内部密钥渲染由 Task 9 部署接线验证。
- 模块开关、实现状态和组件健康分别展示。未来智能/归档动作仅实现权限与门禁：关闭 409、启用但未实现 501；实际依赖失败由就绪与组件探测反映。已启用 OpenList 故障不被当作云目标能力；正式目标业务仍在 M2.1。
- 管理员可读取组件、任务摘要和分页审计，普通用户直接请求拒绝；摘要不含任务 payload 或诊断原始响应。缺失/null grants 请求不再误清空权限，明确空数组仍用于撤权。
- 独立复查发现公开时区错误要求登录，先复现 401 再修复，匿名回归通过。此前完整非缓存 Go race 全套通过，含 33 项真实 DB/HTTP 测试（91.577 秒）；新增匿名时区回归通过，总计 34 项待本批 CI 全量复核。真实 HTTP/MQTT 回环探测、类型生成及前端基线 build/lint、vet/Linux 编译另行通过；正式 Nginx/可选上游容器接线仍待 Task 7–9。

Task 4 GitHub 验证：[run 37179516245](https://github.com/zhigu34/one-nvr/actions/runs/37179516245) / commit `e17ec720e7d7cc210bfbb3b9701fe6ae50e61ddd`，五个 Docker 任务全部成功，含 34 项真实 PostgreSQL/HTTP race 回归。

Task 5 目录池本机验证：

- 只登记允许根 `/storage` 下的既有目录；规范路径和根内文件操作拒绝越界、符号链接逃逸、重复及嵌套池。并发路径登记和默认池变更由真实 PostgreSQL 事务锁/唯一约束验证。
- `.one-nvr.json` 保存站点/池 UUID 及规范路径；缺少身份但存在业务目录时拒绝接管，已登记池丢失根/身份时不补建。数据库提交失败留下的原身份可安全重试；无引用空池移除登记时保留目录与身份，重新登记保留 UUID。
- API 与 Worker 各自每 10 秒读写删除探测，30 秒过期；只写 `.work/probes/`，完成清理临时文件。Worker 持久检查任务能合并重复请求，任务完成只表示已记录检查结果。ZLM 无测试视频源明确待验证，不用应用进程写文件冒充 ZLM。
- statfs 容量按文件系统去重；空间低于 10 GiB 显示阻断，录像控制/动态码率安全线及循环清理留后续。池业务 `used_bytes=null`，等待录像索引，不伪报 0。
- 7 项新增真实 DB/HTTP 测试通过：并发嵌套/默认、检查队列与独立失败/过期、审计回滚身份恢复、文件及任务删除保护、身份失联和权限/不可修改路径/版本冲突。完整非缓存 `go test -race -count=1 ./...` 通过，累计 41 项集成测试耗时 110.140 秒；vet、Linux amd64 编译、前端类型生成/build/lint 通过。
- 当前证据是本机真实文件系统及隔离 PostgreSQL，不等于 Linux bind mount 或 ZLM 实际录像验证；本批固定镜像 CI 与正式目录映射验收分别待发布及 Task 9。

Task 6 证书管理本机验证：

- 支持常规 RSA/EC、PKCS#1/PKCS#8/SEC1 未加密 PEM，组合内容上限 1 MiB；校验密钥配对、IP/DNS SAN、有效期、服务器用途、链顺序/签名和提供链的约束。自签与省略根链可保存，明确区分提供链校验和客户端信任。拒绝忽略私钥前的垃圾文本。
- 版本保存为 `DATA_DIR/tls/versions/<UUID>/` 私有不可变快照（目录 0700、文件 0600）；数据库只保存公共元数据与内部摘要，普通 API 不返回 PEM、私钥或包含私钥的摘要。导入不会宣称网关生效。
- Worker 对固定映射输入重新读取内容，前后核验文件/目录身份；根内符号链接可用、越界/非普通/超限拒绝。完整一致内容两次采样至少间隔 2 秒后保存；10 秒采样，未变化的无效配对 30 秒再校验，新内容恢复检查。半份替换、mtime 不变和同叶换链有实际文件测试。
- 目录模式拒绝手动上传，自动应用开关持久保存；HTTP 模式只保存待用版本，实际应用/回滚请求返回 `tls_https_disabled`。立即检查使用持久任务；有效 HTTPS 候选产生持久应用意图，实际网关处理留 Task 7。重复输入不会产生第二任务。
- 回滚请求与暂停自动应用同事务，保持当前生效 ID 直到网关确认。暂停取消排队的自动应用并保留候选；已开始的自动应用返回 409，要求先完成/对账。此处测试播种历史状态仅验证请求事务，不能代替实际 Nginx 回滚证据。
- 8 项新增真实 PostgreSQL/HTTP 证书测试通过，含私密材料、重复/半份更新、自动任务、篡改拒绝、回滚暂停及审计回滚；完整非缓存 race 回归累计 49 项集成测试（140.886 秒）。证书单位测试、vet、Linux amd64 编译、前端类型生成/build/lint 通过。新增 0003 迁移，旧迁移校验和未改。
- Nginx reload/新连接证书指纹、应用中崩溃与实际目录映射仍待 Task 7/9 的 GitHub Docker CI。当前上游演示前端尚未替换，Task 8 继续。


Task 5–6 GitHub 验证：[run 37182387398](https://github.com/zhigu34/one-nvr/actions/runs/37182387398)，commit `dcfb6cc99fdeb754af7330dce4c1b52583c84480`。五个任务全部成功，包括固定 Go/PostgreSQL/Node 镜像检查、49 项真实 DB/HTTP 回归、35 项 M0 回归及两个 Linux amd64 镜像构建。

Task 7 网关控制与本机验证：

- 同一网关容器中监督 Nginx 与控制进程，使用私有目录传递持久任务/结果。只接受固定 apply/rollback、UUID 及预期当前版本；不接受请求提供的 shell、路径或 Nginx 参数，不挂 Docker socket。运行 UID 10001，公开静态目录 0755/文件 0644，证书私有快照与控制目录分别挂载。
- 候选配置先执行真实 `nginx -t`；持久意图后切配置、reload，再用新 TLS 连接核对叶证书及完整 DER 链摘要。核验失败恢复上一版本再核验；监听状态不明时保持原任务待对账，避免矛盾的终结结果。
- Worker 根据网关持久结果，在有租约 fencing 的事务中写生效 ID、公共结果和审计；过期租约、矛盾结果及错误链拒绝。结果提交与任务完成间崩溃可幂等恢复。排队后的快照校验失败明确终结，不会一直卡在“应用中”。
- 网关覆写客户端地址和内部代理凭据，API 仅在凭据匹配后使用该地址限流；伪造地址回归通过。普通换证书不撤销会话，入口协议切换保持之前的撤销机制。
- 本机完整 race 回归通过，55 项真实 PostgreSQL/HTTP 测试耗时 152.333 秒；末次恢复修复另跑网关 race，Linux amd64 编译/vet、前端类型生成/build/lint、Compose 和 shell 语法均通过。测试构建标签 `gateway_runtime` 缺少独立 Docker 环境即失败，不作为跳过后的通过证据。
- GitHub CI 新增实际 Nginx 容器验收：HTTPS 登录、换证书保留会话、错误密钥、回滚暂停、reload 返回 0 但旧指纹、切配置后崩溃、网关已生效而 Worker 未提交时崩溃、重建/重复结果和同叶换链。**本次提交时该运行仍待 CI 执行；不宣称通过。** ZLM 录像连续性和真机容量仍需媒体环境，M1-A 不作证明。
- Task7 前的开发证书快照缺少新私有 manifest，应重新导入；尚未正式发布的开发快照不作生产升级基线。M0 数据/代码未改。真实基础前端与正式部署留 Task8–9。

Task 7 实际 Docker 验证：[run 37185613818](https://github.com/zhigu34/one-nvr/actions/runs/37185613818) / commit `6e9875951911fbcd14c80187385a8792218fd762`，六项任务全部成功。实际 Nginx 完成 HTTPS 登录、Secure Cookie、更新保留会话、错误密钥拒绝、回滚暂停、旧指纹拒绝、两个崩溃边界恢复、幂等审计及同叶换链验证。以上替代先前提交时的“待运行”状态；未验证 ZLM 录像连续性。

Task 8 前端验证：真实本机 API/隔离 PostgreSQL 的六项浏览器业务流程通过（12.8 秒），密码修改撤销会话另行通过；70 项 Chromium 单元测试通过。页面基于固定上游 shadcn-admin，删除演示业务与 Clerk/faker，采用同源 Cookie/CSRF、生成类型和真实授权。新增管理员专用槽位名称清单用于分配权限，不赋予视频访问权；直接 HTTP/PG 回归通过。Docker 浏览器三配置验收已接入 CI，当前待运行。测试镜像与 Playwright 包同为 1.59.1，按[官方 Docker 文档](https://playwright.dev/docs/docker)固定匹配版本和镜像摘要；不上传可能含令牌/密码的截图、trace 或错误上下文。

Task 8 实际 Docker 验证：[run 37187153806](https://github.com/zhigu34/one-nvr/actions/runs/37187153806) / commit `2d8010efdf2c8564d559291c984b94006fa5d526`，七项任务全部通过：Go/真实 PostgreSQL、类型/build/lint、35 项 M0 回归、两个镜像、实际网关及浏览器。Chromium 单元 70 项通过，真实 Docker 浏览器三种配置各 7 项通过（均约 15.1 秒），包括初始化、权限与撤权缓存、模块状态、HTTP 待用证书、时区和密码修改。无 mock 登录/DB；目录模式隐藏上传并可检查输入。以上替代上一提交时“待运行”。

Task 9 当前实现和待验收：

- 字面量配置由 Go 工具生成私有、按内容寻址的 Compose；五核心与可选服务数量单元测试通过，映射目录 `create_host_path=false`，所有实际数据值中的 `$` 对 Compose 再转义，拒绝浮动上游镜像覆盖。
- 新部署一次生成持续内部凭据，迁移后手动 HTTPS 首次私有自签候选排队；真实 PG 验证重复部署不换 UUID/秘密/证书，不重复任务。默认无业务摄像头源；没有管理员也通过本地工具引导，HTTP 不误报 TLS 生效。
- 自动硬件枚举读取 daemon 宿主 sysfs，按 PCI/实际 render 路径选择 Intel，解码和推理独立自检与报告；GPU 推理失败不静默降级测试通过。固定镜像内使用 FFmpeg 短合成 H.264/TFLite 或 OpenVINO，记录来源和内容 hash，失败保留旧业务配置。报告只读 API 为管理员提供公共设备/状态；未运行显示未检查。
- 新增生产 Docker CI 验收脚本覆盖首次 HTTPS、实际指纹入口检查、重复部署、四种配置，以及停止旧可选容器时 ZLM 容器 ID 保留；本次提交时待运行。浏览器补实际目录池登记/刷新与 ZLM 待测试源文案，待本批 CI。
- **未测**：N5105 真实驱动、权限与 GPU 模型验证；EPYC 实际推理；两路实际媒体录制连续性及 16–32 路容量。容器 ID 保留只证明未重建容器，不证明录像连续性。这些保留 M1-C/M3 退出项；AMD 检测按用户要求不作当前门槛。

Task 9 本机补充检查：完整真实 PG race 57 项通过（159.443 秒），之后新增硬件报告权限回归与重复部署回归合跑通过（7.640 秒）；当前累计 58 项待 CI 全量复核。单位 race、vet/Linux amd64 编译、类型生成/build/lint、70 项 Chromium 单元回归通过。渲染后的 Compose 由实际 Compose CLI 校验通过；宿主仍没有 Docker daemon，不能将这项语法校验当作运行通过。
