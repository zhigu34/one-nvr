# one-nvr M1-B 部署

本分支已实现账号、固定通道槽位、摄像头配置修订/测试/应用、批量导入与配置导出、目录池绑定、连续录像/仅取流、录像索引、时区及证书面板，已通过隔离 M1-B 联合验收。实时视频播放与录像内容回放仍待 M1-C；事件与云归档业务仍返回“尚未实现”。原 M0 可以继续独立运行，不复用其状态目录。同机并行运行时，本项目必须选择与 M0 不冲突的 Web 和 RTC 端口。

Linux amd64，Docker Engine + Compose v2。宿主不安装 Go、Node、Python。首次部署构建 app/gateway，其他镜像全部按 [固定版本与摘要](../../docs/image-versions.md) 拉取。构建需要网络；更换包源不能解决 Docker Hub 鉴权网络失败。可用 `--admin-image registry/name:version@sha256:...` 指定事先构建并验证的工具镜像。

```bash
cp deploy/production/.env.example deploy/production/.env
# 修改 PUBLIC_URL、Web/RTC 端口、目录和 CAMERA_CIDRS，保持数据目录独立。
mkdir -p /srv/one-nvr-storage/disk1
# 授予 API/Worker 运行 UID 10001 对实际池目录的访问权限；不由程序改底层磁盘权限。
./deploy/production/deploy.sh --check
./deploy/production/deploy.sh
```

`.env` 只按字面量解析，不执行 shell，不展开 `$()` 或变量，不保存秘密。数据目录和内部凭据首次生成后持久保存；不要删除 `secrets/`、`postgres/` 或 `tls/`。生产 Compose 是 `DATA_DIR/runtime/compose-<hash>.json` 的不可变版本；`compose.json` 指向最近渲染内容，权限 0600，包含内部数据库凭据，不要上传。

脚本打印获取一次性初始化令牌的命令；令牌只在未初始化站点时可取。打开 PUBLIC_URL，填令牌，创建站点管理员和 16/32 槽位。正常运行 5 容器：gateway（含前端）、api、worker、postgres、zlm。启用 Frigate 增加 frigate/mqtt，启用 OpenList 增加 openlist，因此为 5/7/6/8。临时迁移、初始化和探测容器会结束退出。

## 存储和时区

程序只登记已有目录，`ONE_NVR_STORAGE_ROOT=/srv/one-nvr-storage` 映射为 `/storage`；界面填写 `/storage/disk1`。目录读写和身份标记由 API/Worker 独立检查；没有测试视频源时 ZLM 明确待验证。系统不会配置 NAS、RAID、挂载、热备或自动创建池目录。设置中的 IANA 时区控制显示与新录制 run 的文件命名，数据库时间保留 UTC；历史 run 的规则冻结。正式录像规则见 [录像路径](../../docs/recording-file-layout.md)。

## 协议和证书

默认 HTTPS，手动模式首次生成一年有效的私有自签证书。首次会出现客户端信任提示；之后可在设置上传受信证书并应用。入口就绪检查还会比对数据库生效版本与实际新连接叶指纹。改变证书不会注销会话；变更访问协议会撤销会话。HTTP 中已经保存的有效手动候选在首次切换 HTTPS 时会重新校验并应用，复用原版本；无效候选使部署在重建核心前失败。后台状态刷新不延长 30 分钟闲置期限，界面可见的点击、按键和滚动按分钟节流续期，12 小时绝对期限仍有效。

HTTP 是显式选择：`ONE_NVR_PUBLIC_URL=http://服务器IP:8080`，保持 HTTP_PORT=8080。HTTP 下可保存待用证书，不会显示已生效。只有当前协议的 Web 端口对外映射；RTC 的 8000 UDP/TCP 始终独立，且不得与入口端口重合。域名 PUBLIC_URL 需要显式填写可达 `ONE_NVR_MEDIA_HOST` IP。

目录续签：填写现有 `ONE_NVR_TLS_DIR=/srv/nvr-certs`，将整个目录只读映射，不会自动创建输入目录。续签程序完整更新 `fullchain.pem` 和 `privkey.pem`；内容连续稳定后转为私有不可变版本，再由 Nginx 校验/应用/核验。错配保留上一版本；回滚暂停自动应用。配置来源变更需重新运行部署。不要只映射单个证书文件。

## 可选模块和硬件

`.env` 默认 `ONE_NVR_FRIGATE_ENABLE=no`、`ONE_NVR_OPENLIST_ENABLE=no`。重新执行部署按配置启停；关闭时只停止项目内对应服务，保留目录，不停止 ZLM、不删除卷。开关表示启用意图，健康状态和业务“尚未实现”分别显示；启用模块失败返回非零并保留核心可用，不能当作全成功。

启用检测才枚举目标 Docker daemon 的只读 sysfs，然后在同一固定 Frigate 镜像中自检。使用固定生成命令的短 H.264 样本并记录样本和模型 SHA256，分别运行 FFmpeg 解码、CPU TFLite/Intel OpenVINO 推理；不是通道容量或实际摄像头认证。按 PCI 身份匹配 Intel render 节点，ASPEED 显示卡不当加速器。`auto` 优先实测的 Intel，初次 CPU 也需要自检；曾选中 GPU 失败报告故障，保留期望，不静默降级。可显式指定 `epyc-cpu` 选择 CPU。Nvidia/未认证 AMD 不自动使用。

报告写 `DATA_DIR/hardware/latest.json`；`.env` 不写入运行结果。有效设备最小覆盖写 `runtime/hardware.compose.json`，手动 Compose 操作启用检测时需同时 `-f` 这个文件；日常使用 deploy.sh。Frigate 模板 `record.enabled=false`，无摄像头时保持无业务检测源，不能误报产生事件。OpenList 仅持久保存自身配置，不默认映射录像根；未来仍允许外部 WebDAV。

## 验收

GitHub CI 使用真实 PostgreSQL、Nginx 与浏览器；开发入口：

```bash
./deploy/production/dev.sh test-go -race ./internal/... ./cmd/... ./migrations
./deploy/production/dev.sh test-db -race ./tests/integration
./deploy/production/dev.sh web build
./deploy/production/dev.sh e2e
```

实际证据、失败与未测项记录在 [M1-A 验证报告](../../docs/M1-A-validation.md)。N5105 驱动/权限/模型自检需部署后报告，EPYC 推理和 16–32 路容量不是当前 CI 的证明。M1-B 的真实媒体、页面与故障联合验收状态见 [M1-B 验证报告](../../docs/M1-B-validation.md) 和 [验收矩阵](../../docs/M1-B-decisions.md)。只有明确记录成功的精确提交通过对应门槛；不能以容器 ID 未变化代替录像连续性。内容回放和事件业务仍待后续。

## 摄像头与普通录像

`ONE_NVR_CAMERA_CIDRS` 是允许摄像头 IP 的范围，例如 `192.168.33.0/24,192.168.66.0/24`；空值允许初始化，但拒绝源配置。配置提交与实际连接都会检查最新地址边界；回环、链路本地和本项目内部组件不能成为摄像头源。更改部署范围后重新运行 deploy.sh。

在通道管理进入“配置摄像头与录像”。编号与 UUID 固定；分别保存草稿、测试取流、应用配置。主/子流只填路径，如 `/main`、`/sub`。子流故障明确降级；主流失败回滚旧源。清空只移除当前摄像头，重新配置沿用原通道的录像策略和历史。

首次可以选择“关闭录像，仅取流”。开启连续录像前登记目录池、通过真实媒体检查并绑定；码率、目录和可用空间证据均需有效。关闭录像保留取流与历史，不启动事件预录。连续录像目标 60 秒片，正式发布与索引由 Worker 校验。录制断点/未知状态会记录；M1 不自动清理录像，空间不足时阻断相关写入，恢复需连续健康确认。

批量 CSV 模板为 `channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path`，可追加 `onvif_port`。先预览、选择、测试，再确认应用；空源行跳过，不清空已有源。CSV 空密码明确清空，可在预览中改选保留；JSON 缺失密码表示保留。管理员导出的是包含明文密码的本地 JSON 文件，可重新导入。批任务链接可恢复进度，但不自动重试或再次应用。

## 隔离联合验收

以下只运行私有测试项目/合成摄像头，不能指向真实站点数据。需先构建 `one-nvr/app:ci`、`gateway:ci`、`browser:ci`、`media-test:ci` 和 `fixture-runner:ci`；CI 已提供完整构建步骤。

```bash
./deploy/production/test-media-e2e.sh --acceptance --no-build
# 同时执行所有独立媒体合同和联合验收：
./deploy/production/test-media.sh --acceptance
```

联合验收要求实际两路 RTSP/MP4、API/Worker/数据库/ZLM 故障、目录证书实际更新、发布进程在移动前后 SIGKILL、私有媒体边界和历史索引/文件恢复。缺失 fixture 或任一断言失败均非零退出；通过本机类型/静态检查不代表实际 Docker 验收通过。
