# 云归档批次 2：通道归档策略与自动上传调度（M2.1 第二段）

**Goal:** 通道绑定归档目标并选择上传模式（ARC-08）：立即上传（发布即入队）、定时上传（站点时区每日时刻扫描补齐）、手动（批次 1 已交付）。发布失败不停止本地录制；调度去重防多实例重复。

**明确不做（批次 3）：** 本地安全清理与播放租约（ARC-05）、仅云端回放（ARC-04）、云端到期清理（ARC-09）、远端对账（ARC-07）、每周计划与多时刻之上的复杂日历、配额（STO-02/03）。

## 关键决策

1. **策略 = 通道级一行**（`archive_policies`：target_id + mode manual/scheduled/immediate + schedule「逗号分隔 HH:MM」），删除行即回手动。立即/定时模式在**分片发布事务内**固定归档要求（ARC-05：等待计划执行的源不可被提前清理——清理侧批次 3 消费 uploading/verified 副本与策略存在性）。
2. **立即上传经发布事务钩子**：`recording.Service.OnPublished`（可选函数字段，worker 装配注入 archive.Service 的 tx 内方法），hook 错误只记日志、**不使发布失败**（PRD：上传失败不停止本地录制）。
3. **定时调度去重**（ARC-08「以策略和触发时刻去重，多实例不重复领取」）：`archive_schedule_runs(policy_id, fire_date, fire_time)` UNIQUE，入队与记录同事务；worker 每 30s 扫描 scheduled 策略，站点时区当天时刻命中即补扫全部未上传 ready 分片（分批 ≤100）。
4. **入队内核共享**：批次 1 的 `CreateUploads` 抽出 `enqueueUpload(tx, targetID, segment)`（copy 行 + jobs 幂等键 segment:target），手动/立即/定时三路共用，防漂移。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0016_archive_policies.sql` | `archive_policies`（PK=channel_id）+ `archive_schedule_runs` UNIQUE |
| 归档 | `internal/archive/policy.go` | 策略 List/Upsert（admin）、`EnqueueForPublication(tx,…)`（immediate）、`SweepSchedules(ctx)`（定时，站点时区）、入队内核抽取 |
| recording | `service.go`+`publish.go` | `OnPublished` 钩子字段 + 发布事务内调用（只记日志） |
| HTTP/契约 | `httpapi/archive.go`、m1b + api:generate | `GET/PUT /archive-policies` |
| 装配 | `internal/app/run.go` | hook 注入 + `RunScheduleSweeper`（30s） |
| 前端 | 归档目标页加「通道归档策略」表 | 每通道：目标/模式/时刻 + 保存 |
| 测试 | 集成（immediate 发布即归档、scheduled 命中+去重+补扫、manual 不自动、hook 失败不阻断发布）、vitest | 见下 |
