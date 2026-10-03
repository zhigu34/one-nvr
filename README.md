# one-nvr

面向单站点本地部署的 NVR 项目，以固定通道管理视频源。ZLMediaKit 负责取流与正式录制，Frigate 负责智能检测，one-nvr 负责录像与事件管理。

当前提供 **M0 真机验证部署包**：单个 Docker Compose、`.env` 完整 RTSP URL 配置、ZLM MP4 分片录制、Frigate 人车检测、SQLite 索引和 HTTPS 测试页面。正式控制面板、云归档、自动清理和完整可靠性指标尚未实现，产品目标见 [PRD](docs/PRD.md)。

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

启动时自动生成配置、密钥和测试证书：

```bash
docker compose up -d --build
docker compose ps -a
```

访问 `https://服务器IP:8443`，账号 `admin`。默认需放行 TCP 8443、UDP/TCP 8000；首次访问使用自签证书。查看随机密码：

```bash
docker compose run --rm --no-deps --entrypoint cat init \
  /workspace/state/operator.txt
```

`init` 和 `mqtt-init` 成功后退出是正常状态。工具基础镜像使用 `python:3.12-slim`；首次构建仍需下载系统及 Python 依赖。详细硬件、故障实验、目录和未实现功能说明见 [M0 部署说明](deploy/m0/README.md) 与 [真机验证清单](docs/M0-validation.md)。镜像实际启动、GPU 与摄像头端到端链路仍待真机验证。

## 更新

从 `deploy/m0` 执行，先停止服务，再拉取代码和重建：

```bash
docker compose down
git pull --ff-only
docker compose up -d --build
```

不要覆盖已有 `.env`；按更新说明补充参数。停止或重建不会自动删除宿主录像与索引，也不会覆盖已有密钥。更新会中断本实验所有通道。

`.env`、摄像头实际 CSV、`state`、录像目录和本地发布压缩包不提交 Git。分享日志前脱敏，避免公开带密码的 RTSP URL 或 `docker compose config` 输出。

## 开发验证

```bash
python3 -m unittest discover -s deploy/m0/tests -v
```

测试覆盖 URL/CSV 解析、初始化、来源隔离、录像索引、事件时序、路径访问约束和本机 HTTP 接口，不替代实际 Docker/硬件验证。
