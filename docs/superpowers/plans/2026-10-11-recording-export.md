# 录像范围导出与证据（M2 收尾）

**Goal:** 补齐 M2 出口条件的最后一块——操作者从回放时间轴选范围（或用片段边界）发起导出，系统按「原码拼接」产出带证据清单的 MP4/ZIP；导出中的源分片被临时引用保护；产物默认 7 天到期、可永久保护。

**Spec:** [PRD](../../PRD.md) EXP-01（任务模型）、EXP-02（时间精度与范围分析）、EXP-03（证据清单）、EXP-04（时间窗口保护）、EXP-05（任务保护与产物保留）、FILE-06（导出与证据入口）；[M2 决策](../../M2-decisions.md)「明确不做」对本批的解除。

## 范围

**做：** 迁移 `0014`（`recording_exports` / `recording_export_artifacts` / `recording_export_sources`）、同步范围分析端点、异步导出任务（jobs 基座，worker 2 并发）、ffmpeg 原码拼接 + 输出校验 + SHA-256、ZIP 下载（含清单）、产物 7 天到期与永久保护、按时间窗口批量保护源分片（EXP-04）、回放页导出入口 + 录像管理页「导出记录」抽屉、契约与测试。

**不做（后续批次）：** 精确裁剪/转码模式（EXP-02 可选项）；长时段自动拆分编排（服务端先强制单次 ≤30 分钟，前端提示）；仅云端源分片导出（M2.1）；跨通道导出；容量压力淘汰与配额（依赖 M2.1）。

## 关键设计决策

1. **任务模型复用 `jobs` 基座**：`recording_exports` 行是领域事实（窗口、缺口处理方式、状态、进度、错误、创建人），执行用 `jobs(kind='recording.export')`；worker 起 2 个消费循环 = PRD 的「默认最多 2 个并发，其他排队」。租约/续期/崩溃重试用 `jobs.Execute` 现成机制；崩溃后重跑**从零开始**（拼接快、`.work` 工作目录按 exportID 清理重建），产物只在最终原子发布后登记，不存在半成品可见。
2. **范围分析是同步只读端点**（`GET /recording-exports/analyze`）：合并覆盖、连续分组、缺口、暂不可用（非 `ready` 状态）片段全部实时计算；分析**不**校验文件在不在。创建任务在**同一事务**里解析并固定（pin）ready 源分片列表（`recording_export_sources`）作为临时引用——删除路径（批量删除、保留策略）跳过被 `queued/running` 导出引用的分片（EXP-05）；导出终态后引用自然失效。
3. **快速拼接 = ffmpeg concat demuxer `-c copy`**：参数一致性用各分片已入库的 `media_evidence` 视频参数（codec/width/height/fps 全等才拼），不一致 → 任务 `failed: media_incompatible`（EXP-02：不静默切换高开销转码）。音频编码未按分片入库——同一连续 run 内参数一致，跨 run 由输出校验兜底；清单如实列出源分片与源版本。产物完成后 ffprobe 校验（可读、时长与预期覆盖在容差内）再登记。
4. **产物落池已预留的 `exports/` 命名空间**：`exports/<exportID>/<seq>.mp4`；工作文件写 `.work/exports/<exportID>/`，完成后仿 `atomicPublishWith` 原子 rename + 目录 Sync，最后单事务登记 artifact + `succeeded`。清单 JSON **不落池**，下载 ZIP 时由 Go stdlib 现场打包（SHA-256 早已算好入库），避免第二份事实。
5. **容量闸两道**：创建时用 `storage_pool_checks` 新鲜 `free_bytes` 对比预估产物（覆盖分片字节和 ×1.05 封装余量），不足 `409 low_space`；worker 写入前复查，不足 `failed: low_space`。输出池默认通道绑定池，管理员可改选启用池；创建时固定池引用。
6. **单次窗口 ≤30 分钟**（EXP-01）服务端强制（`422 window_too_long`），前端预校验并对更长范围提示拆分（自动拆分编排留批次 B）。
7. **进度**：worker 解析 ffmpeg `-progress` 的 `out_time`，节流（≥2s）回写 `recording_exports.progress/message`——既是任务进度也是活性证据；前端抽屉在有 `queued/running` 行时 2s 轮询。
8. **产物生命周期**：`expires_at = 完成时间 + 7 天`（PRD 默认），`protected=true` 永久保留（admin 开关，对齐批次 1 保护语义）；到期清理挂在既有保留策略扫描里（unlink → `state='expired'`），下载遇 expired 按 404 处理（与本地删除同语义）。
9. **权限**：analyze/create/list/下载用通道级 `auth.Export`（`channel_grants` 早有该 action）；时间窗口保护（EXP-04）与产物保护开关 admin-only。审计：`recording.export_requested` / `recording.export_downloaded` / `recording.export_window_protected` / `recording.export_artifact_protected`；worker 侧成败不写审计（导出行本身是完整记录）。
10. **清单内容**（EXP-03，JSON）：格式版本、任务 ID、创建人（创建时反规范化 id+用户名，用户删除不失证）、通道编号/ID/名称、请求与实际起止、缺口区间、缺口处理方式、各产物（文件名/SHA-256/大小/实际起止/源分片数）、源分片列表（segment id、源版本 id、起止——**不含文件系统路径**，脱敏来源摘要即源版本+时间）。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `migrations/0014_recording_export.sql` | 三表 + 索引（artifact 到期部分索引、exports 按 channel+created_at、UNIQUE(pool_id,relative_path)） |
| 服务 | 新增 `internal/recording/export_types.go`、`export.go`、`export_run.go`、`export_download.go` | 分析（覆盖/分组/缺口）、创建（pin+入队+审计）、列表/详情、窗口保护、worker 拼接管线、下载流与 ZIP |
| 改动 | `internal/recording/retention.go` | 保留扫描里加产物到期清理 |
| 改动 | `internal/recording/lifecycle.go`（DeleteLocal） | 删除前拒绝被活跃导出引用的分片（逐项理由 `pinned_by_export`） |
| HTTP | 新增 `internal/httpapi/recording_exports.go`；改 `router.go` | 7 个端点：analyze / create(202) / list / get / download(ZIP) / artifact content / artifact protection + `POST /recordings/protect-window` |
| 装配 | `internal/app/run.go` | worker 分支加 2 个 `recording.export` 消费循环 |
| 契约 | `docs/api/m1b.openapi.yaml` + `pnpm api:generate` | 端点与 `RecordingExport` / `RecordingExportArtifact` / `ExportAnalysis` 等 schema |
| 前端 | 新增 `features/recording-exports/`（对话框 + 抽屉 + api 封装）；改 `features/playback/index.tsx`、`features/recording-files/index.tsx` | 选中片段卡「导出」入口（对话框内可调起止、展示分析、选缺口处理）→ 202 → 进度轮询 → 下载；管理页工具栏「导出记录」抽屉（列表/进度/失败原因/到期/下载） |
| 测试 | Go 单测（覆盖合并/分组纯函数、清单构造）、集成（全流程、权限、引用保护、容量、参数不一致、窗口保护）、vitest（对话框/抽屉）、e2e 媒体块（提交→轮询→ZIP 校验→负例） | 见下 |

## 测试覆盖

- **单测**：合并覆盖与贪心分组（含重叠分片跳过）、窗口/时长校验、产物命名、清单 JSON 字段完整性。
- **集成**（本机 PostgreSQL）：管理员创建→作业→产物登记→ZIP 清单字段与 SHA-256 复算一致；非 admin 有 export 授权可创建、无授权 404/403；无 ready 分片 422；窗口 >30min 422；容量不足 409；参数不一致任务失败 `media_incompatible`；`queued/running` 导出引用的分片批量删除被拒 `pinned_by_export`、导出终态后可删；窗口保护按相交更新并审计；产物到期清理跳过 protected。
- **前端 vitest**：对话框分析展示与缺口选择、提交请求体与幂等键、进度轮询、抽屉列表/失败原因/下载链接、权限 gating。
- **e2e（CI 媒体块）**：真实容器内对已发布段走 analyze→create→succeeded→下载 ZIP（PK 头 + manifest 字段 + SHA 复算）→artifact MP4 `ftyp` 头；viewer 无 export 权限 403/404 负例。

## 未验证 / 未做

- 真机 ffmpeg 拼接跨厂商编码（同通道换源后参数漂移）只能靠 CI 合成摄像头覆盖单参数场景；现场如有 H.265/音频参数漂移的源需实测。
- 精确裁剪、长时段拆分编排、ZIP 之外的打包、云源导出 → 后续批次。
