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

- 本机没有 Docker daemon。固定 Node 24.21.0 容器内 build/lint、应用/gateway 镜像构建、容器内 Go 测试、PostgreSQL 17.11、Nginx TLS 与模块部署均未运行；Compose 解析不代表容器启动成功。
- Task 4–9：通道/站点/组件、存储池、证书与网关应用、真实前端和正式部署。
- 16–32 路容量、真实录像连续性、RTC 连通、Intel GPU 解码/推理；AMD 推理按用户要求暂不作为退出门槛。

正式部署仍全部使用 Docker，不要求两台部署机安装 Go/Node/PostgreSQL。临时宿主工具只用于当前开发验证。私密 M0 真机报告不纳入提交或镜像构建上下文。

Task 2 真实数据库验证：

- PostgreSQL 17.11 Homebrew 官方 GHCR 二进制包 SHA256 `1fbc3c17f3da21f29363a6a942aa9db176e5d808d2c39e86f225ee9f665147db`，仅临时副本重定位安装路径/动态库，测试库只绑定回环。正式 Bookworm 镜像尚未启动。
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

CI 环境选择：用户指定 GitHub Actions。新增 Foundation CI，固定 Docker 运行 Go/真实 PostgreSQL 17.11/Node 前端/M0 回归，再构建 Linux amd64 应用与网关。checkout 固定 v4.2.2 commit。工作流仅具 contents:read，暂不推送发布镜像或部署到真机；首次实际运行结果待填。

Task 1–3 独立审查与修复：

- 查看者配置权限在授权写入与实际操作处均拒绝；降为查看者时事务清除 configure 授权。
- 账号创建、改权、禁用、登出、改密与入口协议切换共用事务授权锁，撤权完成后旧身份无法提交账号创建。
- 无效用户名不能耗尽账户限流条目；客户端限流、过期清理和有界淘汰保留有效用户登录能力。Task 7 接入代理时需使用网关覆写且仅信任该代理的客户端地址，避免共用限流桶。
- 新增 0002 迁移持久记录入口协议。HTTP/HTTPS 切换撤销全部旧会话；单纯证书续签不触发撤销。
- 四项回归均先复现失败再通过；审查者独立重跑通过，未发现新增 Critical/Important。完整非缓存 `go test -race -count=1 ./...` 通过，其中 23 项真实 PostgreSQL/HTTP 集成测试耗时 64.216 秒；vet 与 Linux amd64 三入口编译通过。
