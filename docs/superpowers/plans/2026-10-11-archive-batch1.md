# 云归档批次 1：WebDAV 目标与手动上传链路（M2.1 第一段）

**Goal:** 打通「配置 WebDAV 目标 → 对已发布分片手动发起上传 → worker 原码上传并验证 → 副本索引可见（本地+云端）」的最小闭环。PRD ARC-01（配置与探针）、ARC-02（入队执行与验证）、ARC-03（远端对象键）、ARC-08 的手动模式、FILE-02 的位置维度。

**明确不做（批次 2/3）：** 定时/立即自动上传调度（ARC-08 其余）、本地安全清理与播放租约（ARC-05）、仅云端回放（ARC-04）、云端到期清理（ARC-09）、远端核验与缓存（ARC-07）、云源导出、容量压力淘汰与配额（STO-02/03）。

## 关键决策

1. **上传权限单独授予**：`channel_grants` 加 `archive` 列，`auth.Action` 加 `Archive`，管理员默认拥有（迁移回填 admin 既有行 + roleAllows 天然允许）；操作员默认没有。Channel.permissions 枚举加 `"archive"`。
2. **凭据复用源凭据加密**：`secrets.SealCredential/OpenCredential` 以 (site, target, target, "password") 为 AAD——与源凭据同一密钥派生，绑定目标 ID，库中只存 key_id/nonce/ciphertext。
3. **远端对象键 = `<remote_root>/<site_id>/recordings/<通道号>/<本地日期>/<与本地同名文件>`**：直接由本地 `target_relative_path` 推导（ARC-03：共用录像 ID 与冻结命名，不按上传时区重命名，换源/改名不改键）。
4. **探针照 storage probe 语义**：PUT 探针文件 → GET 全量比对 → GET Range → DELETE，逐项能力（write/read/range/delete）落 `last_check`；缺 DELETE 显示「到期清理不可用」而不是全绿（批次 3 消费）。
5. **验证 = 存在 + 长度一致**（ARC-02 最低要求）：上传时 TeeReader 计算 SHA-256 入库；PUT 后 GET `bytes=0-0` 取 Content-Range 总长比对；哈希校验留远端对账批次。
6. **上传中的源不可删**：`deleteOne` 的 pin 检查扩展到 `uploading` 状态副本（理由 `archiving`），与导出 pin 对称。
7. **jobs kind `recording.archive_upload`**：幂等键 = segment+target，worker 并发 2；上传失败走 jobs 退避重试，凭据/权限类失败 `PermanentFailure`。
8. **List 带 `archived`**：`publicationColumns` 加 `EXISTS(verified copy)`，Segment 加 `archived`——契约向后兼容的加列。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0015_archive_targets.sql` | `archive_targets`（凭据三列 + last_check + cloud_retention_days 预留）、`recording_archive_copies`、`channel_grants.archive` + admin 回填 |
| auth | `authorize.go` | `Archive` action：validAction/requireChannel/RequireAnyChannelTx/GetGrants/SetGrants/RequireActorChannelTx 全部带上新列 |
| channel | `service.go` | `permissions()` 与两处查询带 archive |
| 归档 | 新增 `internal/archive/{webdav,service}.go` | 客户端（Put/GetRange/Delete/Check，限时限读）、目标 CRUD+探针、CreateUploads（逐行 archive 鉴权）、HandleUpload（流式 PUT+SHA、长度验证） |
| recording | `query.go`/`types.go` | Segment.`Archived` |
| HTTP | `internal/httpapi/archive.go` + router | `/archive-targets` CRUD+test、`POST /recordings/archive` |
| 装配 | `internal/app/run.go` | worker 2 个 `recording.archive_upload` 循环 |
| 前端 | `features/archive-targets/`、`recording-files`、sidebar | 归档目标管理页（admin）、管理页「上传到云端」+ 「本地+云端」徽标 |
| 测试 | webdav 单测、集成（CRUD 权限/探针/上传/验证/badge/删除保护）、vitest | 见下 |
