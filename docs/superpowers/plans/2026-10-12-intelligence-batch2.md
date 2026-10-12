# M3 智能与事件批次 2：检测配置、能力展示与配置发布

**Goal:** 每通道可启停检测、选择检测码流/帧率/目标类别（AI-01），页面显示模型/检测器/运行状态/配置版本（AI-07）；配置经版本化发布落到 Frigate `config.yml` 并触发其 reload，失败可回滚、不静默降级。区域多边形与规则（AI-02/03/04/06）留批次 3。

## 关键设计

1. **检测源 = ZLM 内部 RTSP（PRD §10.2）**：camera 的 `ffmpeg.inputs` 指向 `rtsp://zlm:554/one_nvr/<stream_session>`（用现有 probe 内部 URL 的同一约定），`record.enabled=false`（正式录像由 ZLM 负责），snapshots 开启。
2. **Frigate camera 键 = 通道的稳定检测名**：`ch<通道号>`（如 `ch01`），不随通道改名变化；批次 1 的 `frigate_cameras` 映射表因此可以自动派生（发布时同步写入），管理员无需手工维护——手工映射仍保留为兜底。
3. **配置版本化 + 串行发布**：`detection_configs`（每通道一份：enabled、stream（main/sub）、fps、labels[]、version）+ `detection_config_revisions`（发布快照，含渲染出的 config.yml 哈希）；发布 = 渲染全站 `config.yml` → 写入 `<data>/frigate/config.yml` → 经 MQTT 发布 `frigate/reload`（Frigate 官方 reload 机制）→ 记录结果。发布失败保留旧文件并报错（不静默降级）。
4. **能力展示**（AI-07）：worker 定期经 Frigate HTTP 拉 `/api/version` 与 `/api/stats`（检测器、推理速度、过载迹象）落 `detection_runtime`（单行心跳表），页面展示模型/检测器/运行状态/最近发布版本；未知类别或不支持能力禁止保存。
5. **检测代次**（CH-08）：`channels.detection_generation` 随检测 stream/启停变更递增；事件按发布时的代次归属（批次 1 的 events 表已留 `source_revision_id`，本批补 `detection_generation`），换源后旧代次事件不并入新代次。
6. **不中断录像**（PRD §10.2）：发布只改 Frigate 配置，ZLM 录像链路零接触；页面明确「检测发布不影响录像」。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0019_detection.sql` | detection_configs + detection_config_revisions + detection_runtime + channels.detection_generation |
| 包 | `internal/detection/{service,publish,render}.go` | 配置 CRUD、渲染器（纯函数，可单测）、发布（写文件+MQTT reload）、运行时统计抓取 |
| 装配 | run.go | worker：运行时统计循环（60s）；发布走 jobs（`detection.publish`） |
| HTTP/契约 | `httpapi/detection.go` + m1b + api:generate | GET/PUT /detection/configs、POST /detection/publish、GET /detection/runtime |
| 前端 | `features/detection/`（新页「检测配置」，监控组） | 每通道：启停/码流/帧率/类别多选；顶部能力卡（模型/检测器/状态/版本）；发布按钮 + 结果 |
| 测试 | 渲染器单测（黄金文件）、集成（CRUD/发布写文件/失败保留旧版/代次递增）、vitest | 见下 |

## 验收对照

- AI-01：每通道启停/码流/帧率/类别可配、可保存、可发布。
- AI-07：模型/检测器/运行状态/版本可见；未知类别拒存。
- 「配置可回滚」：发布失败保留旧 config.yml；revision 表记录每次发布的成功/失败。
