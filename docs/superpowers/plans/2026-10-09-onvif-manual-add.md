# ONVIF 手动添加与码流自动填充（批次 3）

**Goal:** 把「添加摄像头」里预留的 ONVIF 入口转正：操作者只填 IP、ONVIF 端口与凭据，主/子流地址由摄像头自己返回，不再手抄 RTSP 路径；仍走既有的保存 / 测试 / 启用链路。

**Spec:** [PRD](../../PRD.md) CH-06（ONVIF，P1）；CH-09（来源身份与配置版本分离）；CH-10/CH-11（结构化字段，不接受整条 URL）；[M1 控制面设计](../specs/2026-10-03-m1-control-plane-design.md)。

## 用户指示（2026-10-09，权威）

1. **不做局域网发现/扫描**（原批次 3 计划里的「局域网发现」取消）。
2. **按 ONVIF 方式手动添加**：保留「添加方式」的两个 tab，ONVIF 侧由操作者填地址与凭据。
3. **不用手填码流**：主/子流地址由设备返回。

## 范围

**做：** 单台摄像头能力探测（同步、只读）、`POST /api/v1/channels/{id}/onvif/probe`、前端 ONVIF 手动面板与自动填充、契约与测试。

**不做（明确留空，不显示为可启用）：** 局域网发现/扫描；PTZ 方向/变焦/预置位（只在探测结果里**如实报告**是否支持，不给可点控件）；厂商固件/时间/编码批量管理（PRD 归 P2）。

## 关键设计决策

1. **不扫网段**：部署是 Docker 默认桥接网络（compose 由 `internal/config/render.go` 渲染，服务都在 `default` 网），组播 WS-Discovery（UDP 239.255.255.250:3702）到不了物理局域网；要靠组播就得给容器 host 网络，而那会与既有的网络身份断言（worker 固定 IP、recording-hook `172.30.254.2`）冲突。保持"手动添加"，探测只针对操作者指定的那一个地址。
2. **探测只是证据，不是启用**：不创建 `source_revisions`、不改 `current_revision_id`、不改录像策略、不落任何凭据。保存仍然走 `POST /source-revisions` + 测试 + `If-Match`，`connectSource()` 的编排一字未动 —— 不新增绕过测试证明的旁路。
3. **结构化字段，不落整条 URL**：ONVIF 的 `GetStreamUri` 返回的是 `rtsp://user:pass@host:port/path?query`，本项目只接受 `ip / rtsp_port / main_path / sub_path / onvif_port`（CH-10/CH-11）。因此探测结果**拆成结构字段**并把凭据丢弃，响应里不含用户名/密码，也不含带凭据的完整地址。
4. **边界与权限复用同一个内核**：地址校验抽成 `NetworkPolicy.validateTarget`（`SourceConfig.Validate` 改为调用它），探测与保存共用同一套 `ONE_NVR_CAMERA_CIDRS` 边界与同一批错误码（`camera_network_not_configured` / `camera_address_denied` / `source_config_invalid`）；权限用通道 `Configure`（与保存修订一致），错误码沿用 `forbidden` / `not_found`。
5. **不跟随设备给的任何地址**：设备可以在 `GetCapabilities` 里报任意 Media `XAddr`，也可以在流地址里写别的主机。两者都只取路径，主机与端口一律用**探测目标**，否则一台摄像头就能把这个服务指向网络里的别处。
6. **先校准设备时钟再算摘要**：ONVIF 的 WS-UsernameToken 摘要是 `Base64(SHA1(nonce + Created + password))`，`Created` 超出设备容忍窗口就会被拒 —— 这是 ONVIF 最常见的失败。客户端先用**匿名**的 `GetSystemDateAndTime` 取设备 UTC 时间，再用该时钟签发所有认证请求。
7. **失败分两类**：操作者能改的（地址不在允许网段、不是 ONVIF 设备、认证被拒、设备没有可用码流）→ `422`；操作者改不了的（不可达、响应无法解析、超时）→ `503`（可重试）。不新造状态码，落在契约既有集合内。
8. **审计不含凭据**：`onvif.probed` 只记 `{ip, onvif_port, profiles, result}`；`result` 复用唯一的那处失败映射，保证审计码与 HTTP 码永不打架。边界/输入被拒时**不写审计**（没有接触摄像头，不是摄像头事件）。

## 改动清单

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 协议 | 新增 `internal/onvif/{onvif,soap,client}.go` | SOAP 1.2 报文构造与解析（按**局部名**解析，兼容各家前缀）、WS-UsernameToken 摘要、匿名时钟、`GetDeviceInformation` / `GetCapabilities` / `GetProfiles` / `GetStreamUri`；无代理、禁重定向、限时、限读 512KiB |
| 服务 | 新增 `internal/channel/source_onvif.go` | `ProbeONVIF`：`Configure` 鉴权 + 边界校验（各自独立事务，**不跨网络调用持连接**）→ 5s 预算内探测 → 过滤不可用码流 → 审计 → 映射错误 |
| 服务 | 改 `internal/channel/address.go` | 抽出 `validateTarget` 作为唯一地址边界内核，`SourceConfig.Validate` 改用 |
| HTTP | 新增 `internal/httpapi/channels_source.go` 内 `probeChannelONVIF`；改 `router.go` | `POST /api/v1/channels/{id}/onvif/probe`，每人 20 次/分钟限流 |
| 契约 | 改 `docs/api/m1b.openapi.yaml`；重生成 `apps/web/src/lib/api-types.ts` | 新端点 + `OnvifProbeInput` / `OnvifProbe` / `OnvifDevice` / `OnvifStream` |
| 前端 | 新增 `apps/web/src/features/channels/source-onvif.tsx`；改 `source-form.tsx` | 「添加方式：RTSP 手动 / ONVIF 手动」两个 tab 转正；ONVIF 模式填 IP + ONVIF 端口 + 凭据 → 「获取码流」→ 显示设备型号/固件/PTZ 能力 + 主/子流下拉（默认取最大分辨率为主流、次之为子流）→ 自动填入 RTSP 端口与路径（可改）；探测成功前保存按钮禁用；已保存密码不用于探测时给出提示 |
| 测试 | 新增 `internal/onvif/client_test.go`、`tests/integration/source_onvif_test.go`；改 `source-form.test.tsx` | 见下 |

## 测试覆盖

- **协议层单测**（假设备 httptest）：结构字段拆分（带 query 的路径、丢弃凭据与外部主机）、`Created` 落在**设备时钟**±30s 内且摘要与 WS-Security 定义逐字节相符（用它抓摘要拼接顺序错误）、跨主机 Media `XAddr` 不被跟随（断言请求路径）、认证拒绝/不可达/非 ONVIF 服务/无可用码流分别映射到对应哨兵错误、需要认证的设备给凭据能过、不需要认证的设备不给凭据也能过。
- **集成测试**（本机 PostgreSQL）：越界/回环/网络号/广播/链路本地/非地址 → `422` 与对应错误码；无 `ONE_NVR_CAMERA_CIDRS` → `camera_network_not_configured`；端口越界与带控制字符的用户名 → `invalid_input`；**被拒时不写审计**；只有 Playback 的操作者 → `forbidden`，不可见通道 → `not_found`；对保留文档网段地址的失败探测**写了 `onvif.probed`**，条目里有 IP 与结果码、**没有用户名密码**。
- **前端单测**：ONVIF 模式探测前禁止保存 → 探测后自动填入主/子流路径与 RTSP 端口、显示设备型号与 PTZ 能力、请求体带 IP/端口/凭据；探测被拒时显示原因且仍禁止保存；已有 `onvif_port` 在普通编辑中保留。

## 未验证 / 未做

- **真机 ONVIF 摄像头未验证**（本机与 CI 都没有摄像头）：现场要确认的项——ONVIF 端口（常见 80 / 8000）、摘要认证是否通过、profile 命名与主/子流选择是否符合预期、路径是否与实际拉流一致。
- 局域网发现、PTZ 控制、厂商固件/时间/编码批量管理未做。
