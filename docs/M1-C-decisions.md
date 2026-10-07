# M1-C 历史回放：范围与实现决策

M1-C 在 M1-B 已发布的录像索引之上交付受授权的历史回放：按通道与时间检索、单段 Range 播放、跨分片续播、跳转与倍速、即时回放入口。本文件记录已落实的实现决策与验收边界；证据矩阵在批次完成后写入 `M1-C-validation.md`。

## 读路径（B1/B2）已落实决策

- **授权与解析同一事务**。`recording.Service.OpenSegment` 在一个事务里先取 `channel_id`、再走 `auth.RequireChannelTx(..., auth.Playback)`、再读出发布行；原独立的 `AuthorizeSegment` 已删除，避免出现第二条会漂移的授权路径。
- **未授权、不存在、跨通道一律 404**，且错误体与`not_found`完全一致；不按耗时或状态码区分，也不写媒体审计（未授权请求不算一次媒体读取）。
- **五类失败各有明确语义**，不允许回落到静态文件路径：

  | 情形 | 状态 | 错误码 |
  |---|---|---|
  | 无权限 / 通道不在授权范围 / segment 不存在 | 404 | `not_found` |
  | `recording_segments.state <> 'ready'` | 409 | `recording_not_ready` |
  | 目标文件被移走 | 409 | `recording_missing` |
  | 文件存在但字节身份（dev/inode/size/mtime）与发布记录不一致 | 409 | `recording_damaged` |
  | 存储池不可用（池行缺失、标记校验失败） | 503 | `pool_unavailable` |

- **只有 `ready` 且身份校验通过才送字节**。任何非 200/206 响应都不含媒体字节，也不带 `Content-Range`。
- **Range 只有一个实现**：`recording.Content.PlanRange` 解析 + `io.NewSectionReader` 精确送 `[start,end]`。
  - 未用 `http.ServeContent`：它会给多区间拼 `multipart/byteranges`、416 回 `text/plain`，并且状态码由它自己决定 —— 与「只服务单区间、416 走 JSON 错误信封、审计必须先于第一个媒体字节落库」三条同时冲突。
  - 畸形 Range（`bytes=abc`、`bytes=19-10`、非 `bytes` 单位）按 RFC 7233 **忽略**并回 200 全量；只有「语法合法但本 API 不服务」（多区间、`bytes=-0`、起点 ≥ 长度）才 416，且带 `Content-Range: bytes */<size>`。
  - `If-Range` 采用强校验器（`ETag` 由 segment id + 字节数 + mtime 派生）；不匹配或过期时回到 200 全量；同时接受 HTTP-date 形式。
  - 响应固定带 `Accept-Ranges: bytes`、`Content-Type: video/mp4`、`Cache-Control: private, no-store`。
- **审计先于字节**：每次成功媒体响应前写一条 `recording.streamed`（actor、segment、通道、字节窗口、是否区间），审计不可持久化则回 503 且零媒体字节，沿用查看/导出凭据的同一条规则。
  - 粒度为每请求一行，换取「谁在什么时候取走了哪些字节」的完整记录；长回放会产生较多行。若实际量级偏高，按 (session, segment) 去重或加留存属于独立批次，不在本批静默降级。
- **不泄漏**：响应头、响应体、日志与审计都不含池根、冻结相对路径、上游通道标识。
- **读路径不写数据库**：播放时发现的 missing/damaged 只在响应中如实报告，状态修复仍由 `List` 单点完成（它本来就会做同一身份校验），避免读路径出现写放大与第二处状态机。
- **HEAD 不可用**：路由只注册 GET，非 GET 一律被既有 CSRF 守卫拒绝（403）。客户端不能用 HEAD 探测区间元数据，`Range` 使用说明因此只面向 GET。

## 与控制面设计说明的两处不一致（已核对，非遗漏）

- 设计说明第 200 行的端点表写「未知/缺失 409，已确定删除 410」。本批按 404/409 实现：未知与越权必须不可区分（沿用 M1-B 的信息隐藏断言），而 M1 明确不自动删除正式录像，`recording_segments.state` 也没有「已删除」取值，因此 410 在 M1 无可信来源，不实现。删除/加锁/保留策略是独立批次。
- 设计说明第 167 行要求「每段开始建立媒体租约，长连接每 10 秒复核授权」。本批未引入租约对象：每个 Range 请求本身就是一次独立的授权 + 解析事务，授权撤销在下一个请求立即生效；分片是短小的完整体，长连接持续复核留待与多路同步回放/SSE 一起评估。

## 尚未验证（批次内待办）

- 前端回放页（时间轴、跳转、倍速、跨分片续播、缺口与损坏提示）属 B3；
- 真实容器 + 真实浏览器解码帧增长的验收、网关是否原样透传 `Range`/`Accept-Ranges`/206 属 B4；
- 16–32 路并发播放的容量结论仍不在承诺范围（与 M1-B 同一立场）。
