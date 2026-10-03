# one-nvr 录像文件生成与路径规则

版本：1；确定日期：2026-10-03。适用于正式版目录存储池及 WebDAV 归档。本文是设计基线，尚未实现；不改动现有 M0 文件，也不承诺已验证固定 ZLM 镜像的全部文件行为。

## 1. 正式录像规则

默认录制主码流，MP4 分片目标长度 60 秒，不转码；实际边界取决于媒体和关键帧，索引保存真实 `[start,end)`。连续、计划、手动录制共用一套文件规则，录制原因保存在元数据中，不各复制一份视频。

本地正式文件：

```text
<pool_root>/one-nvr/<pool_id>/recordings/<channel_no>/<YYYY-MM-DD>/<channel_no>_<YYYYMMDDTHHMMSSZ>_<recording_id>.mp4
```

示例（ID 为格式示例，不代表实际部署）：

```text
/storage/pool-a/one-nvr/3e7b65ae-85d9-4f44-97e8-b9cd4f896c72/recordings/CH01/2026-10-03/CH01_20261003T080439Z_6f3e2d1a9b485f07a812cc4d0e76b935.mp4
```

| 部分 | 规则 |
| --- | --- |
| `pool_root` | 已添加且验证过的存储池目录；用户只提供此目录 |
| `pool_id` | 不可变池 UUID；one-nvr 只管理该专属命名空间 |
| `channel_no` | 固定编号 CH01–CH32；不使用可修改的通道名称、摄像头名称、IP 或凭据 |
| 日期目录 | 分片录制开始时刻的 UTC 日期，格式 YYYY-MM-DD |
| 文件时间 | ZLM 完成回调中的原始 `start_time`，按 UTC 转为 YYYYMMDDTHHMMSSZ，秒精度；Z 表示 UTC |
| `recording_id` | 稳定 UUID 的 32 位小写十六进制形式；同一原始分片始终相同 |
| 扩展名 | `.mp4`；不由扩展名宣称媒体已校验或必然可播放 |

文件名不写结束时间、估算时长或可修改名称，以免后续校验修订和改名要求重命名。UUID 防止同秒分片、重启和时钟回拨产生目标重名；目标已存在时仍必须检查身份，绝不覆盖其他文件。

文件时间是服务器录制起点，摄像头画面烧录时间单独看待。回放与保留期限使用数据库中已核验的实际起止时间，不从文件名推算。后续校验修订实际时间不修改已发布路径。跨 UTC 午夜的一个分片完整放入开始日期，不为日期单独切断；站点时区修改也不迁移历史文件。UI 按站点时区显示并按索引查询：示例 08:04:39Z 在 Asia/Shanghai 为 16:04:39，跨本地日期的检索不靠遍历同名 UTC 日期目录。

首版不提供自定义路径/文件名模板，避免换源、回放、归档和清理使用不同规则。

## 2. 由 ZLM 写入，由 Worker 发布

采用“ZLM 原生写入工作目录，完成后在同池发布标准文件”的方式。相比直接保留原生流目录，增加每片一次目录移动，但正式目录可按固定通道和日期浏览；无需 fork ZLM 来增加自定义文件名功能。

```text
<pool_root>/one-nvr/<pool_id>/_work/zlm/<recording_run_id>/<ZLM 实际附加的目录与文件名>
```

`customized_path` 指向上述 run 根目录。ZLM 仍可能追加 record app、app、stream、日期等层级；one-nvr 按完成回调的实际路径读取，不硬编码文档示例文件名。上游当前源码显示先写点号临时文件，关闭后改名并触发完成通知；固定镜像中的行为必须在 M1 真机验收中核对。

每次实际重新启动录制分配新的 `recording_run_id`，Worker 重启但 ZLM 原录制仍运行时继续使用原 run。新 run 将旧/新视频源、池切换以及 ZLM 重启后的原生文件名隔开；不在同一原生目录重新启动一个会重置文件序号的录制器。自动 MP4 录制默认关闭，由 Worker 对账后用明确目录启动，避免隐式恢复写入旧目录。

启动前持久化 run 与通道、源版本、流会话、池和工作目录的绑定；同步保存不含凭据的 run 描述到池内 `.meta/recording-runs/<recording_run_id>.json`。文件带格式版本、site_id、pool_id、channel_id/channel_no、source_revision_id、stream_session_id、run_id、工作相对路径和创建时间；迟到回调按原 run 处理，不能查当前摄像头来猜来源。

发布顺序：

1. 完成回调通过身份及路径检查后先持久化 inbox；数据库不可用时写持久 spool。Hook 应答不作为已经索引/可播放的证据。
2. 原文件必须是该 run 内的已关闭普通 MP4 文件；拒绝越界/符号链接逃逸。核对存在、大小和基本轨道，记录缺失/损坏等状态，未完成文件不发布。
3. 以 `(recording_run_id, ZLM 原始相对路径)` 为唯一来源键。使用 site UUID 作为 UUIDv5 namespace，以规范化的 `pool_id/run_id/原始相对路径` 为 name 生成 recording_id；同一回调、重复扫描和重试得到同一个 ID。目录/文件名规则版本记为 1。
4. 先提交发布意图：记录原始相对路径、目标相对路径、run、文件身份/大小、校验结果与 `finalizing` 状态；此时不能播放、归档或清理。数据库无法持久化意图时保留原文件并延后发布，不影响 ZLM 继续写入。
5. 在同一池内以不覆盖目标的方式原子移动，确认文件与目录持久化；不复制整段视频、不转码。跨文件系统移动失败时保留原文件并报错，不默默复制/删除。池内部目录须支持所需 rename 和目录同步；这是目录操作能力测试，不管理底层实现。
6. 提交标准本地位置并置为 `ready` 后才进入回放和归档资格判断。原始回调路径仅作来源/恢复线索，播放走 one-nvr 的授权媒体接口，不能继续引用 ZLM 原生回调 URL。

移动成功但数据库提交失败时，已有发布意图指向目标；恢复核对目标身份后补提交。源仍存在则重试；源/目标都不存在标记缺失；两者同时存在或目标身份冲突先诊断，不覆盖或擅自删除。重复回调发现原路径已移走时查已存在的来源键，不误判为新增录像丢失。

API/Worker 故障时 ZLM 在已绑定 run 工作目录继续录制。恢复扫描只检查已登记 run 目录与未完成发布意图；裸文件名不足以恢复源版本/完整性，无法确定身份的文件列为待核查。正在写入文件、未处理完成分片和发布失败文件占用池容量并进入诊断，不能当作可随意按文件年龄删除的缓存。

## 3. 池内其他目录

所有路径均相对于 `<pool_root>/one-nvr/<pool_id>/`；只在标识匹配的命名空间执行媒体操作。

| 相对目录/文件 | 用途与边界 |
| --- | --- |
| `recordings/CH01/YYYY-MM-DD/CH01_<start>Z_<recording_id>.mp4` | 已发布原始分片；连续/计划/手动录像及未来事件保留的源片段共用 |
| `snapshots/CH01/YYYY-MM-DD/CH01_<capture_time>Z_<asset_id>.jpg` | 归档后的抓拍；时间取实际捕获时刻，关联事件与源版本保存在索引；不直接用外部 event ID 拼路径 |
| `exports/<export_job_id>/<artifact_id>.mp4`、`.zip` 或 `.json` | 导出产物/清单，独立于原录像；用户下载名可带通道与时间范围 |
| `_work/zlm/<recording_run_id>/` | ZLM 原生录制工作目录；未发布分片的恢复来源 |
| `_work/exports/<export_job_id>/` | 导出过程文件，产物确认完成后发布到 exports |
| `_work/cache/<cache_job_id>/` | 云端读取/导出缓存，下载中使用 `.part`；有独立容量预算和租约 |
| `_work/buffer/<recording_run_id>/` | P1 事件前录的滚动分片，仅在事件录像需要时使用，不属于 M1 功能 |
| `.meta/recording-runs/<recording_run_id>.json` | 不含连接凭据的来源绑定，配合数据库/发布意图恢复；不是用户录像 |

文件日期和时间均采用 UTC；内部 ID 使用系统生成的安全形式，外部输入不能成为任意路径。缓存/导出工作区可以按任务终态与租约清理；原生录制工作区中的待发布媒体必须单独对账。

事件索引引用实际相交的 recording_id，不按每个事件复制连续录像。事件开关关闭时不创建仅供事件前录的 buffer，仍可写 snapshots、保存时间点，已有常规录像继续按上述规则生成。P1 事件保留从真实缓冲分片中选择需要保留的片段并发布到 recordings；多个事件复用同一原始分片，不改它的 ID、路径或实际起止。未经选择的纯缓冲按独立短期策略清理；事件开关本身不能删除已发布录像。

## 4. 云端统一命名

```text
<target_root>/one-nvr/<site_id>/recordings/<channel_no>/<YYYY-MM-DD>/<与本地相同的文件名>
```

云端加入不可变站点 ID，两个独立站点可共用同一 WebDAV 根而不覆盖 CH01。对象键不包含本地池 ID，通道改绑池不影响命名；文件名中的 recording_id 仍区别每条源录像。本地/云端位置共用 recording_id，文件字节保持一致。远端根目录和目标配置版本在任务创建时冻结，重试使用原对象键，不按当前目标重算历史位置。

相对路径使用 POSIX `/`；WebDAV 请求逐路径段编码，不把对象键原样拼成 URL。上传失败或未验证的对象不能标为已归档；只有已登记且身份验证过的副本参加到期删除。按实际结束时间计算云保留期限，不解析文件名日期来删除文件。

## 5. M1 与后续验收

- M1 验证固定 ZLM 镜像在指定工作根下的附加路径、临时文件、完成通知和目标 60 秒分片边界。
- 更换摄像头、修改通道名称及切换存储池后，历史 ID/路径与来源正确，新片进入当前池；原生同秒文件和新 run 不覆盖旧片。
- UTC 午夜、站点时区改变、时钟回拨不产生目标覆盖，回放按实际索引检索；时间异常保留诊断。
- 重复 Hook/扫描、移动前后崩溃、数据库短时不可用可对账恢复；目标冲突、跨文件系统、权限失败时不丢原文件，不将未发布状态当作可播放。
- 未完成 MP4 不发布；ZLM 原生 URL 不绕过 one-nvr 权限。M0 原目录保持原样，不自动改名或迁移。
- M2.1 验证本地/云端同名同 ID、两个站点共享远端根、上传重试/Range/到期删除；P1 事件缓冲随事件录像开关验证。

## 官方实现依据

- [ZLM startRecord API](https://docs.zlmediakit.com/guide/media_server/restful_api.html)：提供 customized_path 和 max_second。
- [on_record_mp4](https://docs.zlmediakit.com/guide/media_server/web_hook_api.html)：完成通知提供 file_path、start_time、time_len、app/stream 等来源字段；通知本身不敏感于应答。
- [Recorder.cpp](https://github.com/ZLMediaKit/ZLMediaKit/blob/master/src/Record/Recorder.cpp)、[MP4Recorder.cpp](https://github.com/ZLMediaKit/ZLMediaKit/blob/master/src/Record/MP4Recorder.cpp)：当前上游的目录追加、原生命名和完成发布实现。master 源码只是设计依据，不能代替已锁定镜像的契约验收。
