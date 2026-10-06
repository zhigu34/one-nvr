# one-nvr 镜像版本基线

确定日期：2026-10-03。适用范围：正式版单机 Docker Compose，Linux `amd64`（AMD EPYC 7402 / Intel N5105）。M1-A 已交付正式 Dockerfile / Compose 并通过 GitHub CI 的构建、基础部署和网关验证；M1-B 媒体能力仍在实测中。

## 版本选择

| 组件 | 固定标签 | 用途 | 验证范围 |
| --- | --- | --- | --- |
| Go | `1.27.1-bookworm` | 构建 API / Worker / admin | M1-A/M1-B CI 实际构建通过 |
| Node.js | `24.21.0-bookworm-slim` | 构建 shadcn-admin 前端 | M1-A/M1-B CI 实际构建通过 |
| Debian | `bookworm-20260918-slim` | 自建 Go 应用运行基础 | CI 核心实际运行/重复部署通过 |
| PostgreSQL | `17.11-bookworm` | 基础模块数据库 | CI PostgreSQL 实际迁移/集成通过 |
| Nginx | `1.30.5-alpine3.24` | 自建 gateway 的运行基础 | CI 实际网关/换证/恢复通过 |
| ZLMediaKit | `master + 固定 digest` | 基础模块取流与录像 | 沿用 M0 两机每机两路直播/录像/回放的构建 |
| Frigate | `0.17.2` | 可选智能检测 | 沿用 M0 N5105 事件/抓拍验证构建；AMD 检测未验收 |
| Mosquitto | `2.0.22` | 可选智能检测的 MQTT | 沿用 M0 消息链路构建 |
| OpenList | `v4.2.6` | 可选云归档的自带服务 | 仓库与架构已核对，归档链路未测 |

Go / Node 仅用于构建，不增加常驻容器。API、Worker 与一次性 admin 命令复用同一自建 Go 应用镜像，以 Debian slim 为运行基础；前端构建产物复制到以 Nginx 为基础的自建 gateway 镜像，不单独运行 Node 服务。基础 5 个容器，两模块全部开启 8 个，初始化/迁移执行完退出。

Go 沿用设计的 1.27 系列；Node 采用 24 系列 LTS 构建前端；PostgreSQL 保持已选 17 系列，固定其补丁版本。Go、Node、PostgreSQL 和应用运行基础统一选择 Bookworm 变体，减少构建与运行环境差异。Nginx 正式版选稳定分支 1.30.5。OpenList 使用标准 v4.2.6 镜像。

ZLM 当前基线没有记录可对应的语义版本号，直接沿用 M0 构建的 digest；`master@sha256:…` 按 digest 寻址，不随 master 标签更新。Frigate 与 Mosquitto 同样沿用已测构建，不因上游发布新版本自动升级。两台机器用同一 Frigate 基础镜像，Intel 核显通过设备映射和硬件配置处理；不凭 CPU 品牌切换 TensorRT 镜像。

## 完整镜像引用

以下均使用 `tag@digest`。digest 为镜像索引/manifest 的摘要，不是容器 image ID。部署明确使用 `linux/amd64`；索引中的其他架构及证明描述项不代表 one-nvr 已支持它们。

```text
golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195
node:24.21.0-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6
debian:bookworm-20260918-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
postgres:17.11-bookworm@sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652
nginx:1.30.5-alpine3.24@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94
zlmediakit/zlmediakit:master@sha256:25ecf6c4a55e72bc495be15c8480f0b0cef58c712e5c9f0596452777b41bf344
ghcr.io/blakeblackshear/frigate:0.17.2@sha256:d4351369984d4a9e2a49ac59736f6490856a7ea11f7790040746d21496967010
eclipse-mosquitto:2.0.22@sha256:199ea8ef2e35ec2b1b37e59cfd1dbae538ed4dfa4a2251a121a52215a6248a21
openlistteam/openlist:v4.2.6@sha256:c555c6e1c8af2aead38ed12ec761ac077fdf046d19cf033414be8e056aec6b64
```

2026-10-03 通过 Docker Hub / GHCR Registry API 读取上述九个引用的 manifest，确认返回的 digest 和 `linux/amd64` 条目。新版本查询具体标签；M0 沿用版本查询已有不可变 digest，以保留当时的实际构建。查询仅读取元数据，没有拉取并启动这些镜像。已核对可拉取的 manifest 不代表正式构建、数据库兼容或云归档验收通过。

## 历史实验

M0 验证包已于 2026-10-06 从当前源码移除，可从 Git 历史恢复。正式版沿用已核对的 ZLM/Frigate 摘要，构建和运行版本见上表；不再构建 Python M0 工具镜像。旧机器上的实验容器和数据不由源码清理操作删除。

## 构建、部署与升级约束

- 正式 Dockerfile 的各个 FROM 与上游运行镜像引用采用上述 digest。自建应用/gateway 发布镜像还须记录源码 commit、构建参数和最终 digest；CI 已实际构建；尚未发布产品镜像，不能用基础镜像 digest 代替最终产品镜像 digest。
- 前端导入时记录 shadcn-admin commit、固定包管理器版本并保留 `pnpm-lock.yaml`，后端保留 `go.sum`。ffmpeg/ffprobe、CA 证书等通过系统包安装时还须记录包版本和来源；基础镜像固定不等于 apt 安装结果已冻结，发布构建需固定包版本与可获取的仓库快照。
- `.env` 可覆盖镜像引用以使用离线/内部仓库，但发布基线保留可读版本和已核对的不可变摘要。内部镜像搬运后核对 manifest 与架构，不能仅检查同名标签存在；`docker save/load` 导入须核对本机实际 ID / RepoDigests，不能假定 digest 引用一定保留。
- 部署脚本仅准备启用模块所需镜像；Frigate 关闭时不要求 Frigate/MQTT，云归档关闭时不要求 OpenList。Go/Node 构建镜像只有需要构建自建镜像时才准备。
- 版本更新由明确的基线变更触发，不自动跟随 latest 或重解析固定标签。更新后核对 digest，完成对应构建与契约/真机检查，并保留上一个发布引用。
- 正式 Nginx 的 HTTPS、登录、媒体代理、静态文件目录/文件权限需重测；PostgreSQL 验证迁移/备份恢复；OpenList 验证持久目录权限、初始化凭据、WebDAV 上传/Range/到期删除。Frigate 硬件能力和检测容量仍以实际测试为准。

## 官方依据

- [Go 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/golang)、[Go 发行说明](https://go.dev/doc/devel/release)。
- [Node 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/node)。
- [PostgreSQL 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/postgres)、[版本支持政策](https://www.postgresql.org/support/versioning/)。
- [Debian 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/debian)、[Nginx 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/nginx)。
- [Mosquitto 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/eclipse-mosquitto)。
- [Frigate 0.17.2](https://github.com/blakeblackshear/frigate/releases/tag/v0.17.2)、[ZLM 上游](https://github.com/ZLMediaKit/ZLMediaKit)。
- [OpenList Docker 部署说明](https://doc.oplist.org/guide/installation/docker)。

## M1-B 媒体工具

2026-10-05 增加 FFmpeg/ffprobe：Debian Bookworm 签名快照 `20260919T000000Z`，包 `ffmpeg=7:5.1.9-0+deb12u1`。`deploy/production/media-packages.lock` 记录该包及可用依赖备选包的版本、架构和 SHA256；安装脚本验证每个实际下载的 deb 后才安装。基础镜像已安装的软件由原 digest 固定，构建不执行浮动全系统升级。

构建可通过 `--build-arg ONE_NVR_DEBIAN_SNAPSHOT_URL=https://内部快照镜像/路径/` 指定保留同一签名索引和软件包内容的镜像地址。仓库签名和 TLS 校验均开启；历史快照仅关闭索引的时效检查，不关闭签名认证。修改来源不能绕过包哈希锁。最终运行镜像包含媒体工具，但仍由 API / Worker 共用，不增加常驻容器。固定镜像的真实媒体契约见 [M1-B 验证记录](M1-B-validation.md)。
