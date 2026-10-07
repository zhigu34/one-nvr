# one-nvr

面向单站点、16–32 个固定通道的本地 NVR。Go + PostgreSQL 管理通道、录像索引和任务，ZLMediaKit 负责取流与录像，控制面基于 shadcn-admin。Frigate 和 OpenList 为可选模块，按 `.env` 开关启停。

当前 M1-B 可部署测试：站点初始化、账号权限、固定通道、摄像头配置/测试/替换、批量导入导出、目录存储池、连续录像或仅取流、录像索引、时区与证书管理。已增加 WebRTC 实时预览（1/4/9/16 分屏、主子流切换），容器出画面验收记录见实时预览验证文档。录像内容回放待后续实现；智能事件接入、云归档业务仍待开发。开启可选容器不代表这些业务已经完成。

M0 前期验证代码已移除，历史版本可从 Git 恢复。现场旧容器和录像数据不会随代码更新自动删除。

## 部署

需要 Linux amd64、Docker Engine 和 Docker Compose v2.20.0 或以上；宿主无需安装 Go、Node 或 Python。在项目根目录执行：

```bash
cp -n .env.example .env
# 编辑 .env：站点 URL、Web/RTC 端口、数据目录、存储根目录和摄像头网段。
# 创建实际存储池目录，授予容器 UID 10001 读写权限。
mkdir -p /srv/one-nvr-storage/disk1
chown 10001:10001 /srv/one-nvr-storage/disk1
./deploy.sh --check
./deploy.sh
```

`ONE_NVR_DATA_DIR` 是宿主机专用应用数据目录。例如 NAS 上可以填 `/vol1/1000/docker/one-nvr/data`，该目录首次可以不存在。`deploy.sh` 自动创建目录、生成内部密钥并初始化数据库和运行配置；不需要手工创建密钥文件或填写内部密码。目录必须沿用并保留，不能填写项目根目录。`ONE_NVR_STORAGE_ROOT` 则是录像存储池所在的已有根目录，两者用途不同。

首次构建 app/gateway，其他镜像固定 digest。脚本打印一次性初始化令牌获取命令；打开 `.env` 的 `ONE_NVR_PUBLIC_URL` 初始化管理员和 16/32 个通道。`.env` 不填写摄像头密码，摄像头在通道界面配置，批量导入字段如下：

```csv
channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path
```

路径直接填 `/main`、`/sub`，可追加 `onvif_port`。存储池登记已挂载目录，例如 `/storage/disk1`；不管理底层 NAS、RAID 或磁盘挂载。

基础常驻 5 个容器：gateway（含前端）、api、worker、postgres、zlm。Frigate 开关增加 frigate/mqtt，OpenList 开关增加 openlist；全部启用为 8 个。默认两模块关闭。

根目录 `compose.yaml` 是正式运行入口，加载数据目录内自动生成的私有配置和硬件覆盖。内部凭据不写入仓库。日常操作使用脚本加载完整配置：

```bash
./deploy.sh compose ps
./deploy.sh compose logs --tail=100 api worker zlm
./deploy.sh compose restart api worker
# 修改 .env 或更新源码后重新部署：
./deploy.sh --check
./deploy.sh
```

不要直接 `docker compose up` 绕过迁移、初始化和硬件探测。已有正式部署可把 `deploy/production/.env` 复制为根目录 `.env`，保留原数据目录与项目名；M0 的配置不能直接复用。切换前停止原 M0 服务，保留其配置和录像作为备份。

[完整部署说明](deploy/production/README.md) 包含证书续签目录、硬件自动探测、模块状态、升级及故障处理。

## 项目目录

```text
.env.example       部署配置模板
deploy.sh          部署、检查和 Compose 运维入口
compose.yaml       正式 Compose 入口
apps/web/          shadcn-admin 前端
cmd/               Go API、Worker 和管理命令
internal/          后端业务模块
migrations/        PostgreSQL 迁移
deploy/production/ 镜像构建、配置模板、硬件探测和 CI 验收工具
tests/             集成、媒体与浏览器测试
docs/              PRD、部署约定和验证报告
```

## 开发与验证

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

Docker 与浏览器验收由 GitHub CI 执行，包括真实 PostgreSQL、ZLM 两路合成源、连续录像发布、进程/数据库故障及证书恢复。每机两路真机验证与 CI 不代表已经通过 16–32 路容量或长期稳定性验证。

- [PRD](docs/PRD.md)
- [镜像版本](docs/image-versions.md)
- [录像目录与文件名](docs/recording-file-layout.md)
- [实时预览验证](docs/live-preview-validation.md)
- [M1-B 验证记录](docs/M1-B-validation.md)
- [M1-B 验收矩阵](docs/M1-B-decisions.md)
- [前端许可证](apps/web/LICENSE)
