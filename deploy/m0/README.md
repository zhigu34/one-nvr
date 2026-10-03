# one-nvr M0 真机部署包

用于部署 [M0 验证清单](../../docs/M0-validation.md) 的初始实验环境。包含 ZLM、Frigate、Mosquitto、SQLite 事件/分片观察器和 HTTPS 测试页；不是完整生产版 one-nvr。Frigate 正式录像关闭，摄像头主/子流由 ZLM 拉取，正式 MP4 只由 ZLM 写入。

## 1. 准备与 .env

- Linux x86_64，Docker Engine、Docker Compose v2。初始化、证书生成和观察器全部在 Docker 中运行，宿主机无需 Python 或 OpenSSL。
- 先接两路真实摄像头，主流建议 H.264 1080p，子流 H.264 640×360、10 FPS；当前检测配置 640×360、5 FPS。
- EPYC 用 CPU 档案；N5105 用 Intel 档案，提前准备驱动、设备权限；虚拟机需要核显直通。
- 浏览器能访问服务器 IP，摄像头可从容器网络访问。默认放行 TCP 8443、UDP/TCP 8000；修改 .env 中端口后对应调整防火墙。
- 上游镜像固定 digest：Frigate 0.17.2 候选、ZLM master 镜像快照、Mosquitto 2.0.22、Nginx 1.28.0。自建工具镜像按用户要求使用 `python:3.12-slim`，可复用本地同名镜像；该标签不锁定 digest。首次构建仍需下载 ffmpeg、OpenSSL 和 Python MQTT 依赖，已有基础镜像不等于可以离线构建。

复制仓库或解压发布包，进入 `deploy/m0`，首次创建配置（已有 .env 不要覆盖）：

```bash
cd deploy/m0
cp .env.example .env
chmod 600 .env
```

只编辑 `.env`，无需 CSV 或初始化命令参数：

```dotenv
M0_MEDIA_HOST=192.168.1.10
COMPOSE_PROFILES=cpu
M0_STATE_DIR=./state
M0_STORAGE_ROOT=/srv/one-nvr/storage
M0_HTTPS_PORT=8443
M0_RTC_PORT=8000
M0_SHM_SIZE=256mb
M0_GPU_DEVICE=/dev/dri/renderD128

camera1='rtsp://admin:你的密码@192.168.1.101:554/main'
camera1_sub='rtsp://admin:你的密码@192.168.1.101:554/sub'
camera1_name='入口'
camera2='rtsp://admin:你的密码@192.168.1.102:554/main'
camera2_sub='rtsp://admin:你的密码@192.168.1.102:554/sub'
camera2_name='走廊'
```

`M0_MEDIA_HOST` 是浏览器可达的服务器 IP，不是摄像头或容器 IP；用于 WebRTC 媒体连接与测试证书。`M0_STORAGE_ROOT` 填已经挂载好的宿主目录，默认 `./storage`；one-nvr 不管理其底层存储。

`camera1` 固定对应 CH01，依次到 `camera32`/CH32；可以留空位，新增/删除其他项不改变既有通道号。`_sub`、`_name` 可省略；没有子流时 Frigate 使用 ZLM 内部主流检测。URL 原样交给 ZLM，不重组凭据、端口或 query；支持 IPv4、带方括号 IPv6 和容器可解析的域名。示例 `/main`、`/sub` 换成摄像头实际路径，如 `/Streaming/Channels/101`。原来的八列 CSV 模板仍保留用于正式产品批量导入设计，当前 Compose 不读取它。

URL 使用英文单引号 `'`，避免 `$` 被 Compose 插值；不要使用中文弯引号。凭据中的 `@`、`#`、`:`、单引号等保留字符先 URL 编码，如 `@` 写 `%40`、`#` 写 `%23`、单引号写 `%27`。不要分享 `.env` 或含凭据的 `docker compose config` 输出；检查格式用 `docker compose config --quiet`。

## 2. 一条命令启动

两种硬件使用同一个 `compose.yaml`，所有参数都来自 `.env`：

```bash
docker compose config --quiet
docker compose up -d --build
docker compose ps -a
```

Compose 自动运行 `init` → MQTT 密码初始化 → 常驻服务。初始化失败时依赖服务不会启动，查看 `docker compose logs init`。生成文件通过目录挂载共享，避免首次启动时尚不存在的配置文件被 Docker 当作目录。成功退出的 `init`、`mqtt-init` 显示 `Exited (0)` 是正常状态。

EPYC：设 `COMPOSE_PROFILES=cpu`，只启动 CPU 版 `frigate`，不挂载 GPU。OpenVINO CPU 推理、软件解码；使用镜像内置 SSD MobileNet 模型验证 person/car 链路。AMD CPU/模型兼容性与负载待实测，不承诺 16–32 路检测能力。

## 3. N5105 核显档案

设 `COMPOSE_PROFILES=intel`，只启动 `frigate-intel`，内部网络别名仍为 `frigate`。只能选 cpu/intel 其中一个，不能同时启用。用 `ls -l /dev/dri` 确认 render 节点，并在 `.env` 的 `M0_GPU_DEVICE` 填实际节点；映射到容器 `/dev/dri/renderD128`。

启动同样只用 `docker compose up -d --build`，无需额外 `-f` 或 `--profile`。该档案用 VAAPI/iHD 解码、OpenVINO GPU 推理；失败时查看 `docker compose logs frigate-intel`，不会自动降级 CPU。`M0_SHM_SIZE=256mb` 仅用于初始两路，缓存 `/tmp/cache` 另有 128MiB 上限；增加路数/分辨率需要实测调整。

## 4. 打开验证页

浏览器访问 `https://服务器IP:8443`。初始化生成局域网测试用自签证书，首次访问检查该证书对应自己的测试服务器后继续。用户名 `admin`，随机密码仅保存在本机文件：

```bash
docker compose run --rm --no-deps --entrypoint cat init /workspace/state/operator.txt
```

测试页可查看组件/处理帧率/推理延迟，按通道与时间检索已完成分片，WebRTC 直播、播放/下载原片、查看人车 MQTT 事件、区域和关联录像/抓拍。事件窗口前 10 秒、后 20 秒按时间相交分片展示，点击原片播放；不是精确裁剪或完整无缝时间轴播放器。

“结构可读”来自 ffprobe 容器/轨道检查，不代表每帧都完整。扫描恢复分片的时间标为 `filename-provisional`，需要与媒体/现场时钟核对，不能作为正式完整性认证。在线表的 `listed` 是 ZLM 流登记状态，媒体进展未知时不能用它推算摄像头可用率。

服务管理端口不向宿主发布：ZLM HTTP/RTSP、Frigate 管理接口、MQTT 都在 Compose 网络内。浏览器通过 HTTPS 网关访问；私有探针 HTTP 端口只供网关和 ZLM 回调使用。无需单独创建生产数据库。

`init` 是由 Compose 自动启动的一次性服务，与观察器共用自建的 `one-nvr-m0-tools:local` 镜像。初始化以容器默认 root 写配置，敏感文件权限为 0600；普通宿主用户读取密码可用上面的 Docker 命令，无需放宽文件权限。再次初始化不会覆盖既有密钥或删除录像。

## 5. 数据与配置

```text
state/
  secrets/             随机服务与登录密钥，重复初始化不覆盖
  operator.txt         本机登录信息
  zlm.ini              ZLM 配置
  frigate/             Frigate 配置、上游数据库和模型缓存
  frigate-media/       Frigate 抓拍等实验媒体，正式录像关闭
  probe/               SQLite、观察器配置和已打开抓拍的本地缓存
  mqtt-data/           MQTT 持久数据
  mqtt-auth/           MQTT 密码哈希
  tls/                 HTTPS 测试证书
storage/
  m0-recordings/       ZLM 正式录像专属命名空间
```

`.env` 控制服务器地址、硬件档案、宿主目录、端口及 M0 摄像头完整 URL，由 `.env.example` 复制后按需修改；Docker 初始化不会创建或覆盖 `.env`。默认 `./state`、`./storage` 相对于 Compose 文件目录，绝对路径指向实际宿主目录。移动部署目录后先核对 `.env`，不要把容器挂到新空目录并误以为数据丢失。修改目录需同步准备权限、修改 `.env` 并在维护窗口重建受影响服务。已有 TLS 密钥保留；服务器 IP 改变需要在保留旧证书备份后重新签发正确 SAN 的证书。

M0 的 `.env` 与生成的上游配置包含摄像头明文凭据，生成的敏感配置权限为 0600；业务数据库只存观察结果，不实现正式版凭据加密/查看/批量编辑接口。不要上传 `cameras.csv`、`.env`、`state`；ZLM 上游日志也可能包含 RTSP 地址，分享诊断前需脱敏。Docker 构建上下文已排除这些文件。生产版密码管理按 PRD 实现。

**本包不自动清理录像/事件，也不上传云端。** 观察存储空间，实验结束后先停止服务再按自己的数据保留要求处理 `storage/m0-recordings`。事件索引 90 天、普通录像保留期与云端 365 天清理属于正式控制面后续实现。

## 6. 验证命令与故障实验

```bash
# 以下检测服务名用于 CPU；Intel 替换 frigate 为 frigate-intel。
docker compose logs --tail=100 probe
docker compose logs --tail=100 frigate
docker compose logs --tail=100 zlm

# 核验锁定 Frigate 版本的真实配置 schema，无需先连摄像头。
docker compose run --rm --no-deps --entrypoint python3 frigate -c \
  'from frigate.config import FrigateConfig; FrigateConfig.parse_yaml(open("/config/config.yml").read()); print("Frigate config valid")'

# Frigate 停止时 ZLM 仍应完成新分片。
docker compose stop frigate
docker compose start frigate

# MQTT 停止时检测可能继续，接收端应显示连接中断。
docker compose stop mqtt
docker compose start mqtt

# ZLM 重启会产生真实媒体缺口，观察器恢复拉流/录像并记录新分片。
docker compose restart zlm
```

保持测试页关闭至少跨过数个分片后再查看，确认后台录像不依赖浏览器。现场让人/车进入区域，检查 MQTT 事件与原片画面对应；下载跨片附近原片，核对关键帧和真实缺口。M0 的事件入库幂等，不具备 Frigate 历史 HTTP 对账或独立可用性/故障时间带，相关恢复边界需要按清单另行验证。

修改摄像头、端口或硬件档案：先在修改 .env 前执行 `docker compose down`，编辑 `.env`，再执行 `docker compose up -d --build`，自动重新初始化并启动。旧版 CSV 部署迁移到 URL 配置时来源摘要会改变，历史仍按原来源保留。不要在服务运行时单独调用初始化覆盖上游配置。会中断本实验所有通道并保留实际分片/SQLite 数据；不是生产版单通道在线切换/自动回滚。旧流名带来源配置摘要，旧文件/事件不改归新来源。初始化不会迁移或删除旧媒体。

## 7. 停止、验证状态与后续实验

```bash
docker compose down
```

数据在宿主目录中，停止/重建容器不会自动删除。完整备份应包含 `state` 和实际录像目录，数据库一致性备份先停止探针；复制配置并不等于复制所有媒体。

已在开发端进行：Python 单元测试（包含 .env URL 输入、固定通道、来源隔离与初始化 CLI）、官方 Compose CPU/Intel 档案解析、特殊字符和生成配置结构/路径检查。开发端没有 Docker 守护进程或这两款服务器/真实摄像头，尚未执行镜像构建启动、Frigate 实际 schema/模型加载、GPU 加速、Nginx 运行、取流/录制/浏览器端到端测试。先运行两路并回传脱敏状态与错误，再判定候选配置是否可用。

WebDAV 云归档、剪辑拼接、自动源切换回滚、完整可靠性指标、权限撤销和到期清理未在本包实现；这个初始部署包用于拿到它们所需的上游数据与接口证据。完整 M0 出口仍以验证清单记录的实验结果为准。

上游核对来源：[Frigate 0.17.2 Dockerfile](https://github.com/blakeblackshear/frigate/blob/v0.17.2/docker/main/Dockerfile)、[Frigate 安装](https://docs.frigate.video/frigate/installation/)、[ZLM API](https://docs.zlmediakit.com/guide/media_server/restful_api.html)、[ZLM 配置](https://github.com/ZLMediaKit/ZLMediaKit/blob/master/conf/config.ini)。镜像摘要核对日期 2026-10-03。
