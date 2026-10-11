# 云归档批次 3：本地安全清理、播放租约、仅云端回放与云端到期清理（M2.1 收尾）

**Goal:** 补齐 M2.1 出口条件的三件事——「上传失败不停止本地录制」（已在批次 2 证实）、「清理本地后同一录像 ID 可回放」（ARC-04/05）、「最大保留期删除与保护例外可验证」（ARC-09）。

## 范围

1. **播放租约（PLAY 租约，M1-C 推迟项回归）**
   - 迁移：`recording_playback_leases(segment_id PK, lease_expires_at, fencing_token, created_at)`。
   - `/recordings/{id}/content` 成功打开本地文件并审计后登记租约（60s，播放端每 30s 心跳续租：`POST /recordings/{id}/lease`；连接结束前端调用释放或自然过期）。
   - 删除/清理/到期三处检查活跃租约：被租约占用的分片跳过并给理由（`lease_held`）。POSIX fd 语义保证在途播放读完；租约防的是「删完再发起新请求」。
2. **本地安全清理（ARC-05）**
   - 保留扫描扩展：有归档策略（manual/scheduled/immediate 任一）的通道，本地候选 = 保留期届满（默认 48h，`sites.archive_local_retention_hours`，0=永不自动清理）**且**该分片存在 `verified` 且未到云端保留截止的副本 **且** 无保护/任务/租约。
   - 云不可达时停止归档通道的自动清理（最近探针不健康即视为不可确认）；未启用归档的通道维持原 7 天语义。
   - 上传中/等待上传/未验证的候选不可清理（沿用批次 1 的 `archiving` pin）。
3. **仅云端 Range 回放（ARC-04）**
   - `/recordings/{id}/content`：本地不可用（deleted/missing 或文件打开失败）且存在 `verified` 云副本时，代理 WebDAV Range（206/416/Content-Range 透传、限速限时、不泄凭据）；本地可用永远优先。
   - 云端读取失败单独错误码（`cloud_read_failed`），不回写归档成功历史。
4. **云端到期清理（ARC-09）**
   - `cloud_expires_at = 分片实际结束时间 + 目标 cloud_retention_days`（ARC-01 已留列）；worker 每小时分批扫描过期副本 → DELETE 远端对象 → 状态 `expired`（保留行与审计）。
   - 保护与活跃租约例外：跳过并计数展示；本地/云端全无副本时保留元数据与时间轴缺口，不无限保留过期对象。
5. **前端**：管理页摘要加「仅云端」数；回放对仅云端片段如实标注（云端读取失败单独提示）；目标页显示到期清理能力提示（承接批次 1 探针）。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0017_archive_lifecycle.sql` | 播放租约表 + `recording_archive_copies.cloud_expires_at` + `sites.archive_local_retention_hours` |
| 归档 | `internal/archive/{expiry,proxy}.go` | 到期清理扫描、WebDAV Range 代理读 |
| recording | `content.go`/`lifecycle.go`/`retention.go` | 云端回退读取、租约登记/检查、安全清理候选 |
| HTTP/契约 | `recordings.go`/m1b + api:generate | 租约心跳端点、content 云端回退语义 |
| 前端 | `recording-files`/`playback` | 仅云端标注、租约心跳、到期清理能力提示 |
| 测试 | 集成（清理安全条件矩阵、租约阻塞、云端回放 206、到期删除+保护例外+24h 内完成）、web vitest | 见下 |

## 验收对照（M2.1 出口）

- 上传失败不停止本地录制：批次 2 钩子隔离 + 集成已证。
- 清理本地后同一录像 ID 可回放：本批云端回退 + 清理候选条件集成测试。
- 最大保留期删除与保护例外可验证：本批到期清理 + 保护/租约例外测试。
