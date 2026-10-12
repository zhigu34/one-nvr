# M3 智能与事件批次 1：事件接收链路与事件中心

**Goal:** 打通「Frigate 经 MQTT 发布目标事件 → one-nvr 归一入库 → 事件中心展示/处理 → 抓拍可看 → 90 天到期清理」的闭环，并满足来源归属与媒体状态如实展示。MQTT 客户端自实现（零新依赖，照 operations/probe.go 的 3.1.1 帧格式扩展到 SUBSCRIBE/PUBLISH 解析）。

## 范围（EVT-01/02/03/07 主体，CH-07 的事件侧，PLAY-04 的跳转数据前提）

1. **MQTT 订阅与归一**（新 `internal/events` 包）：worker 在 `FrigateEnabled` 时连接 `mqtt:1883`（组件凭据），订阅 `frigate/events`（Frigate 0.17 固定主题前缀 `frigate/`）；JSON payload 归一为事件行：上游 id、通道映射（Frigate camera 名 = one-nvr 通道的 `<vhost>/<app>/<stream>` 里的 stream 还是显式配置？——用**显式映射表** `frigate_cameras(channel_id PK, frigate_name)`，由账号配置，避免猜测）、目标类别、分数、区域历史、开始/结束、抓拍路径、原始类型。
2. **幂等与去重**（AI-05 的落地侧）：上游事件 id 唯一（`UNIQUE(source,source_id)`）；「new」消息插入，「update」补全结束时间/最佳抓拍/分数（只前进不后退：ended 不因迟到 new 回滚）；同一 id 一次逻辑事件。MQTT 断线重连（指数退避），离线期间事件缺口如实记 `event_stream_gaps`（`EVT-04` 的检测积压雏形）。
3. **事件模型**（迁移 `0018`）：`events`（id、channel_id、source_revision_id 可空、source、source_id、category、score、zones jsonb、rule_hits 空置待批次 2、started_at、ended_at、snapshot_relative_path、media_state、track_state、handle_state、handle_note、version、created_at、expires_at=ended_at+90d）；`frigate_cameras` 映射表。
4. **抓拍交付**：Frigate 抓拍在 `frigate` 容器的 `/media/frigate/clips/`。批次 1 用**反向代理读取**（worker 经共享卷不可达）；改走 Frigate HTTP API `/api/events/<id>/snapshot.jpg`（`http://frigate:5000`，凭据在 Frigate 配置内，同网默认放行）——`GET /events/{id}/snapshot` 代理转发，未授权 404，`media_state` 如实（pending/ready/gone）。
5. **处理流程**（EVT-03）：`PATCH /events/{id}/handle`（确认/关闭/误报/重开，备注，If-Match version）；操作者/时间/备注进事件处理记录表 `event_handling`。
6. **事件中心页**：筛选（时间/通道/类别/级别=无、处理状态、源版本）、分页列表 + 抓拍卡片视图；详情抽屉（时间、分数、区域、抓拍、录像区间跳转、处理记录）；「查看事件录像」深链进回放工作区（PLAY-04：跳到事件窗口）。
7. **90 天清理**（EVT-07）：worker 每小时删到期普通结束事件；受保护/活动/有任务引用跳过并展示例外；只清事件索引，不级联录像/云对象/审计。

## 明确不做（批次 2/3）

检测配置/区域编辑/规则（AI-01..07）、通知与 Webhook（EVT-05）、异常事件（EVT-04 的完整集合）、事件录像前录缓冲（P1）、智能回放跳播（PLAY-07 P1）、人脸/车牌（P2）。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 迁移 | `0018_events.sql` | events + event_handling + frigate_cameras + event_stream_gaps |
| 包 | 新增 `internal/events/{client,service,types}.go` | MQTT 3.1.1 SUBSCRIBE 客户端（重连/心跳/PUBLISH 解析）、归一、状态机 |
| 装配 | run.go | worker：FrigateEnabled 时起订阅循环 + 清理循环；Dependencies 注入 events 服务 |
| HTTP/契约 | 新增 `internal/httpapi/events.go`；router；m1b + api:generate | list/get/snapshot 代理/handle/mappings |
| 前端 | 新增 `features/events/`；sidebar「事件」；回放入口 | 列表+卡片+详情抽屉+处理+跳转回放 |
| 测试 | MQTT 客户端单测（假 broker 帧往返）、集成（归一/幂等/去重/迟到消息/过期清理/处理版本）、vitest、e2e 可选 | 见下 |
