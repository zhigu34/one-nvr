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

## 常用验证

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

按改动选择必要检查；媒体、录像连续性、证书或部署入口变更还要查看 `.github/workflows/foundation-ci.yml` 中对应真实容器验收 job。报告通过情况时写明具体命令、CI run/commit 和测试范围；没有跑过的现场设备或容量测试要明确标作未验证。

## 当前交接快照（2026-10-07）

- 当前主题：简化摄像头接入。用户反馈原界面暴露了太多内部步骤，普通添加难用。
- 已实现“保存并连接”：自动保存、取流测试、校验有效主流画面并应用；首次接入默认只预览，不要求先建录像池。已有连续录像的通道会自动检查存储池并沿用录像策略。失败可保留表单与凭据重试。手工分步测试和历史配置应用放在高级诊断内。
- 仓库提交 `0e55fb8`（发布到 GitHub 对应提交 `b59e613`），分支 `codex/m1b-channel-recording`，草稿 PR [#2](https://github.com/zhigu34/one-nvr/pull/2)。先核对远端 HEAD 与 PR 状态；这只是接手快照。
- CI run [37596104311](https://github.com/zhigu34/one-nvr/actions/runs/37596104311)：前端、网关、浏览器、合成媒体和真实媒体浏览器验收已通过；最后一次查看时 PostgreSQL 集成与双通道联合故障验收仍在运行。接手时重新检查状态，尤其确认 `media-browser-contract` receipt 包含 `camera_one_click_connect` 和 `camera_one_click_preserves_recording`。
- 本地没有可用 Docker daemon；真实 Docker/摄像头验收通过 GitHub CI 运行。两台 NAS 尚需用户部署新镜像后做现场验证；CI 成功不能代表这一步已完成。
