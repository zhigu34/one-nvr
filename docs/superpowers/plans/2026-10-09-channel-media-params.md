# 通道管理页：媒体参数与接入方式

**Goal:** 通道列表新增「媒体参数」列（分辨率 / 视频编码 / 帧率 / 音频编码，音频按能否经实时预览出声着色）与「接入方式」列（ONVIF / RTSP），并把「摄像头」列精简为只显示 IP（完整地址进悬停提示）。

**Spec:** [PRD](../../PRD.md) CH-02（列表显示）；M1-B 的 source revision / 测试证据模型；音频口径见 `.workbuddy/memory/2026-10-09.md`（ZLM WebRTC 仅支持 opus / PCMA / PCMU）。

## 用户指示（2026-10-09，权威）

1. 优化通道管理页的参数显示 → 追问后确认：媒体参数列 + 音频信息显示。
2. 加上摄像头的类型：RTSP / ONVIF。
3. 摄像头列只显示 IP。

## 关键设计决策

1. **数据来源复用测试观测，零新增探测**：媒体参数取自「当前生效修订的最近一次成功测试」（`source_tests.result`），与 `bitrate_kbps` 同为"最近观测"口径。不新增运行时探测、不调用 ZLM、无迁移。
2. **音频编码零成本采集**：`FirstFrame` 用的那份 ffmpeg 日志本来就有输入流 `Audio:` 行，顺手解析。三态语义：`audio_codec` 为 nil = 旧记录未采集（显示「—」，重测一次补齐）、`""` = 确认无音频（显示「无音频」）、编码名 = 实际编码。
3. **接入方式按 `onvif_port` 判定**：ONVIF 方式保存时写入该列（迁移 `0005` 即有），纯 RTSP 接入为空。未配置修订显示「—」。**不新增"添加动作"字段**——"有没有 ONVIF 端口"是比"当初点了哪个 tab"更有用的定义。
4. **「预览无声」着色在前端**：ZLM 的 WebRTC 只转发 opus / PCMA / PCMU，其余编码（典型是 AAC）在实时预览中无声；列表用琥珀色标记 + tooltip 说明，将来 ZLM 行为变化只改前端常量表（`media.ts`）。
5. **展示层不混合状态**：媒体参数是参考信息（摄像头离线也照显，悬停给观测时间），状态列仍是唯一的状态来源；两者互不推导。
6. **不新增旁路**：媒体参数只追加进现有 `GET /channels/summary` 富集，权限沿用 grants 模型，不外泄给无授权者。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 探测 | `internal/media/probe/runner.go` | `FirstFrame` 解析 `Audio:` 行；`VideoEvidence.audio_codec`（nil / "" / 编码名三态） |
| 类型 | `internal/channel/source_types.go` | `StreamTest.audio_codec`；新增 `MediaParams`；`SummaryItem.onvif_port` / `main_media` / `sub_media` |
| 落库 | `internal/recording/source_testing.go` | 测试成功时把探测到的音频写进 `StreamTest` |
| 列表 | `internal/channel/source_status.go` | source 富集读 `r.onvif_port`；新增媒体参数富集（`DISTINCT ON (channel_id)` 取当前修订最近一次成功测试的 result） |
| 契约 | `docs/api/m1b.openapi.yaml` + 重生成 `api-types.ts` | `StreamTest.audio_codec`；`ChannelSummary.onvif_port` / `main_media` / `sub_media`；新增 `ChannelMediaParams` |
| 前端 | `apps/web/src/features/channels/`：新增 `media.ts`、`media-cell.tsx`；改 `index.tsx` | 显示映射与格式化；媒体参数两行渲染 + 音频着色 + 观测时间 tooltip；新增两列；摄像头列只显示 IP |
| 测试 | `runner_test.go`、`source_switch_test.go`（替身加音频）、`tests/integration/source_status_test.go`、`index.test.tsx` | 见下 |

## 测试覆盖

- **probe 单测**：有音频（`pcm_alaw`）；无音频 → **空串而不是 nil**（"确认没有" ≠ "没问过"）。
- **集成测试**：`apply` 后 summary 带 main/sub 媒体参数与 `audio_codec: pcm_alaw`；另插一条带 `onvif_port` 的替换修订（无测试）→ `onvif_port` 带出、**媒体参数清空**（未测试不继承旧修订）；RTSP-only 修订不带 ONVIF 端口。
- **前端单测**：ONVIF / RTSP / —；`2560×1440 H.264 25fps` 与 `音 PCMA` / `音 AAC`；摄像头列只显示 IP（路径不在 textContent、在 title）。

## 未做 / 未验证

- 配置页（`source-form` 测试面板）不同步显示媒体参数——本批只动列表页。
- 音频「预览无声」着色的现场确认：NAS 部署 + 对已接入通道重测一次后目视（真机大华 PCMA 应常规色、海康 AAC 应琥珀色）。
- ZLM 升级若开始支持 AAC，需要同步改前端常量表。
