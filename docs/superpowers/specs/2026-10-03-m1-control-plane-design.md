# one-nvr M1：正式控制面设计

日期：2026-10-03。依据：[PRD v0.19](../../PRD.md)、已确认的 Go + PostgreSQL 选型及 M0 限定范围验证。

状态：设计待评审；本文中的模块、表和接口均为拟实现契约，不表示已经交付。

## 1. 目标与交付边界

用户需要在单台 Linux 主机上，通过统一面板管理 16/32 个固定通道，接入或更换摄像头、添加已经挂载的目录存储池、控制通道权限并观看实时画面。更换视频源后，通道 ID、授权、池绑定和历史录像保持连续。

M1 交付：初始化、本地账号、通道授权、固定槽位、结构化视频源及版本历史、密码查看/配置导出、CSV/JSON 批量导入、目录存储池、源切换与回滚、实时多画面、基础状态/审计，以及支撑换源验收的最小录像闭环。

最小录像闭环包含连续录制、完成分片索引、按通道/时间列出分片、授权播放单段和源切换标记。M2 扩展周计划、连续时间轴、文件管理、导出/保护、循环清理和完整性校验；M2.1 实现手动/定时/立即云归档与云端到期删除；M3 实现正式智能规则与事件流程；M4 完成可靠性汇总和发布验收。事件索引 90 天、默认云保留 365 天等已确认要求继续有效。

M1 的录像不自动清理；界面明确显示此阶段限制，并在安全空间不足时阻断受影响池的新增录制。当前两路样本验证用于功能验收，16/32 是设计规模；AMD 检测、容量认证和剩余长期实验不会冒充已通过。

### 1.1 事件录像开关

在原通道录像策略中增加 `event_recording_enabled`，默认 false，随事件录像能力交付；不新增站点运行模式或改变里程碑。关闭后检测、截图和事件实际起止/通道信息照常保存，不自动生成单独事件视频或启动用于事件前录的滚动视频缓冲。原连续/计划/手动录像独立计算，有这些录像时仍可关联已有片段；全部常规录像关闭且事件开关关闭时，不因事件到达开始录制。Frigate 正式录像仍关闭，截图保持原配置；其检测/跟踪/抓拍必需的内部缓冲可保留，并以锁定版本实际依赖验证。

事件页没有本机录像时仍展示截图、通道、带时区的实际时间，并支持复制时间信息供用户到海康 NVR 人工查询；结束时间未知如实显示。无 clip、Frigate HTTP 404 不否定已持久化 MQTT 事件。不增加 NVR 通道映射、自动对接/回放或事件专用部署流程。关闭开关收尾当前事件任务，后续事件、换源和重启不复位开关；已有媒体不删除。基础检测/事件仍按 M3 交付，M1 只预留策略字段和统一调度边界。

## 2. 方案与技术边界

选择一个 Go 模块、API/Worker 两个进程、一个 PostgreSQL 数据库。API 负责校验、授权、事务和查询；Worker 领取持久任务，执行 ZLM 调用、目录检查、状态采样和恢复对账。媒体文件由 ZLM 写入，Frigate 的正式录像保持关闭。

| 方案 | 收益 | 代价与选择 |
| --- | --- | --- |
| Go 模块 + 独立 API/Worker + PostgreSQL | 共享领域模型，耗时任务不阻塞请求，任务重启可恢复 | 需要明确期望/实际状态和任务租约；采用 |
| 单进程执行请求及全部后台任务 | 启动简单 | 请求重启与任务恢复耦合，不适合换源/归档的发展方向 |
| 多个业务微服务 | 独立扩展 | 当前单站点规模增加部署与跨服务一致性成本 |

实现基线：Go 1.27.1、标准库 `net/http`、`pgx/v5`、显式 SQL；PostgreSQL 17.11；前端直接引入 shadcn-admin，保留 React/TypeScript/Vite、TanStack Router/Query 和组件体系，使用 Node 24.21.0 构建。构建与运行镜像已在 [镜像版本基线](../../image-versions.md) 固定具体标签和 digest：应用运行基础为 Debian bookworm-20260918-slim，网关为 Nginx 1.30.5-alpine3.24，OpenList v4.2.6；ZLM、Frigate 0.17.2、Mosquitto 2.0.22 沿用 M0 构建。新镜像仅核对仓库元数据与 amd64 架构，正式链路尚未验证。导入时另固定上游 commit、包管理器版本、`go.sum` 和 `pnpm-lock.yaml`，发布构建固定额外系统包/仓库快照，不使用浮动 `latest`。

不增加 Redis、消息队列业务中间层或 ORM。PostgreSQL 任务表承担业务队列；Mosquitto 仅在智能检测模块启用时承担 Frigate 消息集成。API 重启不重建 ZLM。业务配置保存在数据库，`.env` 承担模块开关、端口、路径、镜像、内部连接与部署约束，摄像头配置不再以 `.env` 为正式来源。

### 2.1 模块化部署

正式部署提供两个 yes/no 开关，缺失默认 no、无效值拒绝：

```dotenv
ONE_NVR_FRIGATE_ENABLE=no
ONE_NVR_OPENLIST_ENABLE=no
```

基础看/录始终运行 gateway（含前端）、api、worker、postgres、zlm，共 5 个容器。智能检测开启增加 frigate/mqtt；云归档开启增加正式 Compose 内的自带 openlist，任务仍复用 Worker。两模块 no/no、yes/no、no/yes、yes/yes 分别为 5、7、6、8 个常驻容器，初始化/迁移容器执行结束后不计入。ONE_NVR_OPENLIST_ENABLE=yes 同时提供云归档模块和自带 OpenList；归档目标可选择自带服务，也可添加其他标准 WebDAV，不新增第三个必填开关。

deploy.sh 从明确 `.env` 路径解析统一配置，不执行 source/eval；使用 Compose profiles 和明确服务清单，核心 depends_on 不包含可选服务。关闭智能模块不检查模型/设备/对应镜像，不加载 GPU 覆盖；关闭归档不检查 OpenList 镜像/持久目录/服务或远端；开启后检查自带 OpenList 的实际服务状态，归档目标仍逐项验证。启动/拉取/健康报告均只覆盖启用模块。修改 env 后再执行 deploy.sh 生效；先前开启后来关闭的模块须显式停对应容器，单纯减少 profile 不能当作已停止，不执行全站 down 或删除卷。API/Worker 只构造启用的适配器/调度器，未启用连接参数可缺失，不能后台持续重连或报离线告警。

能力状态由后端统一生成，区分 disabled、not_configured、healthy、unavailable 和 not_available。enabled 是部署意图，available 是当前可用性；env=yes 不能将尚未交付的 M2.1/M3 能力冒充实现。前端配置/执行按钮依据状态置灰并显示“功能未启用”等具体原因；直接 API 调用同样检查，关闭返回 409 feature_disabled，未实现 501 feature_not_available，依赖异常 503。可选模块 disabled 不使核心 readiness 失败；已启用但宕机仍显示真实异常，不自动伪装禁用。

自带 OpenList 使用固定版本/digest 的独立镜像、云模块 profile 和独立持久目录（宿主 `ONE_NVR_DATA_DIR/openlist` 映射其配置数据目录）；初始化凭据只生成一次，不因重建容器重置。不向其默认开放原始录像根目录，Worker 通过受授权 WebDAV 上传，one-nvr 仍拥有原录像索引、保留和任务语义。网盘挂载由管理员通过受控原生管理入口配置，入口代理/认证与镜像实际参数在部署验收中验证。

`GET /capabilities` 的归档模块部署开关与 `operations/components` 的自带 OpenList 健康分开。后端通过私有服务名探测上游 HTTP 状态并返回最近成功/失败时间和脱敏原因，前端展示“未启用/启动中/正常/不可达或异常”；.env=yes、容器存在或端口可连都不等于服务与归档目标可用。无需让浏览器访问 Docker 网络或让 API 读取 Docker socket。

归档目标保存 builtin_openlist/custom_webdav 类型、地址/根目录、加密凭据和不可变配置版本，仍按 PRD 的目标模型管理。自带服务正常但未配置网盘时目标为未配置/不可写，不伪报可上传；自带服务故障仅阻断其目标，前端仍可添加/测试其他 WebDAV，正常其他目标的任务、回放与清理继续。云模块关闭才禁止全部云操作。新目标不自动替换默认目标/通道绑定，历史任务与副本继续引用原目标版本。

禁用保留配置/数据/卷；历史本地事件/抓拍查询不依赖 Frigate 在线，依赖上游的新检测/规则操作不可选。归档关闭先将任务安全暂停，再停止自带 OpenList 并保留其持久目录；暂停上传、云端读取和到期删除，已有仅云媒体明确显示功能未启用；不删除对象或放松既有待归档/保全约束。再次启用先对账恢复，云期限仍基于原结束时间。基础取流、录像、源可靠性与本地回放独立运行。

建议目录：

```text
apps/web/                         原始 shadcn-admin 经修改的前端
cmd/api/ cmd/worker/ cmd/admin/    业务入口、后台入口、初始化/迁移工具
internal/auth/                    账号、会话、权限、凭据加密
internal/channel/                 槽位、来源、草稿、切换、批量导入
internal/media/zlm/               固定上游版本的 API/Hook 契约适配
internal/storage/                 目录注册、探测、池状态
internal/recording/               M1 最小分片索引与受控交付
internal/jobs/                    租约、幂等、重试、对账
internal/operations/              组件状态、审计和状态流
migrations/                      版本化 SQL 与校验和
deploy/production/               正式 Compose 与初始化/部署工具
docs/api/                        OpenAPI 和前端类型生成说明
```

## 3. 身份、通道权限与凭据

使用服务器端会话。浏览器只保存随机会话标识的 `HttpOnly; SameSite=Lax; Path=/` Cookie，HTTPS 添加 Secure，明确选择的 HTTP 模式不设 Secure；协议只信任 gateway 覆写的受控信息，不由客户端头降级。数据库保存标识的 SHA-256 摘要。默认闲置 30 分钟、绝对 12 小时到期；登录轮换标识，登出、改密、禁用立即撤销。修改类请求要求同源 Origin 与会话 CSRF token；登录/初始化也校验同源并限制尝试频率。默认 HTTPS，部署者可通过 PUBLIC_URL 选择 HTTP；入口协议切换撤销原会话，HTTP 提示当前连接未加密。详见 [访问与证书设计](../../web-access-tls.md)。

用户密码使用 Argon2id，编码保存参数、随机 salt 和结果；初始参数 64 MiB、3 次迭代、并行度 1，使用受限并发避免耗尽内存。站点角色固定为管理员、操作员、查看者。每用户每通道独立授予 `live`、`playback`、`export`、`configure`；角色限定可授予的动作，查看者无配置/凭据访问，操作员无密码查看/配置导出。站点管理权限不隐式授予全部通道的视频权限。初始管理员明确获得全部初始化槽位的授权，新用户默认无通道授权；禁止禁用最后一个有效管理员。

摄像头用户名和密码使用 AES-256-GCM 加密；每次保存使用新 nonce，AAD 绑定站点、通道、源版本和字段，密文附 key ID。32 字节主密钥存于持久 secret 文件，缺失或不匹配时拒绝相关解密/发布，不随机生成替代旧密钥。普通响应只返回用户名展示摘要、密码保存状态和脱敏地址。

显式查看密码使用 POST，检查管理员角色及目标通道配置授权，写脱敏审计后返回 `Cache-Control: no-store`；成功审计不能持久化时不交付明文。批量配置导出同样授权，固定读取一致的源版本集合；任一必需凭据无法解密即整个导出失败，逐项返回错误。导出 JSON 直接包含解密凭据，不生成公开链接或服务端长期明文文件。

前端明文只放局部组件内存；隐藏、离页、退出及会话失效立即清除，不进入 Query 缓存、localStorage、URL 或日志。编辑密码用 `keep / replace / clear` 明确表示意图；掩码不是可提交密码。删除用户采用停用保留引用，审计不级联删除。

## 4. 数据模型与一致性

所有业务主键为服务端生成 UUID；时间用 PostgreSQL `timestamptz`，API 用 UTC RFC3339，界面、录像/抓拍目录和文件名使用站点本地时区，默认 Asia/Shanghai；Worker 显式进行时区转换并提供 IANA 时区数据。通道编号保存整数 1–32，展示生成 CH01；站点初始化一次选择 16 或 32，16 扩容到 32 只追加，禁止缩容/重编号/重新生成 ID。

站点时区在“系统设置 → 站点设置”使用可搜索列表选择，初始化同样提供该选择器，默认 `Asia/Shanghai`。显示中文名称、IANA 标识和当前时间预览；前端从后端获取支持列表，后端校验后持久化，不能只依赖浏览器的时区支持或静默回退。时区列表为公共参考数据，供初始化使用；站点配置按现有登录权限读取，管理员通过带 `If-Match` 的站点更新修改并审计，配置版本冲突返回 409，无效时区返回 422。

保存后刷新界面的默认格式/时间检索范围；新建录像/抓拍发布意图读取最新配置并冻结时区/offset，已冻结发布和云任务不重算路径，详情展示文件生成时区。修改只改变业务时区，不要求重启容器或 ZLM，也不改历史 UTC 时刻。M2/M2.1 的站点计划按新时区重算下一次调度，不重复已执行任务或延长保留期限。

| 表 | 核心字段与约束 |
| --- | --- |
| `sites` | 单站点 ID、名称、IANA 时区、槽位数、初始化时间、配置版本；仅一条站点记录 |
| `users` / `sessions` | 账号唯一、密码哈希、角色、禁用、授权版本；会话摘要唯一、闲置/绝对期限、撤销时间 |
| `tls_certificates` | 不可变版本 ID、公共 X.509 元数据、叶证书指纹、证书/私钥受限文件引用、导入者/时间；数据库不保存私钥明文 |
| `gateway_tls_state` | 单网关当前/上一有效版本、HTTP 下待用版本、配置版本与持久应用任务引用；同一网关互斥，预期版本冲突返回 409，生效依据实际握手指纹 |
| `channel_grants` | `(user_id,channel_id)` 唯一；动作布尔字段，所有资源入口同一授权策略 |
| `channels` | `(site_id,number)` 唯一；名称、分组、enabled、current_revision_id、storage_pool_id、config_version；ID/编号不可更新 |
| `source_identities` | channel_id、标签、身份置信度、可选核验设备标识；身份只在通道历史内管理 |
| `source_revisions` | channel_id、source_id、递增版本、结构地址、加密用户名/密码、TCP/UDP、创建者；已保存版本内容不可覆盖 |
| `source_tests` | revision_id、用途、首帧/轨道结果、配置摘要、完成/过期时间；结果与被测版本绑定 |
| `source_switch_jobs` | 旧/新版本、原池/期望录像、当前阶段、实际时间、错误、租约与 fencing token；每通道最多一个未终结切换 |
| `source_import_batches/items` | 操作者、有效期、格式、行号、目标、预期 config_version、加密草稿、身份意图、测试/切换结果；批内目标唯一 |
| `stream_sessions` | channel_id、source_revision_id、用途、generation、唯一 ZLM app/stream、开始/关闭时间及状态；保留已关闭映射 |
| `storage_pools/checks` | 名称、规范路径、业务标识、默认/启用、只读/离线/空间状态；检查按 API/Worker/ZLM 分开记录时间和结果 |
| `recording_policies` | 每通道连续/关闭、event_recording_enabled（默认 false）、池引用、策略版本；M1 不开放周计划/事件录像，预留开关，未实现能力不显示为可启用 |
| `recording_runs` | 通道/源版本/流会话/池、唯一原生工作目录、启动与终止状态；实际重启录制创建新 run，启动前持久绑定并写无凭据描述 |
| `recording_segments/locations` | 通道、源版本、流会话/run、池、原始/目标相对路径、命名规则版本/命名时区/UTC offset/原始开始时间、发布状态、实际 `[start,end)`、大小、编码/基础检查状态；run+原始相对路径唯一，本地池+正式路径唯一 |
| `recording_gaps` | 通道、切换任务、实际中断区间及原因；来源未知显式记录 |
| `play_sessions` | 用户/通道/源流会话、类型、到期、授权版本、实际连接引用、撤销状态 |
| `live_views` | 用户、名称、1/4/9/16 布局、通道/位置/码流；加载时重验当前授权 |
| `jobs` / `job_attempts` | 类型、对象、幂等键、状态、下次尝试、租约/尝试编号、脱敏结果；幂等键在动作范围内唯一 |
| `component_observations` / `audit_logs` | 组件与通道分项状态、观测/过期时间；操作者、动作、对象、结果、请求 ID、脱敏差异 |

媒体、源版本、池、任务与审计的外键采用保留引用的删除限制，不能从清空源级联删除历史。`channels.current_revision_id` 用包含 channel_id 的复合外键，禁止绑定其他通道版本。池路径跨记录的重复/嵌套检查在数据库事务级锁内执行；默认池只有一个的部分唯一索引。所有配置变更检查 `If-Match` 对应 config_version；过期返回 409，不执行外部操作。

API 在同一事务中写期望配置、任务和审计，事务提交后返回 202。Worker 用 `FOR UPDATE SKIP LOCKED` 领取任务，租约默认 30 秒、10 秒续期，失败退避 2/4/8/16/30 秒。每通道执行器持有独立 PostgreSQL session advisory lock，通过与租约对应的 fencing token 更新状态；旧执行器丢失数据库连接或租约后停止调用上游。上游调用最长 10 秒；接手执行器先查询既有流/录制，再执行下一步，不能仅凭任务超时重复启停。

长期拉流重试持续到禁用/更改期望配置，连续失败进入告警；一次切换的就绪窗口默认 30 秒，超时转回滚，回滚窗口 30 秒。任务结果保留可诊断原因和实际外部状态，不把 POST 成功当作上游生效。

## 5. 视频源、批量导入与换源

结构字段固定为 `channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path`，可选 `onvif_port`。`ip` 只接受 IPv4/IPv6 字面量，端口 1–65535，空 RTSP 端口默认 554；主路径以单个 `/` 开头，可保留 query，子路径可为空。拒绝完整 URL、`//host`、控制字符和无效百分号转义。认证信息逐组件编码，IPv6 自动加方括号，不重复编码路径/query 的合法转义。

部署层明确允许的摄像头 CIDR，排除内部管理服务与特殊保留目标；不因地址是私网就绕过允许范围检查。M0 完整 URL 导入仅作显式迁移工具：解析后必须无损表达在八列模型中，双 `rtsp://`、主/子端口或凭据不同等逐项拒绝并说明，不修改正式字段语义。

源版本草稿保存后不可直接成为 current。连接测试并发上限 2，创建独立临时 ZLM 流，不停止旧源；通过受限首帧探测读取实际视频帧，并记录轨道、编码、尺寸、帧率与错误。首帧探测工具仅访问内部临时流、固定超时/输出上限。测试结果 5 分钟内可用于对应版本发布，修改配置必须重测；失败草稿可保留，不能自动替换正常源。

修改连接配置沿用 source_id；更换摄像头默认新建，也可明确选择该通道历史 source_id。Source ID 不根据 IP 或密码哈希自动合并。命名/分组修改不重建流；清空源关闭当前会话并保留槽位、策略、授权和历史。

切换阶段：`queued → preflight → closing_old → starting_new → confirming → succeeded`；切换开始后的失败进入 `rolling_back → rolled_back / rollback_failed`，前置失败为 `failed`。关闭旧录制时完成尾片，保留实际边界和旧会话映射；新会话使用唯一 app/stream 代次，不能复用旧物理流标识。主流首帧与应录时的录制状态确认后才更新 current；子流失败单独显示降级，不伪装双流正常。回滚使用旧版本新建运行代次，遵循原通道策略。Worker 重启依据持久阶段和 ZLM 实际状态对账，同通道互斥，不重建其他通道。

CSV 支持 UTF-8 BOM、标准引号/逗号/换行；文件最多 1 MiB、32 条数据记录，未知表头/格式版本明确报错。预览不应用；保存加密草稿和脱敏差异，不持久化原始明文文件。JSON 缺失密码表示保留，空字符串表示清空；CSV 标准八列表头中的空密码表示明确空密码，若要保留已有密码，须在预览选择 keep；掩码拒绝。未配置目标的 keep 无效，须提供密码或选择无密码。

预览默认 30 分钟有效；提交需指定目标映射、身份意图、是否导入名称和选中行。重复目标拒绝、重复地址提示核对；跨站点文件不得注入原站点 UUID。同设备完全相同配置跳过，不重连。批任务逐行检查最新权限和预期版本，冲突失败不覆盖；每行独立测试/切换/回滚，成功行不会因其他行失败撤销。取消只停止未执行项，执行中的换源必须完成或回滚到可诊断终态。凭据导出 JSON 带 `format_version=1`、site_id、导出时间和追溯信息，可原样进入该预览。

## 6. 目录存储池与最小录像

服务使用一致容器路径 `/storage`。池只能登记已经存在且位于允许根内的绝对目录；使用规范路径和受根目录约束的文件访问，拒绝符号链接逃逸、重复和嵌套。界面不执行 mount、不配置 NAS/RAID，不创建消失的池根目录。

首次注册分配 pool_id，在池根 `.one-nvr.json` 写入站点/池标识，不把池 ID 作为目录层级；测试文件仅在已登记 `.work/probes/` 创建/删除。首次注册遇到标识不匹配，或无标识但已有 recordings/snapshots/exports/.work/.meta 保留目录，拒绝自动接管，提示专用目录或恢复身份核验，不清空既有内容。API 和 Worker 都验证标识、读写/删除及容量；ZLM 通过临时录制到同一路径验证实际写入，测试媒体随探测任务清理。三个检查均成功才允许池写入；ZLM 无可用测试源时显示“待 ZLM 写入验证”，可登记但不能作为已验证写入池。首次配置源可以同时完成源测试与池写入验证，避免初始化依赖循环。

后续探测标识缺失/不匹配、目录不可访问不自动补建。池状态和上次检查时间分开，检查过期为 unknown；M1 默认 10 秒探测、30 秒过期。多个池共享文件系统时，通过同一运行环境的文件系统标识及容量证据合并展示可用量，不把路径当作独立容量。单独池的已索引业务占用仍独立统计。

通道首次配置绑定默认池；更换源不改池。更换池完成当前分片后启动新目录录制，保留旧分片原池位置。已有媒体/任务引用的池只能停用，路径不能改或删除；停用阻断新写入，仍可读取的历史媒体继续授权播放。

M1 连续录像目标分片长度 60 秒，实际时间以完成回调和媒体检查为准。正式生成与路径采用 [录像文件规则](../../recording-file-layout.md) v3：ZLM 在同池 `.work/zlm/<recording_run_id>/` 完成原生文件；Worker 先持久化发布意图，再不覆盖地原子移动到 `recordings/<channel_no>/<本地日期>/<channel_no>_<YYYYMMDD_HHMMSS>_<recording_id>.mp4`，提交 ready 后方可交付。原始来源键为 run+ZLM 相对路径，录像 ID 幂等生成；首次建立发布意图冻结命名时区、原始开始时刻对应的 UTC offset 和目标路径；文件名记录本地原始录制开始时间，校验修订/站点时区变化/重试不改已有路径，云端沿用本地名称。每次实际重新启动录制使用新 run，重启 Worker 不改变仍在运行的 run。移动与索引崩溃窗口按发布意图恢复，数据库不可用时保留待发布原文件，不移动写入中的文件，不转码。Worker 的内部 Hook 接收端验证服务身份，先持久化 PostgreSQL inbox 再应答，数据库不可用时先 fsync 到持久 spool，均不可写则返回失败；数据库 inbox 表以回调来源和幂等键唯一。回调通过 app/stream 查原始会话，迟到尾片不会归属新源。文件必须位于该会话绑定池和录制目录内，按池保存相对路径，核验存在/大小及基础轨道；正在写入、未知或丢失不显示为可播放。基础探测不代表逐帧完整性验收。

数据库/API 故障时 ZLM 已有录制继续。Worker 持久 spool 保存已收到的回调；恢复扫描只遍历已登记 run 工作目录、发布意图及标准业务目录，利用持久会话/run 映射补回丢失索引。来源描述保存于池内 `.meta/recording-runs/`，不含 RTSP 凭据；无法确定来源的文件隔离诊断，不猜测当前源。重复 Hook 原路径已移动时先查来源键，不误报丢失；待发布媒体不参与按缓存年龄清理。不会承诺数据库、Worker 同时停止期间所有 Hook 都可靠送达。

安全空间低于 `max(10 GiB, 该文件系统承载通道近期总码率对应的 10 分钟写入量)` 且无法释放时阻断相关池录制；恢复到两倍安全线后恢复。M1 无清理器，明确显示容量处理入口；播放器引用不会被后台删除。M2 接管保留策略时保持同一分片/位置模型，不迁移媒体归属。

## 7. 实时预览与媒体授权

前端请求创建播放会话，API 检查用户、通道动作及当前流会话后发放 5 分钟票据。票据绑定用户会话、通道、运行代次和用途，数据库只保存摘要；WebRTC SDP 只通过同源受控接口传递。浏览器不获得 ZLM 管理 secret、摄像头 URL/密码或内部管理地址。

WebRTC 媒体经 ZLM RTC 端口协商，信令入口鉴权不替代媒体会话授权。适配器校验 `on_play` 的票据/流代次，将 ZLM 实际连接与业务会话关联；每 10 秒复核授权，撤销时调用固定版本的会话关闭接口，目标 60 秒内断开。续期、重新协商也重新授权。这个能力必须通过实际固定镜像验证后才能标记 M1 权限验收通过；若连接无法可靠关闭，该发布不满足退出条件。

API 不可用时拒绝新媒体会话。API 与 Worker 均不可用时，前端倒计时/续期失败后在票据到期前停止自身 PeerConnection；此客户端行为不能替代服务端撤销保证，组件全故障下的直接 RTC 会话上界须在验收报告单独记录。不得为了展示成功关闭上游播放 Hook 或公开流/录像目录。

最小历史播放通过 `/recordings/:id/content` 受保护 Range 交付；每次读取检查会话/通道，不根据当前源在线或 enabled 隐藏历史。每段开始建立媒体租约，长连接每 10 秒复核授权。网关内部文件映射不对外开放，路径只由已核验数据库记录解析。

实时页支持 1/4/9/16，32 槽位分页/切换视图；默认子流，无子流明确提示并使用主流，放大切主流。界面状态为连接中、播放中、重连中、离线、无权限、编码不支持、服务不可用；退避 1/2/4/8/15 秒，离开页面/隐藏标签/移出视图释放连接。H.265 根据实际浏览器能力协商和播放，不自动承诺转码；录像正常和浏览器无法播放分别显示。

## 8. API 契约与前端入口

统一 `/api/v1`，普通 JSON 响应为 `{data, request_id}`，列表增加 `{page:{next_cursor}}`，错误为 `{error:{code,message,fields,retryable},request_id}`。ID 均用 UUID；异步动作返回 202 和 job_id。新增/执行请求支持 `Idempotency-Key`，同键同操作者/动作/脱敏规范参数返回同任务，不同参数返回 409；参数摘要对凭据使用受服务密钥保护的 HMAC，不使用可离线猜测密码的裸 hash。

未登录 401、已登录无动作权限 403、资源不在授权范围 404、语法 400、字段规则 422、版本/幂等冲突 409、限流 429、依赖不可用 503。敏感与认证响应 `no-store`。GET 不产生配置副作用，列表服务端分页默认 50/最大 100；时间检索要求明确 `[start,end)`，M1 只接受单通道且窗口不超过 31 天。

| 接口（均在 `/api/v1` 下） | 请求/结果及职责 |
| --- | --- |
| `GET /capabilities` | 当前版本/部署开关/配置/实际运行状态，返回模块 enabled、available、state、reason；自带 OpenList 组件健康单独呈现，故障不全局阻断正常其他 WebDAV |
| `GET /setup/status`、`POST /setup` | 仅返回是否初始化；POST 校验一次性部署令牌并事务创建站点、管理员、16/32 槽位和初始授权，成功失效令牌 |
| `POST /auth/login`、`POST /auth/logout`、`GET /auth/me` | 本地会话、CSRF token、角色/当前动作范围；无示例默认密码 |
| `POST /auth/change-password` | 旧密码验证，更新并撤销全部旧会话，重新登录 |
| `GET/POST /users`、`PATCH /users/:id`、`PUT /users/:id/channel-grants` | 管理员操作；更新授权版本并触发媒体撤销，保护最后一个管理员 |
| `GET /site/timezones` | 公共支持时区列表，仅返回中文标签/IANA 标识等参考数据，供初始化与设置选择；不暴露站点配置 |
| `GET /settings/tls`、`POST /settings/tls/certificates`、`POST /settings/tls/certificates/:id/apply`、`POST /settings/tls/rollback` | 管理员读取证书公共元数据/上传 PEM 预检/提交应用或回滚任务；不返回私钥，证书动作审计；应用状态由持久任务和实际网关指纹确认 |
| `GET/PATCH /site`、`POST /site/channel-expansion` | 名称/可选择的 IANA 时区；管理员修改，If-Match/审计/校验；仅 16→32 追加，已有通道和授权不改，提交时明确授予执行管理员新槽位授权，其他用户默认无新增授权 |
| `GET /channels`、`GET/PATCH /channels/:id` | 槽位列表、配置/分项状态；名称/分组/启用，带版本条件 |
| `GET/POST /channels/:id/source-revisions` | 查询脱敏历史、创建结构化不可变草稿；指定 modify/replace/history 身份意图 |
| `POST /channels/:id/source-revisions/:revision/test` | 返回持久测试任务、真实首帧/轨道结果 |
| `POST /channels/:id/source/apply`、`POST /channels/:id/source/clear` | 版本检查、测试前置、串行切换/清空，返回 switch_job_id |
| `POST /channels/:id/source/credentials/reveal`、`POST /channels/source-config-export` | 明确的管理员/通道配置授权；指定当前源版本或通道集合，审计、no-store、明文只在成功响应 |
| `POST /channels/source-imports/preview` | multipart CSV/JSON；加密保存草稿并返回脱敏差异/行错误/批次有效期 |
| `POST /channels/source-imports`、`GET /channels/source-imports/:id` | 预览 ID、行选择/映射/身份意图/凭据意图；逐行结果 |
| `POST /channels/source-imports/:id/test /retry /cancel` | 批量测试、只重试选中失败项、取消未执行项；复验权限与目标版本 |
| `GET/POST /storage-pools`、`PATCH/DELETE /storage-pools/:id` | 已挂载目录注册；设置默认、停用，删除仅无引用空池 |
| `POST /storage-pools/:id/test`、`PUT /channels/:id/storage-pool` | 分服务探测任务；通道池切换任务，带版本条件 |
| `POST /play-sessions`、`POST /play-sessions/:id/renew`、`DELETE /play-sessions/:id` | 授权 live 主/子流或指定 recording_id；到期/撤销/关闭 |
| `POST /play-sessions/:id/webrtc` | 受控 SDP offer/answer，拒绝任意 app/stream/secret 参数 |
| `GET /recordings`、`GET /recordings/:id/content` | 按通道/时间查询已完成单段；受授权 Range，未知/缺失 409，已确定删除 410 |
| `GET/POST /live-views`、`PUT/DELETE /live-views/:id` | 用户自己的布局，加载和保存复验当前通道权限 |
| `GET /jobs/:id`、`GET /operations/components`、`GET /audit-logs` | 本人或管理员任务；管理员组件诊断及审计，普通用户只见自身授权通道状态 |
| `GET /status/stream` | SSE，当前用户可见资源状态；支持心跳/断线后重拉，不推送凭据或内部地址 |

ZLM Hook 使用独立内部端口和服务身份，不属于匿名公网 `/api/v1` 入口；健康接口区分进程存活与依赖就绪。OpenAPI 文件在实施计划中先于前后端实现落地并生成类型，避免前后端各自定义契约。

M1 一级入口为：总览、实时预览、录像、通道管理、存储池、用户与权限、系统设置、运维与审计。录像页面在 M1 仅提供时间检索和单段播放。事件中心/智能规则在 M3 可用后加入，最终保持 PRD 的 10 个入口；不提供能误导为已实现的空菜单。用户角色/通道授权同时决定页面内容及后端请求范围。

保留上游 MIT 许可证、版权与导入 commit。替换演示用户、假统计、模拟业务数据、Clerk 演示认证和模板入口，连接 one-nvr 会话；保留布局/暗亮主题/表单/表格交互。`auth-store` 不保存访问 token 或密码，登录态从 `/auth/me` 恢复。中文文案、站点时区、默认暗色，所有页面有加载、空、错误、无权限状态。

## 9. 部署、迁移与 M0 关系

正式 Compose 使用独立项目名、状态目录和 PostgreSQL 卷，不覆盖 `deploy/m0`、已有 `.env`、SQLite、密钥或录像。M0 与正式版不得同时对同一摄像头启动额外正式录制；同主机切换先停止实验流，再接管对应通道，维护中断记录在案。

入口协议直接从 ONE_NVR_PUBLIC_URL 选择 http/https，默认 HTTPS；ONE_NVR_HTTP_PORT 默认 8080，ONE_NVR_HTTPS_PORT 默认 443，仅绑定选中的入口，URL 有效端口须匹配。HTTP 不依赖 TLS 证书；HTTPS 初始化一次生成自签或使用导入版本。系统设置提供证书信息/上传/预检/应用/回滚，公共元数据保存在数据库，私钥版本文件存 DATA_DIR/tls。由 gateway 容器内受限控制进程执行配置预检、reload 和新 TLS 连接指纹核验，Worker 通过受控共享任务目录交付请求；不增加容器、不挂 Docker socket，失败/重启按持久任务与上一有效版本对账，不影响 ZLM。具体文件、限额、校验与契约见 [访问与证书设计](../../web-access-tls.md)。

正式部署参数包含 `ONE_NVR_RTC_PORT`（整数 1–65535，默认 8000），与所选 HTTP/HTTPS Web 入口端口独立。部署工具将它同时写入 ZLM `rtc.port` / `rtc.tcpPort`、对外协商端口和同号 Docker UDP/TCP 映射；因两者均使用 TCP，拒绝与当前活动 Web 入口端口相同的配置。改端口须同步生成配置和映射；NAT 外部端口保持一致，浏览器连通性实际验证，媒体网络变更按 ZLM 维护流程记录中断。

`ONE_NVR_MEDIA_HOST` 为可选媒体 IP 覆盖项。PUBLIC_URL 主机为明确 IPv4/IPv6 的直连部署时默认沿用其 IP，正式 .env 最小示例仍显式列出 RTC 端口；域名/代理/NAT 拓扑无法确定可达媒体 IP 时要求明确填写，不以 Docker 网卡地址冒充。页面/API/录像/信令共用所选 HTTP/HTTPS 入口，WebRTC 媒体使用独立 RTC 端口；M0 保留 M0_RTC_PORT，不复用正式变量名。参数及完整示例见 PRD 第 14.4 节，当前尚未实现正式启动模板。

全部工具提供 Docker 执行方式，宿主无需 Go、Node、Python。Go 多阶段镜像构建 API/Worker/admin，前端独立构建后由 gateway 交付；已有 `python:3.12-slim` 继续用于 M0，不强制正式 Go 服务依赖 Python。初始化工具生成一次性 setup token、数据库/上游/加密 secret 文件，重复执行不覆盖；`.env` 非敏感配置和源镜像准备/软件包镜像选择有清晰离线说明。

基础部署默认两模块关闭，部署工具只要求核心镜像/目录/密钥；智能模块开启再生成 MQTT/Frigate 必要配置并检查硬件档案，归档开启启动自带 OpenList 并探测实际状态；尚未配置网盘或外部目标时允许进入面板完成配置。核心模块初始化或健康失败必须返回失败；已启用可选模块失败须返回明确的部分失败报告，保留已经就绪的核心服务，不以全成功掩盖异常。正式模块开关在 M1-A 落地，实际云归档/智能能力按原里程碑交付，M0 实验脚本不冒充正式部署实现。

迁移由独立 admin 命令串行执行，数据库锁、迁移编号与校验和记录；失败停止新 API/Worker 就绪，不删除旧数据。初始建表是新增环境，后续用向前兼容迁移；涉及不可逆数据变化时先备份，不自动执行破坏性 down migration。发布报告记录具体 schema、应用与上游镜像版本。

M0 数据不自动转成正式记录。摄像头配置可通过受校验的显式导入迁移；旧录像作为后续只读导入能力另做设计，未迁移时保留 M0 查询入口，不把 SQLite 直接当作正式 PostgreSQL schema。M1 换源历史验收使用正式版产生的录像。

Frigate CPU/Intel 档案保留 M0 契约和独立数据目录；正式业务智能配置发布属于 M3。M1 已拉起的 Frigate 故障不阻塞 ZLM 录制；没有检测配置时显示“未配置”，不会借用 M0 假冒正式智能接入已完成。N5105 为后续主要检测样本，AMD 检测继续不作为当前门槛。

## 10. 验收与后续实施切分

按依赖顺序拆成三个可部署增量，分别形成实施计划；共同遵守本文 ID、授权、数据和任务契约：

1. **M1-A 基础面板**：引入真实上游前端、Go API/Worker/admin、PostgreSQL/迁移、Docker 工具链、初始化/账号授权、固定槽位、模块开关/能力接口、目录池登记与分项验证、HTTP/HTTPS 入口与证书管理/网关控制。真实数据替代所有演示内容。
2. **M1-B 通道接入**：加密凭据、结构化草稿/测试、源切换与回滚、批量导入/明文配置导出、ZLM 连续录制与最小索引，完成源归属、池改绑和重启对账。
3. **M1-C 实时与验收**：受授权 WebRTC、1/4/9/16 分屏、视图保存、分项状态、历史单段 Range 回放、SSE、权限撤销、部署说明与真机验收。

自动验证包括：

- PostgreSQL 迁移/重复初始化/并发槽位和版本冲突；真实数据库验证事务、唯一约束、租约和切换互斥。
- 地址生成包含 IPv6、特殊凭据、路径 query、已有转义、重复协议；导入密码缺失/空值/keep、BOM/逗号、重复目标和跨站点映射。
- 默认响应不泄露凭据，直接接口跨通道访问被拒，撤销会话与最后管理员保护；认证/明文接口审计失败路径。
- 正常切换、测试失败保留旧源、重启接管、回滚失败可见；旧会话迟到回调仍归旧源，清空源保留历史，重命名不重连。
- 固定 ZLM 原生工作路径/完成通知契约；本地日期（含北京时间凌晨）/同秒分片/夏令时重复时刻/新 run 不覆盖、未完成不发布、移动前后崩溃及重复 Hook 可恢复、目标冲突/跨文件系统/权限失败保留原文件，移动后只能走授权的标准媒体接口。
- 目录穿越/符号链接/嵌套/共享容量/业务标识异常，API 可写而 ZLM 不可写，低空间阻断只影响相关池。
- 浏览器登录恢复、明文离页清除、真实查询/空/错误状态、页面隐藏释放连接、旧响应不覆盖新选中通道。
- 初始化与设置时区列表选择、权限/无效标识/版本冲突及保存后恢复；上海/东京转换改变显示和新文件日期时间，不改历史 UTC/路径或冻结任务，不重启录制；文件详情保留命名时区。
- RTC 默认/自定义端口在 ZLM/协商/Compose 中一致；非法端口、活动 Web 入口冲突、媒体地址无法推导有明确拒绝，IP 直连默认与显式覆盖可用，实际浏览器验证媒体连通；不以 Web 入口正常代替 RTC 通过。
- HTTP/HTTPS 登录/Cookie/CSRF、端口选择、HTTP 远端接收浏览器兼容和安全上下文能力提示；证书 key/链/SAN/有效期/文件限额、应用故障/回滚/崩溃恢复，新 TLS 实际指纹和两路录像连续生成核验，私钥与控制目录不公开。
- 模块 no/no、yes/no、no/yes、yes/yes 的服务选择/健康检查和未实现状态；模块缺失镜像/设备不影响基础部署，能力接口与页面/直接 API 一致。开→关再次 deploy 不遗留可选进程、不重建核心或删卷；模拟已启用服务宕机必须呈现异常，云任务暂停/恢复保持保全约束和原到期。自带 OpenList 容器重建保留配置，服务状态与各目标能力分别探测；故障自带目标与健康外部 WebDAV 并存时，后者仍可添加/测试/运行，不被全局置灰。
- 后续事件阶段验收：无常规录像且事件录像关闭时仍检测/保存截图/时间记录，无事件视频或为事件服务的预录分片；已有常规录像不受开关影响，事件/换源/重启不复位开关，必要检测缓冲单独验证。

真机验证使用现有每机两路：主/子流多画面、正式分片播放、源配置切换/故障回滚、池切换及旧分片读取、权限直接请求/正在播放撤销、API/Worker 重启对账、Frigate 故障隔离。长时、损坏媒体、完整云归档和 16/32 压测随对应阶段继续，报告区分未测、失败与通过。

## 11. 资料与评审依据

- [shadcn-admin 仓库与 MIT 许可](https://github.com/satnaing/shadcn-admin)：直接复用上游框架/组件，导入时固定 commit。
- [上游 package.json](https://github.com/satnaing/shadcn-admin/blob/main/package.json)、[认证 store](https://github.com/satnaing/shadcn-admin/blob/main/src/stores/auth-store.ts)：需要替换的演示依赖与登录持久方式，不能默认视为 one-nvr 生产认证。
- [ZLM REST API](https://docs.zlmediakit.com/guide/media_server/restful_api.html)、[Web Hook](https://docs.zlmediakit.com/guide/media_server/web_hook_api.html)：集成原语；实际固定镜像契约测试是放行依据。
- [Go 发行与支持规则](https://go.dev/doc/devel/release)、[PostgreSQL 支持规则](https://www.postgresql.org/support/versioning/)：具体镜像/补丁/digest 已见镜像版本基线，正式兼容性仍须验收。
- [Nginx reload](https://nginx.org/en/docs/control.html)、[HTTPS 证书链](https://nginx.org/en/docs/http/configuring_https_servers.html)：自带网关应用原语；实际指纹与录像连续性需实测。
- [Docker Compose profiles](https://docs.docker.com/compose/how-tos/profiles/)：服务选择原语；显式指定服务可自动启用其 profile，关闭模块须由部署脚本和业务能力检查共同约束。
- [OpenList Docker 部署](https://doc.oplist.org/guide/installation/docker)：固定镜像、运行用户/目录权限与独立数据持久化按实际版本验证。

已自检：M1/M2/M3 边界明确；通道/来源/运行代次分别建模；录像只由 ZLM 生成；目录与权限不依赖底层存储类型；首次池/源探测可闭环；密码默认脱敏与显式明文操作分开；故障与未经验证的能力未标为通过。待用户评审后进入 M1-A 实施计划。
