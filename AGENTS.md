# one-nvr Agent Handoff

这份文件用于快速接手代码任务。先看本文件，再按改动范围阅读 PRD、决策记录和对应模块；README 与部署文档是操作说明的主要来源。

## 产品状态

- 项目面向单站点、本地部署、16–32 个固定通道。Go + PostgreSQL 管理配置、任务和录像索引；ZLMediaKit（ZLM）取流、录像；Web 前端基于 shadcn-admin。
- Frigate 与 OpenList 是可选模块。README 明确标出的未实现功能（例如录像内容回放、智能事件业务、云归档业务）仍按未实现处理，不能从容器存在推断业务可用。
- 通道号和通道身份固定。更换摄像头修改来源修订，不应丢失通道权限、录像策略或历史录像。

## 项目地图

- `cmd/`、`internal/`、`migrations/`：Go 服务、业务模块和数据库迁移。
- `apps/web/`：React/TypeScript 管理界面、API 类型与浏览器单测。
- `compose.yaml`、`deploy.sh`：根目录正式部署入口。
- `deploy/production/`：生产镜像、硬件探测、CI/隔离验收脚本和开发入口 `dev.sh`。
- `tests/`：数据库集成、媒体和真实浏览器端到端验收。
- `docs/PRD.md`：产品范围；`docs/M1-B-rulings.md` 与 `docs/M1-B-decisions.md`：已裁定的行为及验收边界；`docs/M1-B-validation.md` 与专题验证文档：实际验证证据。

## 开发约定

- 改功能前先核对相关 ruling/decision，保留已有安全边界、权限校验、审计和并发版本检查。通道配置变更应沿用现有 source revision、测试证明和 `If-Match` 流程；不要另造旁路直接启用媒体源。
- 改 API 时同时检查 Go handler/service、OpenAPI 契约、迁移和 Web 生成类型。API 类型生成脚本位于 `apps/web/scripts/generate-api.mjs`。
- Web 开发使用 `deploy/production/dev.sh` 提供的项目命令；根目录部署使用 `./deploy.sh`，不要以裸 `docker compose up` 代替部署初始化流程。
- 镜像和系统依赖按 `docs/image-versions.md` 锁定。修改版本、网络、权限或存储行为时，同步更新部署文档与对应验收。
- 摄像头密码、`.env`、私有运行目录和现场录像都是敏感数据。不得把它们的值复制进日志、测试 fixture、截图、提交或 handoff；使用虚构 fixture。
- 测试必须使用隔离数据库、Compose 项目和合成摄像头，不能指向用户的 NAS 数据或真实录像。Docker/真实媒体验收优先走 GitHub Actions；单元测试或 mock UI 不能替代端到端媒体验收。

## 开发流程

1. **接手任务**：查看 `git status`、当前分支和最近提交，保留已有改动；阅读用户最新要求、相关 PRD/裁定及被改模块，确认本次验收结果。
2. **定范围**：找到当前实现、API/数据流和现有测试，列清需改变的行为及不能回归的约束。涉及多个层时，标出 Go/API、迁移、Web、部署和文档的对应改动。
3. **实现并覆盖回归**：先写能复现问题的测试，再做最小实现；沿用现有权限、审计、请求幂等键、版本条件和回滚路径。不要只改 UI 文案掩盖后端状态问题。
4. **验证**：先运行受影响模块的定向测试，再运行本文件“常用验证”里适用的检查。改取流、录像或部署时，等对应真实容器 CI job 完成；失败就查看具体 job 日志，修复后重跑相关检查。
5. **交付**：检查完整 diff、敏感信息和 `git status`，同步必要的决策/操作文档；报告改了什么、实际通过了哪些命令或 CI、还有哪些现场项没验。只在用户当前或既有授权覆盖时推送/更新 PR；不擅自合并或操作用户的生产站点。

## 常用验证

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

按改动选择必要检查；媒体、录像连续性、证书或部署入口变更还要查看 `.github/workflows/foundation-ci.yml` 中对应真实容器验收 job。报告通过情况时写明具体命令、CI run/commit 和测试范围；没有跑过的现场设备或容量测试要明确标作未验证。

## 当前交接快照（2026-10-07）

- 当前主题：M1-C 历史录像回放。M1-B 的固定通道接入、录像与索引留在 `codex/m1b-channel-recording`（草稿 PR [#2](https://github.com/zhigu34/one-nvr/pull/2)）。
- M1-C 已完成读路径与回放工作区：`GET /api/v1/recordings/{id}/content` 由 501 变为受授权的单区间 Range 交付——授权与解析同事务、五类失败语义（404/409/503）、只服务单区间、审计先于第一个媒体字节；前端新增 `/recordings` 回放工作区（时间轴、缺口提示、±10 秒、0.5/1/2/4 倍速、跨分片续播、即时回放入口），侧边栏新增「录像回放」并改为声明式 `everyRole` 可见性。决策与边界见 `docs/M1-C-decisions.md`，验证矩阵待 `docs/M1-C-validation.md`。
- 本地开发分支 `codex/m1c-playback`，提交 `ec7ffd6`（发布到 GitHub 对应提交 `f86cf7e`，远端分支 `codex/m1c-playback`；跟踪远端用本地分支 `codex/m1c-ci`）。
- CI run [37655395677](https://github.com/zhigu34/one-nvr/actions/runs/37655395677)：接手时先看它的最终结论，不要假定通过。
- 未完成：M1-C 的真实容器与浏览器验收（206 与 `Content-Range` 断言、浏览器实际解码帧增长、网关是否原样透传 `Range`/206）与 `docs/M1-C-validation.md`；两台 NAS 上的真机部署验证仍需用户执行，CI 通过不代表现场完成。
- 本地没有可用的 Docker daemon，也没有真实摄像头：真实容器与媒体验收只能在 GitHub CI 运行。单元测试和 mock 过的 UI 不能替代端到端媒体验收。
- 早前记录的“保存并连接”一键接入（提交 `0e55fb8` / 发布 `b59e613`，CI run `37596104311` 11 个 job 全绿）仍是 M1-B 的现场验证入口，未被本轮改动取代。
