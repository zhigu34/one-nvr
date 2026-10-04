# one-nvr M1-A Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans for native implementation, or superpowers:subagent-driven-development if the user selects delegation. Implement task-by-task; do not start product code before the user reviews this written plan and selects execution. Steps use checkbox syntax.

**Goal:** 交付可在 Linux 单机部署的正式基础控制面：本地账号、固定通道、目录池、真实组件状态、站点时区及手动/目录证书管理。

**Architecture:** 一个 Go 模块，API 负责授权/事务，Worker 执行持久任务与探测，PostgreSQL 保存业务状态。前端直接导入固定 shadcn-admin commit 并接入真实 API；静态文件由含受限证书控制进程的 gateway 交付。ZLM 始终独立运行。

**Tech Stack:** Go 1.27.1、net/http、pgx/v5、显式 SQL、PostgreSQL 17.11；React/TypeScript/Vite、TanStack Router/Query、shadcn/ui；Docker Compose、Nginx 1.30.5。

**Spec:** [M1 控制面设计](../specs/2026-10-03-m1-control-plane-design.md)、[PRD v0.28](../../PRD.md)、[访问与证书 v2](../../web-access-tls.md)、[镜像版本基线](../../image-versions.md)、[部署硬件自动探测](../../hardware-auto-detection.md)。

**Status:** 2026-10-04 用户要求继续，按推荐方式在当前仓库的 `codex/m1a-foundation` 分支顺序执行。Task 1 已开始；当前通过项与未运行的容器检查见 `docs/M1-A-validation.md`，未勾选步骤不作为功能通过记录。

## Global Constraints

- 单站点 Linux amd64；EPYC 7402 / Intel N5105；槽位初始化为 16 或 32，扩容只允许 16→32，保持原 ID/编号/授权。
- ZLM 为唯一正式录像组件；Frigate `record.enabled=false`。M1-A 不启动摄像头拉流/录制，M1-B 接入与最小录像，M1-C 实时与单段回放；不把 M1-A 标成“能看能录已完成”。
- 保留整个 `deploy/m0` 及既有 .env、SQLite、密钥与录像；正式 Compose 项目名 `one-nvr`，数据使用独立目录。不自动迁移 M0 数据。
- 两模块开关 `ONE_NVR_FRIGATE_ENABLE` / `ONE_NVR_OPENLIST_ENABLE` 为 yes/no，默认 no；核心 5 服务，组合为 5/7/6/8。构建/测试/迁移的一次性容器不计入。
- ONE_NVR_HARDWARE_PROFILE 可省略，默认 auto；仅 Frigate 开启枚举/分项核验，生成最小设备映射，保留高级覆盖。初次无适配GPU可测试CPU，已有GPU失败不静默降级；当前自动范围CPU/Intel，NVIDIA未认证组合不可自动启用，详见硬件设计。
- 默认 Asia/Shanghai，后端 IANA 列表/校验，数据库 timestamptz，API UTC RFC3339；不使用 .env 作为业务时区或摄像头配置来源。
- 默认 HTTPS，PUBLIC_URL scheme 决定活动入口；HTTP 8080 / HTTPS 443 / RTC 8000 默认值，RTC 同号 UDP/TCP，不与活动 Web TCP 端口冲突；IP 直连沿用入口 IP，域名/代理拓扑需明确可达媒体 IP。
- ONE_NVR_TLS_DIR 默认为空；非空只读绑定整个目录至 Worker /tls-input，固定 fullchain.pem / privkey.pem，目录模式不允许手动上传竞争。
- 服务器会话 HttpOnly/SameSite=Lax/Path=/；HTTPS Secure，明确 HTTP 模式不设 Secure；闲置 30 分钟、绝对 12 小时；修改校验 Origin 和 CSRF；密码 Argon2id 64 MiB/3 次/并行度 1。
- 角色管理员/操作员/查看者；每用户每通道 live/playback/export/configure。站点角色不自动授权视频；保护最后一个有效管理员，普通响应不得包含凭据。
- API /api/v1，JSON `{data,request_id}`；错误 `{error:{code,message,fields,retryable},request_id}`；UUID 主键，If-Match 冲突 409，无效字段 422，认证响应 no-store。
- Worker 领取使用 FOR UPDATE SKIP LOCKED，租约 30 秒、10 秒续期，退避 2/4/8/16/30 秒；有外部副作用先持久意图，重试/接手先对账。
- 池为已存在可访问目录，只管规范路径、身份标识和业务文件；不配置 NAS/RAID/挂载。检查按 API/Worker/ZLM 分项，10 秒采样，30 秒过期；无 ZLM 测试源时保持待验证。
- 所有构建/运行 FROM 和上游镜像采用镜像基线的完整 tag@digest；Go/Node 是构建依赖，不增加常驻服务；API/Worker/admin 一个 Debian 基础应用镜像。
- 禁止 Redis/ORM、Docker socket、公开上游管理端口和目录列表。功能未实现返回 501，模块关闭返回 409 feature_disabled；依赖异常返回 503；前端同样按状态限制。

## Review Focus

- 重复/并发初始化：只能产生一个站点及一次管理员授权，令牌/密钥不能被重复部署替换；Task 2/3 用真实 PostgreSQL 验证。
- 权限收紧后旧浏览器状态：隐藏按钮不能替代后端拒绝，改权/禁用撤销会话并清空缓存；Task 3/8 验证直接 HTTP 与浏览器请求。
- 摄像头尚未配置的第一次部署：核心面板可进入，池的 ZLM 探测显示待验证，启用的未来模块显示未实现/未配置而非成功；Task 4/5/9 验证。
- 目录或证书输入被外部替换：重新打开内容、检查身份及根路径，不写入消失路径；Task 5/6 验证半份证书、mtime 不变与越界符号链接。
- 网关已 reload 但任务结果未提交：新 TLS 握手指纹与持久意图对账，不凭命令成功冒充生效；Task 7/9 验证崩溃和回滚暂停自动监测。

## 文件与接口边界

以下均为计划创建路径，当前不是已存在实现。Go 模块路径 `github.com/zhigu34/one-nvr`。

| 单元 | 文件/目录 | 责任 |
| --- | --- | --- |
| 构建与开发工具 | `go.mod`, `go.sum`, `deploy/production/dev.sh`, `deploy/production/compose.test.yaml`, `deploy/production/Dockerfile.app`, `deploy/production/Dockerfile.gateway` | 容器内构建/测试，应用与静态网关镜像 |
| 应用入口 | `cmd/api/main.go`, `cmd/worker/main.go`, `cmd/admin/main.go`, `cmd/gateway-control/main.go` | API、后台、初始化/迁移/部署渲染、网关控制 |
| 配置与 HTTP | `internal/config/`, `internal/httpapi/`, `internal/id/` | 不执行 .env、严格 DTO/错误/分页/授权边界、UUID |
| 数据基础 | `internal/database/`, `internal/jobs/`, `internal/audit/`, `migrations/` | SQL 迁移、事务、任务租约和脱敏审计 |
| 业务基础 | `internal/auth/`, `internal/site/`, `internal/channel/`, `internal/capability/`, `internal/operations/` | 本地会话、固定槽位、时区、模块/组件状态 |
| 存储与证书 | `internal/storage/`, `internal/media/zlm/`, `internal/tlsmanager/`, `internal/gatewaycontrol/` | 目录池探测、受限上游访问、不可变证书快照及应用 |
| 真实前端 | `apps/web/`, `apps/web/UPSTREAM.md`, `docs/api/m1a.openapi.yaml` | 上游来源记录、中文页面、生成 API 类型 |
| 发布部署 | `deploy/production/compose.yaml`, `deploy/production/.env.example`, `deploy/production/deploy.sh`, `deploy/production/templates/`, `deploy/production/README.md` | 模块选择、渲染配置/秘密、升级与故障说明 |
| 验收 | `tests/integration/`, `tests/e2e/`, `docs/M1-A-validation.md` | 临时数据库/浏览器/真实容器证据，不保存部署密码 |

统一基础类型由 Task 1 定义：`id.ID` 为 UUID 字符串类型；`config.Config` 是已校验部署配置。Task 2 定义 `database.DB` 包含 `*pgxpool.Pool`，事务函数 `WithinTx(ctx, func(pgx.Tx) error) error`。所有 Service 接受 context.Context；公开输出独立 DTO，不直接 JSON 编码含秘密的数据库结构。

所有具体目录下实现按责任拆 `service.go` / `repository.go` / `handler.go`，测试位于相邻 `*_test.go`，数据库集成测试放 `tests/integration`。不预创建 M1-B/C 的空业务模块/页面来冒充交付。

验证工具由 Task 1 交付：`./deploy/production/dev.sh test-go <packages>`、`test-db <packages>`、`web <pnpm arguments>`、`e2e`。test-db 使用唯一测试项目名、临时 PostgreSQL 卷、内部 TEST_DATABASE_URL，退出清理仅自己创建的测试资源；不能接收生产 DATABASE_URL 为测试库。宿主只需 Docker/Compose，无需安装 Go/Node/Python。

## Task 1: 可构建工程、固定上游与配置契约

**Files:** Create: `go.mod`, `go.sum`, `cmd/api/main.go`, `cmd/worker/main.go`, `cmd/admin/main.go`, `internal/id/id.go`, `internal/config/config.go`, `internal/config/env.go`, `internal/config/config_test.go`, `deploy/production/dev.sh`, `deploy/production/compose.test.yaml`, `deploy/production/Dockerfile.app`, `deploy/production/Dockerfile.gateway`, `docs/api/m1a.openapi.yaml`, `apps/web/UPSTREAM.md`；Import: `apps/web/`；Modify: `.gitignore`。

**Interfaces:** Produces `config.Parse(io.Reader) (Config,error)`, `config.Config.Validate() error`, `id.New() (ID,error)`, `id.Parse(string) (ID,error)`；API 错误模型和本阶段各端点请求/结果先进入 OpenAPI，健康入口固定 `/health/live` 和 `/health/ready`（进程存活/依赖就绪），gateway 内网 `/health` 验证静态入口而非摄像头可用性。工具入口使用上文 dev.sh 契约。

- [ ] **Step 1:** 写 `TestParseDoesNotExecuteEnv`、`TestValidateSelectedEntryAndRTC`：包含引号/# 的值保真，`$(...)`/反引号不执行；缺省两开关 no，非 yes/no 拒绝，URL 8443 对应 HTTPS 8443 可用，HTTP 8080 无证书可用，RTC 冲突/非整数拒绝，IP 自动媒体地址和域名缺少覆盖分别接受/拒绝。
- [ ] **Step 2:** 创建只用于运行这些测试的 Go 测试容器/模块声明，运行 `dev.sh test-go ./internal/config ./internal/id`，记录缺失 Parse/Validate 实现导致失败；不为脚手架文件写镜像式测试。
- [ ] **Step 3:** 实现配置解析/验证、UUID 基础和 API/Worker/admin 可编译入口。直接导入上游 `e16c87f213a5ba5e45964e9b67c792105ec74d26` 的真实源码及 MIT 许可，禁止另写相似页面代替上游；UPSTREAM.md 记录原始文件范围/commit/后续删改。使用 pnpm 10.12.4 作为待验证固定候选，首次确认包元数据/Node 兼容和 lock 可安装后记录版本及完整性；如候选不能构建，先记录原因并修订基线，不默换 latest。
- [ ] **Step 4:** 实现容器测试/构建工具、两 Dockerfile 和 OpenAPI；app 静态构建 Go 程序，Task 1 的 gateway 先只交付静态页面；Task 7 再加入编译后的 gateway-control，不能在此依赖尚不存在的入口。保留上游 lock，改依赖后只在固定包管理器中更新；额外系统包采用可获取的固定快照/版本，不取消 TLS 校验。首次锁定 pgx/v5、x/crypto、前端类型生成工具的具体版本并提交 lock/sum。
- [ ] **Step 5:** 运行上述单元测试、`dev.sh web install --frozen-lockfile`、`dev.sh web build`、`dev.sh web lint`，构建应用入口；此时是上游可构建基线，不是业务登录验收。提交 `build: establish pinned Go and shadcn-admin toolchain`。

## Task 2: PostgreSQL 迁移、事务、任务与审计

**Files:** Create: `internal/database/{database,migrate}.go`, `internal/jobs/{repository,worker}.go`, `internal/audit/repository.go`, `migrations/0001_foundation.sql`, `tests/integration/{database,jobs}_test.go`；Modify: `cmd/admin/main.go`。

**Interfaces:** Produces `database.Open(ctx,dsn) (*DB,error)`, `DB.WithinTx`, `database.Migrate(ctx,*DB) error`；`jobs.Enqueue(ctx,pgx.Tx,Input) (id.ID,error)`, `jobs.Claim(ctx,kind string) (Lease,error)`, `jobs.Renew(ctx,Lease) error`, `jobs.Complete(ctx,Lease,Result) error`。Input 包含 Kind/ObjectID/IdempotencyKey/非敏感 JSON；Lease 含 ID/Attempt/FencingToken/ExpiresAt。`audit.Append(ctx,pgx.Tx,Entry) error` 不接受密码、完整 URI 或 PEM。

- [ ] **Step 1:** 写 `TestMigrationConcurrentAndChecksum`、`TestTxRollsBackJobAndAudit`、`TestExpiredLeaseCannotCommit`：并发 migrate 仅一套 schema，已执行 SQL 校验和不符拒绝；事务失败无半份 job/audit；30 秒租约接手后旧 token 完成拒绝，同幂等键不同参数冲突。
- [ ] **Step 2:** 运行 `dev.sh test-db ./tests/integration -run 'Migration|Tx|Lease'`，确认实现缺失失败，真实数据库测试不得自动 skip 为通过。
- [ ] **Step 3:** 实现串行 migration/checksum 和上述接口。建 sites/users/sessions/channel_grants/channels/recording_policies、storage_pools/checks、jobs/job_attempts、component_observations/audit_logs、tls_certificates/gateway_tls_state；M1-B 来源/媒体表在对应后续迁移加入。FK 保留历史、编号唯一、部分唯一默认池；session 只保存摘要，证书只保存公共信息/受限文件引用。admin `migrate` 命令返回失败则 API/Worker 不就绪。
- [ ] **Step 4:** 实现租约 30 秒、续期 10 秒、退避 2/4/8/16/30 秒，以及独立锁/失联停止副作用契约；测试租约和审计断开路径，不能以内存队列替代。重新运行数据库测试，无生产库修改。
- [ ] **Step 5:** 提交 `feat: add transactional foundation schema and durable jobs`。

## Task 3: 一次初始化、本地会话及账号/通道授权

**Files:** Create: `internal/auth/{service,password,session,authorize,handler}.go`, `internal/site/setup.go`, `internal/httpapi/{router,response,origin}.go`, `tests/integration/{setup,auth,authorization}_test.go`；Modify: `cmd/api/main.go`, `cmd/admin/main.go`。

**Interfaces:** Produces `auth.Service.Authenticate(ctx,rawSession string) (Principal,error)`；Principal 含 UserID/SessionID/Role/AuthVersion，`auth.Service.RequireChannel(ctx,Principal,id.ID,Action) error`；`site.Setup(ctx,SetupInput) (Site,error)`，SetupInput 含 Token/AdminName/AdminPassword/Name/Timezone/ChannelCount。`httpapi.NewHandler(Dependencies) http.Handler` 只挂本计划端点，不开放媒体或 ZLM 管理代理。

- [ ] **Step 1:** 写 `TestConcurrentSetupConsumesTokenOnce`、`TestSessionExpiryAndRevocation`、`TestDirectUnauthorizedChannelDenied`、`TestLastAdminProtected`：并发初始化只有一个站点/16或32槽位；初始化管理员获全部槽位动作，新用户无授权；闲置30分钟/绝对12小时、登出/改密/禁用失效；越权资源404、已知动作禁止403；最后管理员不可禁用/降权。
- [ ] **Step 2:** 运行 `dev.sh test-db ./tests/integration -run 'Setup|Session|Unauthorized|LastAdmin'`，确认失败。
- [ ] **Step 3:** 实现一次 setup token、持久秘密文件安全初始化（重复初始化不覆盖）、Argon2id 限制并发、服务器会话与上述权限接口；实现 setup/status、setup、login/logout/me/change-password、users 和 channel-grants 端点。管理员策略不能绕过通道动作检查。
- [ ] **Step 4:** 加 `TestHTTPAndHTTPSCookiesAndCSRF`：按受控 PUBLIC_URL 配置决定 Cookie Secure，忽略客户端伪造 X-Forwarded-Proto；缺/错 Origin 与 CSRF 拒绝修改，认证 no-store。改权更新授权版本/撤销关联会话，审计失败不提交变更；M1-C 接入实际媒体关闭，不在此声称已撤销 RTC 连接。
- [ ] **Step 5:** 重跑以上真实 DB/HTTP 测试，确认所有普通响应无哈希、session摘要、setup token、密钥；提交 `feat: implement local setup sessions and channel authorization`。

## Task 4: 固定通道、站点时区、能力与组件状态

**Files:** Create: `internal/site/{service,timezones,handler}.go`, `internal/channel/{service,handler}.go`, `internal/capability/{service,handler}.go`, `internal/operations/{service,handler}.go`, `tests/integration/{site,channel,capability}_test.go`；Modify: `internal/httpapi/router.go`, `cmd/worker/main.go`, OpenAPI。

**Interfaces:** Produces `site.Service.Update(ctx,Principal,expected int64,Update) (Site,error)`、`Expand(ctx,Principal,expected int64) ([]channel.Channel,error)`；`channel.Service.List(ctx,Principal,Cursor) (Page,error)`、`Update(ctx,Principal,ID,expected int64,Update) (Channel,error)`；`capability.Service.Snapshot(ctx) (Snapshot,error)`；`operations.Service.Observe(ctx,Observation) error`。Snapshot 分开包含部署 enabled、业务 implemented、状态、reason，组件健康不是云目标能力。

- [ ] **Step 1:** 写 `TestExpansionPreservesIDsAndGrants`、`TestSiteTimezoneAndVersion`、`TestCapabilitiesSeparateIntentFromImplementation`：16→32追加，禁止缩容/重编号，执行管理员获新槽位，其他用户不隐式获权；上海/东京持久保存，无效422/旧版本409；no/no禁用，yes模块组件异常不可伪报未启用，M2.1/M3功能即使容器健康仍 not_available。
- [ ] **Step 2:** 运行 `dev.sh test-db ./tests/integration -run 'Expansion|Timezone|Capabilities'`，确认失败。
- [ ] **Step 3:** 实现 sites/channels CRUD 与受控扩容、IANA 支持列表（含中文标签/当前时间预览，嵌入Go时区数据），统一 If-Match/脱敏审计；M1-A 不接受摄像头 URL、修改录像策略或池改绑副作用，这些属于 M1-B。
- [ ] **Step 4:** Worker 经私有服务名/真实 HTTP 状态采样 PostgreSQL/ZLM/gateway 与已启用的 Frigate/OpenList，MQTT 经认证协议连接验证；API/Worker 通过自身 readiness/持久心跳暴露真实状态。初始化无摄像头不阻塞核心就绪；每个观察带时间/过期，失联为unknown/unavailable。实现 capabilities、operations/components、jobs/:id、分页 audit-logs。未来智能/归档动作直接501，关闭409，依赖异常503，模块未启用不离线告警。
- [ ] **Step 5:** 加 `TestObservationExpiresAndOptionalDoesNotFailCore` 和 `TestAuditVisibility`，验证旧正常观测过期、普通用户不能看站点敏感诊断；重跑 Task 4 测试并提交 `feat: expose stable channels timezone and real capability states`。

## Task 5: 目录池登记与分服务探测

**Files:** Create: `internal/storage/{service,path,marker,probe,handler}.go`, `internal/media/zlm/client.go`, `internal/storage/{path,marker,probe}_test.go`, `tests/integration/storage_test.go`；Modify: Worker、HTTP router、OpenAPI。

**Interfaces:** Produces `storage.Service.Register(ctx,Principal,RegisterInput) (Pool,error)`、`Update(ctx,Principal,ID,expected int64,Update) (Pool,error)`、`RequestCheck(ctx,Principal,ID) (id.ID,error)`；`storage.Probe.Check(ctx,Pool) (Check,error)` 输出 API/Worker/ZLM 独立结果。`zlm.Client.Health(ctx) error` 与 `zlm.Client.ProbeWrite(ctx,ProbeInput) (WriteEvidence,error)` 是受限适配；ProbeInput 引用已登记池/临时流，不接受请求任意路径或 secret。

- [ ] **Step 1:** 写 `TestPoolRootAndNestedConcurrency`、`TestMarkerNotRecreated`、`TestAPISuccessDoesNotImplyZLMWritable`：拒绝根外/符号链接逃逸/重复嵌套；无标识但已有保留目录拒绝自动接管，旧标识失联不补建；API可写但Worker失败或ZLM待验证不能 ready。
- [ ] **Step 2:** 运行 `dev.sh test-go ./internal/storage` 和 `dev.sh test-db ./tests/integration -run Pool`，确认失败。
- [ ] **Step 3:** 实现既有目录规范路径、根约束访问、.one-nvr.json 身份与事务级路径冲突锁，探测只用专用 .work/probes 文件；接口 storage-pools 的登记/默认/启停/无引用空池删除、test。不创建不存在的池根，不改已有媒体池路径，不扫描底层NAS/RAID。
- [ ] **Step 4:** API/Worker各自独立读写/删/空间检查，Worker调度分项结果，ZLM无临时测试源记录 pending；保留 ProbeWrite 契约并在M1-B源测试时闭环，不能用Worker写文件冒充ZLM写入。10秒检查/30秒过期，statfs文件系统身份合并共享容量，池业务占用独立展示；低空间显示阻断，M1-B连接录制阻断，M1-A不实现循环清理。
- [ ] **Step 5:** 加 `TestConcurrentDefaultPoolUnique`、`TestSharedFilesystemCapacityNotDoubled`、`TestLostRootNotRecreated`，重跑路径/数据库检查，提交 `feat: register directory pools with honest service-level checks`。

## Task 6: 手动与映射目录证书的版本、校验和监测

**Files:** Create: `internal/tlsmanager/{validate,repository,service,watcher,handler}.go`, `internal/tlsmanager/{validate,watcher}_test.go`, `tests/integration/tls_test.go`；Modify: Worker、HTTP router、OpenAPI。

**Interfaces:** Produces `tlsmanager.Validate(chain,key []byte,host string,now time.Time) (Metadata,error)`；Metadata 是公共 X.509 信息。`tlsmanager.Service.Import(ctx,Principal,chain,key []byte) (Version,error)`、`CheckDirectory(ctx) (CheckResult,error)`、`SetAutoApply(ctx,Principal,expected int64,enabled bool) error`、`RequestApply(ctx,Principal,ID,expected int64) (id.ID,error)`、`RequestRollback(ctx,Principal,expected int64) (id.ID,error)`。内部 FileRefs/幂等摘要不公开。

- [ ] **Step 1:** 用测试CA/自签 fixture 写 `TestValidatePairSANValidityAndLimit`、`TestWatcherPartialReplacementAndSameMtime`：RSA/EC/PKCS1/PKCS8、IP/DNS SAN、自签/省略根链可用；错误密钥、顺序/用途/有效期、总量>1MiB拒绝；先换chain后换key保持旧版本，mtime不变但内容变化仍识别。
- [ ] **Step 2:** 运行 `dev.sh test-go ./internal/tlsmanager`，确认缺失实现失败。
- [ ] **Step 3:** 实现上述校验/受限 DATA_DIR/tls/versions 不可变快照，手动上传来源和私钥不回显/下载；HTTP只保存待用。目录模式固定文件/根内符号链接/实际可读约束，10秒重新读取内容，前后身份校验、至少2秒间隔的两次一致完整采样；异常30秒重试，相同错误聚合；内容一致不生成任务，同叶证书换链仍处理。第一份目录输入坏且无旧快照不生成自签替代。
- [ ] **Step 4:** 持久化自动应用开关默认true，与版本/审计/任务同事务；目录模式上传拒绝，回滚请求同时暂停；DB失联不应用新版本，恢复检查最新文件。实现settings/tls及watch/check/import/apply/rollback端点，管理员授权/If-Match/有界上传/no-store，返回公共信息/待用状态。
- [ ] **Step 5:** 加 `TestWatcherDuplicateAndChainOnlyChange`、`TestHTTPDirectoryPendingAndRollbackPauses`、`TestPrivateMaterialNeverSerialized`，分别运行单元与 `dev.sh test-db ./tests/integration -run TLS`；提交 `feat: manage validated certificate versions and external renewal input`。

## Task 7: gateway 内受限应用、指纹核验及恢复

**Files:** Create: `cmd/gateway-control/main.go`, `internal/gatewaycontrol/{protocol,controller,nginx,reconcile}.go`, `internal/gatewaycontrol/controller_test.go`, `tests/integration/gateway_tls_test.go`, `deploy/production/templates/{nginx-http,nginx-https}.conf`, `deploy/production/gateway-entrypoint.sh`；Modify: `internal/tlsmanager/service.go`, `Dockerfile.gateway`。

**Interfaces:** Produces JSON ApplyRequest `{job_id,certificate_id,expected_active_id,operation}` 和 ApplyResult `{job_id,state,active_id,leaf_sha256,error_code}`，只固定 apply/rollback；`gatewaycontrol.Controller.Apply(ctx,ApplyRequest) (ApplyResult,error)`、`Reconcile(ctx) error`。Worker持久任务写控制目录，gateway按固定版本根解析，不能执行请求提供的shell/path/任意nginx参数。

- [ ] **Step 1:** 写 `TestInvalidRequestNeverExecutes`、`TestReloadExitZeroWrongFingerprintFails`、`TestRestartRecoversSwitchedButUncommitted`：越界ID/版本冲突拒绝；reload返回0但新握手指纹不同不得成功；切引用后崩溃根据意图/实际指纹确定恢复并保留旧版本，而非信任半更新current文件。
- [ ] **Step 2:** 运行 `dev.sh test-go ./internal/gatewaycontrol`，确认失败。单元测试可注入Nginx执行器，但真实reload必须另做容器集成。
- [ ] **Step 3:** 实现持久意图/上一版本、同网关互斥、候选 nginx -t、原子配置引用、reload、新连接叶证书指纹核验及失败回滚再核验。gateway启动先对账，控制进程与nginx由entrypoint监督退出/信号转发，不额外容器，不让API执行另容器nginx，不挂Docker socket。
- [ ] **Step 4:** Worker接收已持久结果并事务记实际生效/审计；保存失败留证据、执行恢复流程，不伪报完成。HTTP不TLS reload/握手；入口协议切换撤销旧会话，正常换证书不登出；网关静态目录0755/文件0644，秘密仅必要服务可读，目录列表与内部控制文件不公开。
- [ ] **Step 5:** 运行 `dev.sh test-db ./tests/integration -run GatewayTLS`，实际Nginx容器检查有效更换、错误key/指纹/崩溃/重复请求、回滚暂停和重建持久；提交 `feat: apply TLS versions through restricted gateway control`。ZLM录像连续性由Task9在媒体环境验证，不由mock宣称通过。

## Task 8: shadcn-admin 接入真实基础页面

**Files:** Modify: `apps/web/src/stores/auth-store.ts`, `apps/web/src/components/layout/`, `apps/web/src/routes/`, `apps/web/src/features/`, `apps/web/package.json`, `apps/web/pnpm-lock.yaml`；Create: `apps/web/src/lib/{api-client,api-types}.ts`, `apps/web/src/features/{setup,channels,storage-pools,users,site-settings,tls-settings,operations}/`, `tests/e2e/m1a.spec.ts`。

**Interfaces:** Consumes OpenAPI及Task3–7真实API。Produces `apiRequest<T>(path:string,init?:RequestInit):Promise<T>`，使用同源 Cookie credentials 和CSRF，401清认证/敏感缓存，统一结构错误；`api-types.ts` 由OpenAPI生成，生成命令 `pnpm api:generate`。页面无自定义秘密token持久化。

- [ ] **Step 1:** 写浏览器流程 `setupPersistsAfterReload`、`viewerCannotConfigureChannel`、`disabledAndNotImplementedModulesAreDistinct`、`tlsPendingIsNotActive`：初始化16槽位后刷新登录恢复；查看者直接访问配置路由/API拒绝；关闭模块文案与未实现不同；目录源/上传/生效状态真实展示。
- [ ] **Step 2:** 使用临时DB和真实API/gateway运行 `dev.sh e2e`，确认业务页面缺失导致失败，不用mock登录/静态假数据作为通过依据。
- [ ] **Step 3:** 保留上游布局、组件、暗亮主题；删除演示数据、无关页面/Clerk与faker业务依赖，保留MIT来源。实现setup/login、通道槽位/用户授权、目录池、站点时区、访问与证书、组件状态/任务/审计页面。站点设置显示当前时间预览，证书显示公共元数据/检查/应用状态和30/7天到期。默认中文暗色；组件状态可用5秒查询，M1-C再接状态SSE。
- [ ] **Step 4:** M1-A导航只展示已交付页面：总览、通道管理、存储池、用户与权限、系统设置、运维与审计。实时/录像入口在M1-C加入；事件/规则在M3加入。总览只统计真实配置/状态，不显示假在线率/事件数；没有数据明确未配置/未提供。所有页面有加载/空/错误/无权限，操作按角色与部署能力限制，保存用If-Match，管理员自己授权也遵守后端约束。
- [ ] **Step 5:** 加 `permissionsChangeClearsStaleClientState`、`timezoneChangesDisplayWithoutRestart`；运行 `dev.sh web api:generate`、`dev.sh web build`、`dev.sh web lint`、`dev.sh web test`、`dev.sh e2e`；检查生成类型无diff、浏览器无秘密持久缓存/第三方登录请求，提交 `feat: connect shadcn-admin foundation pages to local APIs`。

## Task 9: 正式模块化部署与可交付验收

**Files:** Create: `deploy/production/compose.yaml`, `deploy/production/.env.example`, `deploy/production/deploy.sh`, `deploy/production/README.md`, `deploy/production/templates/{zlm.ini,mosquitto.conf,frigate.yaml}`, `internal/config/render.go`, `internal/hardware/{discover,select,report}.go`, `internal/hardware/select_test.go`, `tests/integration/{deployment,hardware,smoke}_test.go`, `docs/M1-A-validation.md`；Modify: `cmd/admin/main.go`, `README.md`, `.gitignore`。

**Interfaces:** admin `render-deployment --env-file <path> --output-dir <path>` 解析配置并生成不可变服务清单/Compose覆盖，`init-secrets`只生成缺失秘密并核验已有身份，`migrate`串行执行。`deploy.sh --env-file <path>` 唯一部署入口，`--check`只校验/报告、不启停；env文件只读传入admin容器，不source/eval。硬件模块产生 `hardware.Discover(ctx) (Inventory,error)` 和 `hardware.Select(Inventory,Validation,Override,Prior) (Proposal,error)`，Proposal 分别列解码/推理、稳定设备标识和最小映射；测试工具由 deploy.sh 控制，不在 API/Worker 挂 Docker socket。报告保存在 DATA_DIR/hardware/latest.json 并由后端读取，不把运行记录写入 .env。首次准备工具镜像使用固定bootstrap清单，CLI参数可指定已构建的admin镜像；覆盖镜像引用由受限解析器校验后进入生成清单。

- [ ] **Step 1:** 写 `TestDeploymentModuleMatrixAndSelectedPorts`、`TestDisableStopsOnlyOptionalServices`、`TestRepeatedDeployPreservesState`：4组合为5/7/6/8，默认只核心，关闭不要求可选镜像/GPU/secret；开→关明确stop旧可选服务且不删卷、不stopZLM；重复启动不覆盖secret/证书/站点；TLS目录只读整目录，输入缺失不自动创建；Web/RTC监听与协商一致。加 `TestAutoHardwareDisabledAndStableIdentity`、`TestAutoHardwareSeparatesDecodeAndInference`、`TestPriorGPUFailureDoesNotFallback`：关闭不启动工具/要求设备，Intel节点变号按PCI身份选择，多候选顺序稳定，服务器显示卡不当加速器；解码成功推理失败独立报告；无GPU初选CPU需验证，已有GPU失败保留期望并返回模块故障。
- [ ] **Step 2:** 运行 `dev.sh test-go ./internal/hardware` 和 `dev.sh test-db ./tests/integration -run 'Deployment|Hardware'`，确认失败，随后用真实 `docker compose config --quiet` 验证渲染输出，不能仅检查YAML字符串。
- [ ] **Step 3:** 实现独立项目/目录、上述admin工具和deploy.sh；只准备本次所需镜像，检测本地镜像amd64/ID/RepoDigests并记录离线载入例外；不把换apt/pnpm源当Docker Hub鉴权修复。实现缺省auto硬件枚举、固定Frigate镜像中最小映射自检、自动候选选择/报告；使用短且有来源/hash的编码样本及兼容模型输入，不用CPU品牌或节点存在判断通过，不自动切换未登记镜像。只读枚举目标Docker daemon宿主，无法探测则unknown；分项结果有超时与失败诊断，保持旧设备/业务配置。生成5核心和profile服务；ZLM私网管理、独立RTC UDP/TCP；Frigate仅一服务按硬件覆盖映射，CPU/Intel模板无正式录像；OpenList持久配置不默认映射录像根。无业务检测/云目标时不假报可用。
- [ ] **Step 4:** 在启用检测时实际运行硬件探测，验证CPU/Intel自动选择及驱动/权限/节点变化，实际检测自检优先N5105，EPYC推理仍可未测且不作M1-A门槛；样本自检通过不能代替M3实际ZLM业务流、事件和容量验证。实际启动空站点，完成初始化→登录→用户授权→槽位命名→目录登记/待ZLM验证→时区→证书上传及目录文件更换→重启恢复。核心失败返回失败，可选已启用失败报告部分失败且保留核心，不以exit0全成功掩盖；智能/归档未来动作仍501。关闭模块不产生离线告警，健康外部WebDAV操作在M2.1接入后独立处理。
- [ ] **Step 5:** 运行全部 `test-go`、`test-db`、前端build/lint/test/e2e，构建两镜像，实际HTTP/HTTPS与新TLS指纹验证；用两路媒体验证证书更换/可选故障不重启ZLM（可用专用验证流，不以M0槽位或产品页面冒充M1-B已实现）。没有媒体环境则明确记未测并保留M1-C退出项，不能宣称不中断录像已验收。回归原M0测试，仅检查未引入回归，不上传私密真机报告。
- [ ] **Step 6:** 更新正式README操作命令、镜像/源码/schema/前端commit、实际通过/失败/未测报告；列清M1-A尚无摄像头配置、录制和回放UI。请求按用户选择方式的一次独立审查并修复实际问题，提交 `feat: ship modular M1-A deployment and validation evidence`，推送开发分支/PR供评审；不要把未测目标自动标通过或合并main。

## 覆盖与交付检查

| 需求 | 负责任务 | 退出依据 |
| --- | --- | --- |
| 固定上游/构建版本、Docker-only工具 | 1/9 | 来源和lock可追踪，实际构建成功 |
| 初始化、账号会话、通道权限/最后管理员 | 2/3/8 | 真实DB+直接HTTP+浏览器测试 |
| UUID/If-Match/幂等/审计/任务租约 | 1/2/3/4 | 并发及失败事务不产生半状态 |
| 固定槽位、扩容、业务时区 | 3/4/8 | 原ID保持、时区保存/刷新/重启保持 |
| 模块开关、核心5容器、能力/组件健康 | 4/8/9 | 四组合与开→关实际部署结果 |
| 自动硬件档案、设备/解码/推理分项报告 | 4/9 | 关闭跳过、CPU/Intel样本自检与异常报告；业务认证M3 |
| 目录池、身份、分项探测/容量状态 | 5/8/9 | API/Worker可用；ZLM无源明确待验证，M1-B闭环 |
| HTTP/HTTPS/RTC参数边界 | 1/3/7/9 | 配置、Cookie、端口及实际入口；RTC播放M1-C |
| 手动/目录证书、自动应用、回滚/恢复 | 2/6/7/8/9 | 实际Nginx新连接指纹+故障重启；录像连续性单列 |
| 敏感数据与M0隔离 | 全部 | 不改M0、不输出秘密、正式数据独立 |

M1-A不交付源草稿/CSV/密码查看/源切换、连续录制/录像文件发布、WebRTC/多画面/回放、真实智能事件和云归档，分别进入已确定M1-B/C、M2/M2.1/M3计划。它不改变事件录像默认false、事件索引90天、云归档手动/定时及最大保留期限这些约束。

## 执行交接

请用户审阅本计划后选择：

1. **本会话顺序实现（推荐）**：在当前仓库使用 `codex/` 开发分支，逐任务测试/提交；不额外创建工作树。任务共享DB/API/证书协议，顺序实现便于保持接口一致。
2. **隔离工作树与子代理分任务实现**：用户选择即授权创建托管工作树及委派任务，逐任务独立审查；按对应技能执行。

书面计划评审与执行方式选择前只进行设计/只读核对，不导入产品源码、安装依赖或创建正式服务。实现后的发布申请必须提供代码、验证报告及可审阅PR；当前计划没有授权直接合并main。
