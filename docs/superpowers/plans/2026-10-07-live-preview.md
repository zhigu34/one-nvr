# 实时预览 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan in the current session, task by task, followed by one independent review.

**Goal:** 在现有固定通道上交付真实 ZLM WebRTC 预览，关闭录像时仍能看。

**Architecture:** 沿用 PRD 6.3；浏览器仅访问同源授权 API，Go 选择当前实际流并代理 WHEP SDP 交换。短期播放租约绑定用户会话、通道与物理 stream_session；Worker 的 on_play 校验租约，监测器关闭过期、登出、权限撤销或换源后的 RTC 连接。浏览器不获取摄像头或 ZLM 管理密钥。

**Tech Stack:** Go、PostgreSQL、ZLM 固定镜像、React/shadcn-admin、原生 RTCPeerConnection。

**Spec:** docs/PRD.md §6.3、§5 浏览器编码；本次实现 LIVE-01/02/03 及通道配置快捷入口。录像播放、事件与手动录像由相应业务后续交付，预览不显示无效按钮。

## Global Constraints

- 1/4/9/16 分屏，32 路分组访问；子流优先，无子流明确提示主流降级。
- 权限以 live 为准，不以 configure/playback 替代；信令、租约刷新均检查会话和通道。
- SDP/播放票据不写共享缓存、本地存储或日志；公开服务不反代任意 ZLM API。
- 租约 30 秒，前端 10 秒刷新，Worker 每 2 秒回收；权限撤销、登出、失效连接须主动断开。
- 自动重连最多 3 次，1/2/4 秒退避；不支持编码不自动重连。关闭、换页、后台标签释放浏览器与服务端连接。
- 声音默认关闭，只对当前选中画面开启。视图保存只存用户的通道 ID、分屏、顺序，载入时重新过滤权限。

## Review Focus

- 协商中关闭页面/撤销权限：迟到的媒体连接也必须关闭。
- 票据跨通道、跨物理流或进入探测流：Hook 必须拒绝。
- 选中通道换源、子流不存在或离线：不得继续播放旧源或伪装正常。
- 直接媒体端口未授权访问、ZLM Location 重定向：保持私有边界。
- 真实浏览器只收到 SDP 而无帧：不得显示播放成功；CI 必须检查递增的实际解码帧。

### Task 1: 授权 WHEP 边界与租约

Files: internal/live/service.go、internal/live/monitor.go、internal/media/zlm/webrtc.go、internal/httpapi/live.go、migrations/0010_live_sessions.sql、internal/recording/hook.go、internal/app/run.go、docs/api/m1b.openapi.yaml（扩展现有合同）。

Interfaces: zlm.Negotiate(ctx,key,offer,ticket) -> answer + private peer; CloseRTC(ctx,peer)。live.Service.Open(ctx,principal,channel,kind,offer)、Renew、Close、AuthorizePlay、Reap。

- [x] 先写 ZLM HTTP 契约和 PG/HTTP 权限、票据串流、撤销、清理测试，确认 RED。
- [x] 只允许选定已有流，WHEP Location 只解析固定删除路径及 id/token，所有错误脱敏。
- [x] 协商前登记票据；协商成功持久化 peer，再复核权限和源；失败/迟到保留可回收连接。
- [x] Hook 保留原有 probe_token 检查，额外允许匹配当前有效播放租约；Worker 独立回收不停止摄像头代理或录像。
- [x] 更新 API 合同与生成类型，跑 Go race/vet 和 PG 集成。

### Task 2: 实际预览页面

Files: apps/web/src/features/live/*、apps/web/src/routes/_authenticated/live.tsx、sidebar-data.ts、channels/index.tsx。

- [x] 先写浏览器测试：选择与分页、视图权限过滤、停止/换流/失败连接释放、后台暂停、有限重连，确认 RED。
- [x] 原生接收 video/audio transceiver，等待 ICE gathering 后交换 SDP；收到实际 video playing/解码帧才显示播放中。
- [x] 分屏、选中通道、主子流、全屏、单画面音频、拖拽换位与用户级视图；未授权和未配置显示真实状态。
- [x] 97 项既有前端测试与新测试、类型检查、lint、生产构建。

### Task 3: 真实媒体浏览器验收与发布

Files: tests/e2e/m1b-media.spec.ts、tests/integration/gateway_media_e2e_test.go、相关隔离 Compose/egress 配置、部署说明。

- [ ] 隔离浏览器实际连接 ZLM 地址，验证 decoded frames 增长；验证关闭录像仍能播放、主子流切换、4 画面及释放。
- [ ] 验证未授权、登出后的服务端关闭；既有媒体录制、换源回滚与恢复检查继续执行。
- [ ] 独立审查后修复关键问题，推送当前开发分支；完成 CI 并准确记录真机边界。
