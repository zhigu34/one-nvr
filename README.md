# one-nvr

面向单站点本地部署的 NVR 项目，以固定通道管理视频源。ZLMediaKit 负责取流与正式录制，Frigate 负责智能检测，one-nvr 负责录像与事件管理。

当前提供 **M0 真机验证部署包**：单个 Docker Compose、`.env` 完整 RTSP URL 配置、ZLM MP4 分片录制、Frigate 人车检测、SQLite 索引和 HTTPS 测试页面。正式控制面板、云归档、自动清理和完整可靠性指标尚未实现，产品目标见 [PRD](docs/PRD.md)。

正式后端已确定为 **Go + PostgreSQL**，前端直接基于 shadcn-admin 修改。M1 的账号权限、固定通道、批量导入、目录存储池、换源和实时预览设计见 [M1 控制面设计](docs/superpowers/specs/2026-10-03-m1-control-plane-design.md)；当前处于设计评审，尚未交付正式版代码。

正式版采用模块化部署：基础看/录计划运行 5 个核心容器；`.env` 中 `ONE_NVR_FRIGATE_ENABLE=yes/no` 和 `ONE_NVR_OPENLIST_ENABLE=yes/no` 分别控制智能检测与云归档，默认关闭。部署脚本按启用模块启动/检查服务，面板显示未启用、未配置或运行异常；这些开关尚未实现在现有 M0 实验包中。

## 部署

需要 Linux x86_64、Docker Engine 和 Docker Compose v2；宿主无需 Python 或 OpenSSL。

```bash
git clone https://github.com/zhigu34/one-nvr.git
cd one-nvr/deploy/m0
cp .env.example .env
chmod 600 .env
```

编辑 `.env`：

```dotenv
M0_MEDIA_HOST=192.168.1.10
COMPOSE_PROFILES=cpu
M0_STATE_DIR=./state
M0_STORAGE_ROOT=/mnt/recordings
M0_HTTPS_PORT=8443

camera1='rtsp://admin:password@192.168.1.101:554/main'
camera1_sub='rtsp://admin:password@192.168.1.101:554/sub'
camera1_name='入口'
camera2='rtsp://admin:password@192.168.1.102:554/main'
```

- `M0_MEDIA_HOST` 填本台服务器实际 IP，浏览器需要能够访问。
- EPYC 7402 使用 `COMPOSE_PROFILES=cpu`；N5105 使用 `intel`，并填写实际 `M0_GPU_DEVICE`。
- `M0_STORAGE_ROOT` 填宿主已挂载的录像目录；项目不管理底层 NAS/RAID。
- `camera1` 对应 CH01，支持到 `camera32`；子流和名称可省略。
- URL 使用英文单引号；账号/密码中的保留字符先 URL 编码。两台机器各自配置 `.env`，运行数据独立。

网络受限且基础镜像已在本地时，推荐通过脚本构建并启动，自动生成配置、密钥和测试证书：

```bash
./deploy.sh
docker compose ps -a
```

访问 `https://服务器IP:8443`，账号 `admin`。默认需放行 TCP 8443、UDP/TCP 8000；首次访问使用自签证书。查看随机密码：

```bash
docker compose run --rm --no-deps --entrypoint cat init \
  /workspace/state/operator.txt
```

`init` 和 `mqtt-init` 成功后退出是正常状态。仍使用一个 Compose 和 `.env`；`deploy.sh` 只协调 Docker 命令，不要求宿主 Python/OpenSSL，也不会执行 `.env` 中的内容。它检查本地 `python:3.12-slim` 与 Engine 架构，选用当前 context 的 Docker 驱动构建器，并关闭 Bake 自动分派；共享工具镜像只构建一次。启动采用 `--pull never`，上游镜像须已在本地且符合 Compose 固定的 digest；缺少时先通过可用网络 `docker compose pull mqtt-init mqtt zlm gateway frigate`（Intel 将 frigate 换 frigate-intel）或在另一机器 `docker save` 后本机 `docker load` 准备镜像。

首次构建仍需安装依赖。Compose 默认使用清华 Debian/PyPI 镜像，可在 `.env` 修改 `DEBIAN_MIRROR`、`DEBIAN_SECURITY_MIRROR`、`PYPI_INDEX_URL`，旧 `.env` 不追加也会使用这些默认值。镜像失败会尝试原始 Debian 源或官方 PyPI，仍保留 TLS 验证和依赖 hash 校验。Dockerfile 单独构建默认使用官方安装源。Docker Hub 基础镜像鉴权/拉取与 apt/pip 是不同阶段，换软件包安装源不能替代镜像准备或修复不可达的 Docker Hub。

详细硬件、故障实验、目录和未实现功能说明见 [M0 部署说明](deploy/m0/README.md) 与 [真机验证清单](docs/M0-validation.md)。两款机器各两路摄像头的取流、录像与浏览器播放，以及 N5105 的事件/抓拍已通过限定范围验证；AMD 检测、实际硬件执行、长期稳定性、故障恢复和 16/32 路容量仍未完成认证。

## 更新

从 `deploy/m0` 执行，先停止服务，再拉取代码和重建：

```bash
docker compose down
git pull --ff-only
./deploy.sh
```

不要覆盖已有 `.env`；按更新说明补充参数。停止或重建不会自动删除宿主录像与索引，也不会覆盖已有密钥。更新会中断本实验所有通道。

`.env`、摄像头实际 CSV、`state`、录像目录和本地发布压缩包不提交 Git。分享日志前脱敏，避免公开带密码的 RTSP URL 或 `docker compose config` 输出。

## 开发验证

```bash
python3 -m unittest discover -s deploy/m0/tests -v
```

测试覆盖 URL/CSV 解析、初始化、来源隔离、录像索引、事件时序、路径访问约束和本机 HTTP 接口，不替代实际 Docker/硬件验证。
