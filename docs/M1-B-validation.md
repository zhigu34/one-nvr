# M1-B 验证记录

更新：2026-10-06。开发分支 `codex/m1b-channel-recording`；M1-B 已通过隔离自动验收；真机部署和16–32路容量未验证。

## 已验证

- 真实隔离 PostgreSQL 17.11：基础数据升级保留；不可变源版本、通道归属、录像来源及命名约束。
- 草稿不启用源；修改保留源身份并重新加密，换机新建身份；可选幂等键防止重复创建。
- 凭据绑定站点/通道/版本/字段，普通响应不返回密文或完整凭据；查看/导出要求管理员和对应通道 configure 权限。
- 查看/导出审计失败不返回明文；导出保留标记未配置的空通道，任一需解密的源失败则整体失败。
- 全量 Go/数据库测试、Go vet、API 类型生成、前端类型检查与生产构建通过。
- ZLM 管理适配器：POST form 携带凭据，不跟随 HTTP 重定向，10 秒上限，错误与过大/损坏响应不泄露上游内容。
- 探测子进程：仅固定 ZLM 内部会话地址，10 秒上限，stdout/stderr 合计最多 1 MiB；本机 FFmpeg 生成的虚构 MP4 经实际 ffprobe 校验。

## 真实媒体 CI 已通过

`media-contract` CI 使用正式固定 digest ZLM、同 digest 摄像头模拟器、四路虚构 H.264 主/子流。一次性测试镜像不是新增常驻服务。校验真实首帧、隐式录像关闭、无人观看仍取流、60 秒片和停止尾片、原生完成 Hook、实际文件路径/大小/结构，以及 RTSP 重定向是否触及禁止的内部地址。

2026-10-05：开发分支 CI run `37227752665` 的 `media-contract` job 成功；此前 DNS、RTSP 超时参数与隔离测试清理权限问题均经实际失败定位后修复。重定向检查先确认允许的源被访问，再确认禁止目标未收到连接。

Bookworm `20260919T000000Z` 签名快照中的媒体工具及依赖候选包版本/SHA256 记录在 `deploy/production/media-packages.lock`；构建逐个核验下载包，不关闭 TLS 或仓库签名验证。基础运行镜像中已存在的软件仍由基础 digest 固定。

HTTP 管理客户端禁止跳转不能证明 ZLM 原生 RTSP 客户端禁止跳转。若实测失败，源启用能力保持未开放，必须补足连接约束后才能通过 M1-B 验收。

## 早期候选记录（后续结果见最终验收）

持久完成回调已通过本机数据库中断/磁盘缓冲/旧 run 尾片/私有播放授权测试；真实存储池写入验证也已通过固定镜像 CI（run `37229398777`），包括生产生成 Hook 配置、映射探测鉴权、匿名探测拒绝、持久 run/inbox、实际 MP4 结构及清理。标准录像发布/崩溃恢复已通过本机实际 MP4/数据库回归，也已通过固定 ZLM `--publish` 容器 CI（run `37232884131`）；源切换/回滚已通过固定镜像 CI；录像调度、批量导入已通过后续固定镜像验证；正式通道配置页面和联合故障验收当前仍待 CI。实时播放和录像内容读取属于 M1-C。未进行 16–32 路负载验证，不宣称该规模的实际性能已达标。


本机发布验证覆盖同池同 inode 移动、不覆盖冲突及恢复、移动后提交前中断、冻结时区路径、重复 Hook、租约被替换后的提交拒绝、失效状态提交拒绝、损坏媒体保留诊断、工具不可用保持重试、登记回执丢 Hook 恢复、扫描预算跨批次推进、无可靠绝对时间保留 provisional，以及实际 ready local 占用统计。`GET /api/v1/recordings` 只提供授权单通道的时间交集元数据；内容读取保持 501，属于 M1-C。

CI run `37229398777` 的生产重复部署测试发现暂存目录未登记。已补充真实目录白名单校验并保留已确认回调；未知目录、文件和符号链接仍拒绝。后续 run `37232884131` 全部10个 job 通过，包括真实媒体发布/探测、正式核心重复部署、网关证书、浏览器及固定镜像验证；先前失败 run 仍保留记录。

## 源生命周期回归（真实容器验收已通过）

源测试最多占两个数据库全局名额，保留旧源/旧录像；主流首帧成功、子流失败返回降级结果，证据有效五分钟。撤权或排队时证据过期会在任务开始前拒绝，临时流在有限重试耗尽后仍保留清理意图。`SourceChange.test_id` 使未完成或失败的测试也能直接查询结果。

正式切换持久保存阶段、30秒新源/回滚期限、独立物理会话和录像 run。已验证切换/清除 API、旧尾片来源不变、失败源以新 generation/run 回滚、回滚失败呈现 unavailable、丢失录制返回结果后复用同一登记 run，以及租约/专用数据库连接失效后禁止继续提交。首次 none 必须显式选择，不要求存储池，不隐式录制，事件录像开关保留。

流缺失不能由管理请求异常推断。适配器只接受成功且明确的 `isMediaOnline.online=false`；[ZLM 官方实现](https://github.com/ZLMediaKit/ZLMediaKit/blob/master/server/WebApi.cpp)支持此接口语义，固定 digest 的真实响应由新增 `test-media.sh --switch` 验收。录像容量和周期健康对账已在 Task7 交付；尚不宣称 M1-B 已完成。

源生命周期固定镜像 CI 已通过：run `37263397662` 全部10个 job 成功。`--switch` 使用生产源测试/切换执行器、实际摄像头模拟流断开、真实连续录制和旧尾片发布，验证降级与失败源回滚；本机 Task6 最终 targeted race135.765s 通过。Task7 后续验收记录见下文。


## 普通录像与持续状态（Task7，已验收）

已完成本机定向回归：停录保留取流/历史/事件开关、改池保持旧位置、容量未知阻止新 run、同文件系统去重、10GiB/600秒容量线与溢出饱和、两倍线及两次间隔十秒恢复确认、失效状态显示 unknown、冻结帧停止录制并记录区间、最近完成片缺失记录 unknown 区间、断流恢复新 session/run、数据库不可用保留上游录制、被引用空池返回409。配置接口提供通道状态、普通录像 none/continuous 及改池；事件录像仍属 M3，实时/录像内容读取仍属 M1-C。

Worker 每十秒对账，最多四个并发通道检查，所有已配置或待清理通道进入有界 FIFO 队列；实际仍录制的上游复用已登记 run。停止池写入后保留取流与原位置。读写证据过期显示 unknown；真实根/marker/写失败才触发池异常停录。源首帧证据五分钟有效，但容量码率要求三十秒内两次有效且间隔十秒；延后执行的连续录制切换若码率过期，会保留原 run 并提示 bitrate_unknown，需重新测试。

新增固定镜像 `test-media.sh --recovery` 检查停录/恢复、真实代理缺失后新 generation/run、移走并恢复池 marker、两次实际健康确认、改池及旧位置保留。本机全量候选回归已通过（integration725.046s），后续容量/无变化策略与临时试录清理单独回归通过；该阶段的后续成功证据为 run `37279488117`；不能单独视为完整 M1-B 验收。

空闲池自动试录只用于已启用 continuous 的恢复需求；所有常规录像 none 时不自动试录。临时探测拥有独立池归属与专用数据库锁，重启后只清理已登记的探测会话，不停止普通录像。源测试不调用试录路径。

## 批量导入与前端（2026-10-05 更新）

Task8 固定镜像 CI run `37277187619` 的独立 `--import` 阶段已通过：真实三行批任务验证成功换源、不可读地址失败、版本冲突失败、成功项不重放，并保留原录像策略、存储池和历史。最终本机 Import/RoundTrip race 回归48.630s通过。当时 Task7 的独立重连恢复仍未通过，后续成功证据见下文；整阶段尚未验收。

正式通道配置页面已经接入真实草稿、测试/应用请求、历史修订分页、密码临时显示、录像开关、存储池绑定、批量预览/逐行选择/确认/取消/失败重试和本地 JSON 导出。导入预览提供密码处理枚举，不回传密码；使用明确版本再次确认后执行。录像索引按授权通道和时间范围查询，显示站点时区；内容读取保持 M1-C 未实现。

隔离本机 Go API/PostgreSQL/Chromium验证：草稿不生效、明文隐藏/切换通道清除、实际响应延迟后不能覆盖新通道、无权限配置拒绝、CSV预览/空行跳过、本地Blob导出后实际JSON回导。四项真实浏览器测试通过；完整前端16 suites87 tests通过，类型/ESLint/生产构建通过。仍需正式镜像浏览器验收及真实取流下的前端换源/回滚/不录制流程，不能宣称Task9或整个M1-B已完成。

Task7 最新真实媒体验收已通过：run `37279488117` 全部10个job成功，固定镜像 `--contract/--probe/--publish/--switch/--recovery/--import` 均通过。重连使用真实缺失确认、有限尝试及阶段诊断；管理请求失败不会当作流消失。此前失败run保留，最新成功不等同于长期稳定性或16–32路负载测试。Task7最终本机 task-done 回归通过（internal/recording 1.455s，integration 268.930s），Task9正式镜像和Task10联合验收仍待完成。

Task9 候选 run `37281050794`：后端、前端、真实媒体全部阶段及另外六项 job 成功；浏览器阶段4项在登录失败，根因是前一阶段真实改密后新增阶段仍用初始密码。修正阶段密码传递后，本机先执行真实改密，再运行新增4项，全部通过（5.4s）。正式镜像重跑和新真实媒体浏览器联合用例仍待CI。新用例本机缺少媒体fixture明确失败，不能跳过作为通过；覆盖32槽位、两组主/子模拟样本、1个活跃通道，不代表32路并发能力。

run `37282813189` 最新候选8/11 job成功，浏览器源测试登录限流、媒体恢复验收预算和真实媒体表单标签/fixture地址3项失败，均保留失败证据。实际登录突发10次200、第11次429已复现；阶段间只重启隔离API。精确标签回归RED4/5后增加稳定aria标签；fixture明确依赖Worker地址。恢复75s仅修正两次15s有界操作及10s间隔无法装入30s的验收预算，native race46.297s通过；不会放宽生产恢复期限或把未知当作缺流。正式Docker重新验收尚未通过，不宣称M1-B完成。

## 当前候选（2026-10-05）

上一候选 run `37285054372` 9/11 job 成功，实际 `contract/probe/publish/switch/recovery/import` 全部通过；两个浏览器 job 在保存草稿处失败。已用实际 Chromium 复现普通 HTTP 的 `crypto.randomUUID` 不存在，导致请求未发出；改用 HTTP 可用的 `crypto.getRandomValues` 生成 UUIDv4 请求键。回归先失败后通过；完整前端 16 suites / 88 tests、类型检查、lint 和生产构建通过。该修复不降低生产登录限制，也不要求系统只能使用 HTTPS。

清空摄像头后当前修订为空，但通道仍有成功应用的历史，不能再次提交首次录制选择。状态接口增加非秘密的 `requires_initial_recording_mode`，由后端按首次应用规则计算。真实 PostgreSQL 回归先失败后通过，SourceStatus / FirstApply / SwitchCommitAndClear / Clear race 75.932 秒通过；真实媒体浏览器用例增加清空后重新配置、策略和历史不变的验证。

当前 native `5dad660` / remote `304efaec` 代码树完全相同（`7c1699cf52d072da5a6f015b0cb1ebe5a8f26161`），CI [37321239628](https://github.com/zhigu34/one-nvr/actions/runs/37321239628) 10/12 job 通过，两个真实媒体浏览器 job 失败。新增独立 `joint-media-runtime` 使用真实生产 API、Worker、Nginx、固定 ZLM、隔离 PostgreSQL 和浏览器镜像。它要求两个通道各有实际完整片，再逐项核验 API/Worker 独立与共同重启、数据库停机与 spool 重放、目录证书自动更新、ZLM 重启新 run、可选模块不可达，以及发布进程在移动前后真正被 SIGKILL 后恢复。结果尚未通过，不以夹具编译或测试启动代替验收。

本机真实 Go API / PostgreSQL / Chromium 四项源操作回归再次全部通过（5.8 秒）；网关私有媒体路径的静态页面匹配检查、Linux 夹具编译/Go vet、Shell 与 YAML 校验通过。本机没有 Docker daemon，实际 Linux 进程与媒体联合证据必须来自 CI。

32 槽位只验证固定通道映射，两组主/子流样本不代表 16–32 路实际负载，也不替代用户两台机器的部署验收。M1-C 将提供 WebRTC 实时观看、授权 Range 录像播放与导出；Frigate 事件/可选事件录像和 OpenList/WebDAV 归档仍属于后续阶段。M1-B 不自动清理正式录像。

本轮联合验收分别停在子流不可用和启用普通录制时报 `bitrate_unknown`。监控调度将空通道也计入四个并发名额，16/32 槽位可能使已配置源超过 30 秒证据有效期才被再次检查。真实 PostgreSQL + 实际监控循环回归先失败（30.29 秒），跳过空槽位并保留主/子流清理候选后通过（23.742 秒）。进一步检查发现停用通道的恢复会话没有切换归属时会漏清理，新增回归先失败，修复后尚待联合 CI。子流连接错误增加仅含固定阶段的诊断码，不记录上游 URL 或原始错误。尚未确认子流失败的完整根因，不宣称该联合验收通过。

本机整套集成测试触及 Go 默认十分钟上限（602.869 秒），此前没有断言失败；重新采用 CI 同样的二十分钟测试预算运行。此超时记录保留，不把不完整本机测试当作通过。

## 最终评审修复候选（2026-10-05）

单次独立整分支评审发现的八项重要问题进入一个作者修复批次；另将存储池不可用时索引仍显示“可用”提升为重要问题。已完成先失败后通过的回归：无活动主流时关闭录制并终结旧 run、停录成功但提交中断后恢复、16 个已配置通道 FIFO 采样、9.9/10.1 秒采样抖动、清空后的导入首次配置元数据和改映射提交、保存修订后保留密码，以及存储池检查使用健康通道/新鲜测试草稿。新鲜容量证据仍要求三十秒内两次有效采样，间隔至少十秒。

可选服务 DNS 不可达时，基础摄像头边界仍可建立；核心地址解析失败仍拒绝连接。连接所在容器网段整体受限，显式摄像头 /32 或 /128 映射除外；已解析的管理地址始终禁止。联合夹具增加可选模块缺失后 ZLM 重启重新连接，不能仅以现存录像继续作为独立性证据。

新增实际镜像联合场景：候选源测试成功后停止新旧两组模拟发布进程，要求明确的回滚失败、保留原修订、释放变更锁、关闭候选/回滚物理会话并显示不可用；恢复模拟发布后要求新 session/run 和两路新 ready 文件。夹具编译通过，实际执行仍待本候选 GitHub CI，不提前标记通过。

当前本机完整前端 17 suites / 91 tests、类型、ESLint 和生产构建通过。最终重要问题数据库定向回归56.658秒通过；完整二十分钟预算 Go race/PG 通过，integration974.086秒、media45.787秒。该本机整套运行开始于旧run终结的最后细化之前；该细化由独立定向回归确认。旧Docker后端缺少FFmpeg，相关媒体数据库用例实际被跳过，不能将其成功视为整套媒体数据库回归的证据。上一候选 run37329858939仍为10/12通过，两项媒体浏览器均在启用录制时报bitrate_unknown；保留该失败证据，不能代替新候选验收。

延期细节：M1-A 用户/存储池操作反馈重挂载、时区搜索预览；本决策摘要中“run 冻结时区”的文字与绑定规格存在差异，实际实现按首次持久发布意图冻结。此次未改动该既有文档文字或运行时规则。

完整清空通道 CSV 回归通过（24.129秒）：已成功应用的通道清空后再次导入，真实子任务执行源测试和应用；保留 none、事件开关、原存储池与旧 ready 文件归属。修复候选 native `b0074c5` / remote `122bc056` 树一致，CI [37335925913](https://github.com/zhigu34/one-nvr/actions/runs/37335925913) 最终9/12通过；后端非媒体 race/PG、前端、部署、网关和镜像构建通过，媒体合同的失败换源回滚以及两项实际媒体浏览器未通过。

2026-10-06：两项浏览器仍在开启连续录像时被 `bitrate_unknown` 拒绝。独立真实 PostgreSQL 回归已复现：用户等待使样本过期后，策略任务持通道锁，Monitor 无法补充证据。修复由策略任务在停止旧录像前自行验证解码帧并采集间隔十秒的真实码率，再重新执行同一容量门禁；回归 RED12.731秒→GREEN23.976秒。冻结帧、未知码率拒绝与无主流停录联合回归通过（70.384秒）。存储池恢复预算回归 RED20.727秒→GREEN47.175秒（含原有健康通道/新鲜草稿回退）；检查先尝试当前配置源，再尝试成功草稿，避免不可达历史草稿先耗尽回滚窗口。合同回滚新增仅含 phase/code 的失败诊断，不输出上游连接或凭据。整套 race/PG 与实际镜像重跑尚未完成，不将本机定向结果当作联合验收通过。

核对 run37403045837 后端日志发现数据库回归耗时仅68.238秒；测试镜像没有FFmpeg，依赖工具的媒体回归被跳过。此前“后端整套通过”的表述范围过大；本机有实际工具，完整媒体回归不受此缺口影响。新增Go测试镜像安装与正式应用相同的锁定媒体包，开发脚本显式构建并强制 `ONE_NVR_REQUIRE_MEDIA_TOOLS=yes`；缺失工具不再跳过。缺失工具失败闭合回归 RED1.565秒→GREEN5.772秒（含实际MP4发布/结构检查）。后端job预算30分钟覆盖20分钟整套测试预算与构建/单元/vet；未放宽单个媒体或源操作期限。该Linux补齐验收仍待新精确候选CI。

run [37403045837](https://github.com/zhigu34/one-nvr/actions/runs/37403045837)，native `4c825ab` / remote `d357606e`：最终10/12通过。实际固定媒体六阶段全部通过，包括失败源回滚；普通录制开启和首次换源在实际浏览器通过。媒体浏览器随后在下一次草稿提交时409，外部API已看到域提交但页面版本尚未刷新；连续换源/清空后重新加载页面再编辑，保持真实乐观锁，未自动重试409或覆盖版本。联合浏览器在第二次应用时503：30秒池写入证明已过期，60秒正式片不能始终提供当前写入证明。验收改为应用前通过真实存储池页面请求实际新检查，第二标签页保留原页所选测试修订；不扩大证明TTL或放宽录制门禁。生产部署、网关、普通浏览器、两种镜像、硬件CPU、M0及前端job成功；联合故障场景尚未执行到，不能视为全部通过。

本机 `4c825ab` 实际媒体工具完整 race/PG 再次通过（integration1043.229秒）。补齐CI工具后的候选 native `6467b68` / remote `f53aeb92`，run [37404369875](https://github.com/zhigu34/one-nvr/actions/runs/37404369875) 正在执行完整Linux媒体数据库回归；实际媒体合同的恢复阶段遇一次管理错误便退出，其他五阶段通过。生产Monitor会周期重试该未知管理状态，验收等待器改为同样的10秒周期，在原75秒预算内重新观察，失败观察不读取/接受新session。回归 RED0.518秒→完整media夹具 GREEN55.961秒，持续未知仍失败。该观察器修正实际容器结果待下一精确候选，不将本机通过当作恢复验收通过。

run37404369875 完整Linux媒体数据库回归已通过（integration819.601秒），强制FFmpeg/FFprobe工具检查生效，Go单元race、vet与迁移检查通过。真实浏览器和联合故障验收仍在运行；媒体恢复阶段的观察器修正尚待下一候选容器复验，未标记整体通过。

run [37404369875](https://github.com/zhigu34/one-nvr/actions/runs/37404369875) 最终10/12通过。真实媒体浏览器完整流程通过（5.9分钟），联合job中的相同浏览器流程也通过；随后第二通道初始化请求不存在的单通道GET而404，尚未进入故障阶段。隔离本机实际API确认旧GET404、已有source/status接口200且返回正配置版本；联合夹具两处读取改用该授权接口。恢复观察器和该夹具修正将在下一精确候选CI复验。

候选run37406888801在前置数据库执行期间被替换：同类不存在GET还出现在最终双源失败夹具，必然404；修正覆盖三处读取。双源故障注入前也通过真实授权池检查刷新证明，与连续换源的既有前置条件一致。上轮完整Linux媒体数据库819.601秒覆盖未变化的生产代码，但被替换候选不作为整套通过证据，下一精确提交仍需全部12个job通过。

Task9 最终命令再次通过：生成类型无漂移、lint、生产构建和17套91项实际Chromium回归（4.17秒）。当前实现的前端及生产逻辑相对已通过真实媒体浏览器的f53候选未变，后续差异只涉及验收观察器/夹具与说明文档；Task9完成，Task10完整联合门槛仍待当前精确CI。

run [37407379502](https://github.com/zhigu34/one-nvr/actions/runs/37407379502) 最终10/12通过，完整Linux媒体数据库804.110秒和固定ZLM六阶段通过；两项媒体浏览器在最后停录/清空失败：立即计数仅一个就绪流、清空提交旧版本409。验收先等待回滚后的主/子流均实际解码就绪，再停录并在既有30秒内确认保留两路；绑定池和停录后刷新页面再做下一版本化操作。生产停录不关闭物理流，子流由周期监控恢复；未修改生产限期或版本保护。三个自建镜像的独立浏览器job改与完整后端并行，最终仍需12/12精确门槛，减少夹具迭代的串行等待。联合故障阶段仍未执行到，不宣称通过。

run [37410251912](https://github.com/zhigu34/one-nvr/actions/runs/37410251912) 最终11/12通过，普通/真实媒体浏览器通过（实际媒体6.1分钟），联合媒体浏览器也通过（6.0分钟），双通道初始化124.32秒与snapshot通过。首次API重启后已看到两路新ready，但文件核验SQL使用API别名p.path；真实数据库只有canonical_path。中断发布夹具有同类查询，两条共享SQL在真实迁移结构下均RED SQLSTATE42703；修正持久列名，不跳过文件/FFprobe核验。首次重启文件核验和后续故障仍待下一精确候选，不能以计数成功代替全部验收。

run37411879424 普通/媒体浏览器和固定媒体六阶段通过，联合媒体浏览器5.9分钟通过；bootstrap130.47秒在普通录像策略任务未终结时失败，域阶段仍testing、任务queued/handler_failed。精确原因未确认；初始化改为每个通道开启continuous前分别执行实际源测试和真实池检查，避免复用一次循环前证明。新增诊断只输出编号、阶段、尝试次数和证明有效布尔值，不输出凭据。生产30秒证明和20秒采样窗口不变；后续文件查询和故障验收仍待当前修正复验。

## 实际联合重启与数据库停机（2026-10-06）

候选CI [37413592800](https://github.com/zhigu34/one-nvr/actions/runs/37413592800) 完成11/12个job；实际双通道初始化159.35s、API重启60.22s、Worker重启58.22s、共同重启60.21s通过，每次校验新的ready片与实际文件大小/FFprobe结构。数据库容器停止后80.03s未观测到持久完成spool，联合job失败；后续数据库恢复、证书、ZLM、模块、发布崩溃及双源失败恢复尚未运行，不能声称通过。

另在真实PG复现录像开启预检证据失效会让job保持queued/域变更保持testing，从而占用通道。主流丢失与池容量证明过期两项RED24.835s；首轮修复只解决主流，容量前置分支仍RED。最终三类预检拒绝及正常/停滞码率刷新回归GREEN82.119s；拒绝保留旧录像设置、释放通道，数据库/租约异常及发生副作用后的恢复仍保留持久重试。当前候选 [37415518262](https://github.com/zhigu34/one-nvr/actions/runs/37415518262) 验证修复及安全回调诊断，状态仍待结果。

## 预检释放与容量恢复时序（2026-10-06）

源9450816的完整本地强制媒体回归 `go test -race ./... -timeout 20m` 通过：PG集成1078.126s、媒体fixture55.977s，未允许缺工具跳过。相同代码树的CI [37415518262](https://github.com/zhigu34/one-nvr/actions/runs/37415518262) 完成11/12，完整Linux媒体/PG集成866.276s通过，实际媒体UI6.0min通过；联合初始化在89.39s以recording_change_failed回滚，不曾进入数据库停机诊断。

进一步真实PG复现：初始化有效的28s/18s码率观察，在空间恢复需两次间隔10s确认期间过期；任务持有通道使Monitor无法补采样，最终回滚为none（RED42.754s）。目标验证阶段现在在原30s窗口内继续观察真实主流帧数和码率，十秒码率双采样、30秒新鲜度、双倍容量与两次恢复确认均保留。该复现与CI错误相符，尚不能证明未输出的CI内部最后错误完全相同。实际联合停机及后续门槛依然待通过。

上述恢复时序回归及预检/过期/停滞码率/双倍空间恢复组合测试GREEN116.894s；后端单元race、vet和脚本语法通过。完整修复候选须重新完成实际CI，尚未验收。

## M1-B 最终实现验收

完整候选 [37433585085](https://github.com/zhigu34/one-nvr/actions/runs/37433585085) 全部12个job通过；native `8aeef08` / remote `b89a29f4` 代码树一致（`2dc9b620234e712ed722f87f8d9dc3e4ecb9ba6e`）。完整Linux媒体工具/真实PostgreSQL race、固定ZLM六阶段、真实前端/媒体浏览器和联合故障门槛均通过。联合检查实际双通道完整片、API/Worker独立与共同重启、数据库停机spool及重放、目录证书自动生效且ZLM容器不变、ZLM重启新session/run、可选服务缺失后重新连接、发布进程移动前后被SIGKILL的恢复、双源均失败的回滚故障及恢复、私有媒体网关边界。每次故障后检查新ready片和真实FFprobe结构，不能用启动日志代替。

唯一一次独立整分支评审的八项重要问题和升级的可用性标签问题均已在同一作者修复批次验证。三项延期细节保持记录。后续收尾提交只归档计划/验证说明，最终分支精确提交CI以草稿PR检查为准；本报告保留此前所有失败和取消记录。

联合回调诊断在API/Worker/共同重启后实际完成回调累计14→16→18→20，恢复扫描计数为0；数据库停机25.01s观测到私有spool，恢复31.20s重放并验证新文件。早期80秒未观测到spool的精确原因仍未确认，失败记录保留，不声称改动过spool实现。最终候选通过实际落盘与恢复断言；后续最终分支检查提供额外复验。

草稿 [PR #2](https://github.com/zhigu34/one-nvr/pull/2) 已创建并附加，base为尚未合并的M1-A分支；未部署用户真机。

Final documentation candidate CI `37436956900` passed 11/12 jobs (full Linux/PostgreSQL race 877.551s), including real database outage spool 24.01s/recovery 31.19s and SIGKILL boundaries 88.50s/59.39s. The joint job failed at synthetic upstream absence confirmation before rollback apply. Its exact response classification was not recorded. A deterministic delayed-media-info regression reproduced the one-second full Inspect returning unknown before it could make its separate online query (RED 1.440s; direct successful online=false evidence race GREEN 1.510s). The fixture now requests only isMediaOnline, still rejecting unknown/error/malformed responses and keeping the five-second observation budget; production code is unchanged. The next exact published candidate must pass all12 actual gates.

Candidate CI `37440825200` joint bootstrap failed after 93.30s with recording_change_failed, before reaching the modified both-source absence observer. Main/sub streams were healthy and the ZLM write proof was expired at cleanup. A real-migration/MP4 regression isolates proof expiring during mandatory two-sample capacity recovery: RED 41.118s, then combined proof/rate/recovery/preflight race GREEN 126.725s. Policy/pool target verification now uses the existing actual CheckPool write/MP4 path to refresh expired proof inside its original phase deadline, matching source-switch verification; no TTL or capacity/bitrate gate was changed. The exact unlogged target error in CI remains unconfirmed; the next candidate still requires complete native and all12 actual CI gates.

Candidate CI `37443581394` joint browser failed before bootstrap while waiting for restored substream after a successful main rollback; its independent media-browser job passed. Fixed stage was sub_source_inspect_unavailable; underlying timeout/API classification was not logged. Native `0316475` complete required-media/PG race passed (integration1123.388s). The primary upstream implementation dispatches detail queries onto the media owner thread, while online status directly checks registry presence (https://github.com/ZLMediaKit/ZLMediaKit/blob/master/server/WebApi.cpp); this is a mechanism, not evidence of the exact pinned-image failure. Delayed-detail client regression RED0.675s→client race GREEN1.537s; recovery observation regressions GREEN1.650s. Inspect now checks successful explicit registry presence first, still rejecting failed/malformed responses and still verifying absence again if details race with disconnect. Fixed stage/deadline/media booleans improve subsequent substream diagnostics without raw errors or credentials. Latest candidate still needs whole-suite and all12 actual CI gates.

Candidate `cd4fb371b209048ae48db6559079d28b9577b984` CI `37446017324` passed 11/12 jobs. Native complete required-media race integration1126.905s and media55.924s passed. Joint actual UI5.6m, bootstrap158.40s, API58.25s, Worker60.25s and combined restart60.25s passed. Database outage spool80.04s failed: readable=true, complete0, pending0, mediaFiles17, two recorder flags true. This does not distinguish continued frame/file generation from callback delivery/intake; no spool fix is asserted. Diagnostic-only next candidate retains the same eighty-second gate and adds fixed callback lifecycle stages plus pre/post frame and media-file counts, without payloads, credentials or file names. Whole actual acceptance remains required.
