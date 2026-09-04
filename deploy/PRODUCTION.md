# 云令生产部署记录

## 当前部署

- 首次部署日期：2026-08-29（Asia/Shanghai）
- 最近生产更新：2026-09-02（Asia/Shanghai）
- 控制面地址：`https://aiwise.top`
- 腾讯云主机：`134.175.131.19`
- 部署目录：`/opt/yunling`
- 生产代码基线：`c22dcaf`（本次重建并更新 API 与 Web 服务）
- TLS：Caddy 自动签发 Let's Encrypt 证书，首次验收有效期至 2026-11-27

API、Scheduler、Web、Caddy、PostgreSQL、Redis、MinIO 和 Ops 八个长期服务均通过 Docker 健康检查。生产 HTTPS 验收结果：健康接口 200、登录 200、会话 200、退出 204。控制台顶部已部署全局“退出登录”按钮，成功时返回登录页，失败时保留当前页面并显示中文错误。

## 版本化发布记录格式

以下字段在首次启用新发布入口并完成真实验收后填写。当前尚未发生的发布、回滚或演练不得预先记为成功。

- 当前候选运行编号：待首次批准发布后填写
- 当前源提交 SHA：待首次批准发布后填写完整 40 位值
- services 镜像摘要：待验收发布清单后填写
- web 镜像摘要：待验收发布清单后填写
- ops 镜像摘要：待验收发布清单后填写
- 上一成功目标：待生产 `previous.json` 生成后填写
- 发布入口首次启用时间：待 `bootstrap` 真实成功后填写（Asia/Shanghai）
- 最近一次回滚演练：待真实演练后填写时间、目标、结果和诊断编号
- 代理发布锁 SHA-256：待首次启用前从 `deploy/agent/release-lock.json` 独立核对并填写

操作流程、状态含义、密钥轮换和故障处理见 [生产发布与回滚手册](RELEASE.md)。

## 脚本同步控制台

2026-09-02 使用提交 `53eb0e9` 的 Web 源码发布脚本同步控制台，部署包 SHA-256 为 `bd8ad3a4eb49380799b83546e174636ff358fd731dfdeefa9f8be4531b02d81f`。本次只重建并重启 `web`，未重启 API、Scheduler、数据库或执行节点。

上线后容器内 `http://127.0.0.1:8080/healthz` 与公网 `https://aiwise.top/healthz` 均返回 `ok`，生产浏览器已实际加载 `https://aiwise.top/sync`。页面显示已发布脚本 `1`、已就绪 `1`、同步中 `0`、异常节点 `0`；当前版本 `2` 的“京东云只读诊断验收”在“京东云执行节点-1”上内容校验一致，可用于任务执行。

同步页只展示每个脚本的当前发布版本，并提供服务器级下载、校验、可用状态；当节点进入 `failed` 或 `drifted` 时可人工重新入队。此次 Web 更新同时修正备份历史的时间语义：定时占位记录显示 `scheduledFor` 和“计划时间”，手动备份显示 `createdAt` 和“创建时间”，避免未来计划被误认为长期排队的历史任务。

## 服务器接入向导

2026-09-02 使用提交 `2fee0b2` 发布全中文“接入新服务器”向导，部署包 `yunling-web-2fee0b2.tar.gz` 的 SHA-256 为 `458dc6ddd3bef9594cb07cdadec186c840f356e02ef6d421b967c0e69d4cdbdc`。部署前已在 `/opt/backups` 保留 Web 源码快照；本次只重建并重启 `web`，未重启 API、Scheduler、数据库或执行节点。

上线后容器内 `http://127.0.0.1:8080/healthz` 和公网 `https://aiwise.top/healthz` 均返回 `ok`，公网健康请求状态码为 `200`。线上已加载资源 `index-Db1vElx2.js`，并确认包含接入向导、一次性令牌、安全安装命令和权限失败重试逻辑。管理员生产页面已实际打开向导，服务器名称、云厂商、地域、标签、安全边界与两步流程展示正常；验收没有点击“创建一次性令牌”，未产生测试注册数据。

向导仅对管理员开放，会话权限读取失败与真实非管理员状态明确分离。签发请求完成前无法关闭向导，避免丢失仅显示一次的令牌。生成的安装命令由 Bash 显式执行，令牌从 `/dev/tty` 隐藏读取，不进入 Shell 历史或进程参数；`set -euo pipefail` 与 `EXIT` trap 保证失败退出码不被吞掉并清理令牌变量。

最终回归结果：Web 20 个测试文件、53 项测试通过；Web 生产构建通过；安装命令通过 Bash 语法检查和失败退出码保留检查。

## 一键代理接入与双架构发布包

2026-09-02 使用提交 `c22dcaf` 发布一键代理接入完整流程及终审加固。上传到腾讯云的源码归档为 `yunling-agent-release-c22dcaf.tar.gz`，大小 `483900` 字节，SHA-256 为 `8059830f411f40ad1810d659e41eb114ce1ce0e47b5e96fdf7ec24825d4204ad`。部署前已创建恢复归档 `/opt/backups/yunling-before-agent-fix-c22dcaf-20260902.tar.gz`，大小 `486745` 字节，SHA-256 为 `d4daa5d1b59c41866925bced9aa00b713fe1075a9c94585a5a2f493ede20e3e7`；归档不包含 `deploy/.env`、`deploy/secrets` 或其他凭据文件。

本次仅重建并替换 `api` 与 `web`：

- API 镜像：`sha256:697358fecbb76253e17c204cc50c57d26d8f2ed804214e1deee08b4bde8aaf10`
- Web 镜像：`sha256:993edfc966ecc9122bbce5dee9996872f0dd70062c92ee80d5f3dd74373dd35e`
- Scheduler 镜像仍为 `sha256:0ce903240d96ae673da423a786a867c7cef69ea221758f4517ea352aed9f0cb2`，启动时间仍为 `2026-09-01T04:13:18Z`
- Ops 镜像仍为 `sha256:f92f70902194f95daa9f4583c36d3573d0018b9aca79b2e82e741324c2bee8ad`，启动时间仍为 `2026-09-01T06:41:53Z`

API 镜像内置代理版本 `0.1.0`，发布清单包含以下两个不可变归档：

- `yunling-agent-0.1.0-linux-amd64.tar.gz`：`3424904` 字节，SHA-256 `64982ac930917ac9a90e4a118d05214bee2ac421cee8d853e7e821d8acbcf3e4`
- `yunling-agent-0.1.0-linux-arm64.tar.gz`：`3066090` 字节，SHA-256 `fdfb55e5936a7ee5ab4529734a05b35267f1735346aca4aa78aabb9a2d041fd1`

两个归档均包含代理二进制、安装脚本、systemd 服务模板和 Polkit 规则；ELF 架构编号分别为 `62`（AMD64）与 `183`（ARM64），AMD64 二进制自检版本为 `0.1.0`。公网清单和两个下载地址均返回 `200`，下载内容哈希与清单一致；归档响应带有 `application/gzip`、附件文件名、不可变一年缓存与基于 SHA-256 的 ETag，命中 ETag 时返回 `304`。未登录调用一次性注册令牌接口返回 `401`，没有生成测试令牌。

API、Web、Scheduler 与 Ops 均保持 `healthy`，容器内 API/Web 健康检查和公网 `https://aiwise.top/healthz` 均通过。生产站点实际提供的前端资源 `index-CMl5RGj0.js` SHA-256 为 `a199200020265b1b12c332d7079d80d0f31ef505c6452d2b4914b21909e5bd31`，与本次构建产物一致，资源内包含“接入新服务器”“创建一次性令牌”“安装并连接代理”等完整中文向导内容。

终审加固修复了 heredoc 标准输入与安装器终端预检冲突：命令把安装器输入显式连接到 `/dev/tty`，令牌仍不会进入命令、环境变量或 Shell 历史。凭据生成后立即进入“已注册”阶段，后续收尾失败会清理引导秘密并重启代理而不是停止服务；现有普通凭据支持保留身份的修复重跑，代理二进制使用同目录临时文件原子替换。公开发布限流改为按客户端隔离，只信任私有网络或回环直接对端的首个转发地址，同时保留全局保险上限和并发下载限制。关闭确认框焦点被限制在自身范围内，`__proto__` 标签也不会再静默丢失。

现有“京东云执行节点-1”在发布后保持 `online`，代理版本为 `0.1.0`；验收查询时心跳延迟约 `4` 秒、运行任务数为 `0`，资源采集正常。此次发布未触发代理重新注册，也未产生新服务器记录。最终回归结果：Go 全量测试通过；Web `21` 个测试文件、`77` 项测试通过；Web 生产构建通过；安装器 Bash 语法、安装失败/注册后失败恢复、现有身份修复和双架构打包测试均通过。Go race 检测因本机工具链未启用 CGO 未执行，限流的并发与客户端隔离测试已在普通 Go 测试中通过。

## 自动备份与恢复校验

- 数据库迁移：`000010` 至 `000012` 已应用，当前迁移版本为 `12`
- 本机 Restic 与腾讯云 COS 双份备份：已完成真实手动备份验收
- 2026-09-01 12:30（Asia/Shanghai）定时备份：`succeeded`，本机快照 `5687d8c8a412aa114510fef4fe5afeef837a3a2c3d817eff02fa27e3db84989e`，COS 快照 `49ecbe8a9c25e8b14f5e6cf95807eb287a0831f5d3d83d4547274e65ea648ac1`，清单 SHA-256 `e50bbfebdda91f851421991e888cae13e517290c4c9108628e7f4010e5e52d6a`，`2451834` 字节、`1` 个对象、尝试 `1` 次
- 2026-09-01 18:30（Asia/Shanghai）定时备份：`succeeded`，本机快照 `de6353f9b93fc51329cbad67dfe1fce6fb6fdffbd6284404a343af09862882e9`，COS 快照 `59428a947debeda4b107caef310497a53635093bc9725adc19eb0be8329e9e34`，清单 SHA-256 `da8422213f6cdd537a0a27ed547bf13d419708e7f61c51718f64dc9ad531db7f`，`2663011` 字节、`1` 个对象、尝试 `1` 次
- 隔离恢复校验：`succeeded`，迁移版本 `12`，校验对象数 `1`
- 临时恢复数据库：成功与失败路径均已清理，生产库未被覆盖
- COS 凭据、Restic 密码和数据库备份凭据：仅通过 root-only 文件挂载，不写入环境变量或日志

2026-09-02 00:10（Asia/Shanghai）只读核对结果：12:30 与 18:30 两次定时备份均按计划完成，本机和 COS 快照字段完整、错误字段为空，形成连续两次成功证据；数据库中“已到期但仍排队”的备份数量为 `0`。Ops 健康接口返回 `ok`，`yunling-ops-1` 状态为 `Up 9 hours (healthy)`。

### 生产出口网络中断恢复演练

2026-09-02 09:51 至 09:55（Asia/Shanghai）对 Ops 执行受控出口网络中断演练。先保留 `yunling_backend`、临时断开 `yunling_egress`，再从控制台触发一次手动备份；运行 `04d839bd-cd90-4676-8042-07014dd3572a` 进入 `uploading`，本机快照已生成，COS 快照字段暂为空。恢复 `yunling_egress` 后，原运行于 09:55:32 完成，没有创建第二条运行；最终状态为 `succeeded`，本机与 COS 快照字段完整，清单 SHA-256 为 `ca5e63fa23def1ab90c3916836eec6d1a1125f249de4cd79377ef8dd52b6797a`，备份大小 `3198848` 字节、对象数 `1`、尝试次数 `1`，错误字段为空。

演练后“已到期的排队或降级备份”数量为 `0`，近六小时备份告警与对应通知发件箱记录均为 `0`；这是预期结果，因为出口在首次 COS 复制进程结束前恢复，运行没有持久化进入 `degraded`，因此不应发送故障或恢复误报。Ops 已重新连接 `yunling_backend` 与 `yunling_egress`，健康接口返回 `ok`，容器状态为 `Up 19 hours (healthy)`。本次结果证明首次复制过程内的网络中断可在同一运行、本机快照不变的情况下恢复；不将其误记为持久化 `degraded` 后的定时续传验收。

## 飞书通知

- 飞书 V2 自定义机器人通知：已配置并启用
- Webhook 与签名密钥：通过系统级秘密加密保存，控制台只显示脱敏机器人标识，保存后输入框立即清空
- 生产测试消息：`sent`，首次投递成功，尝试次数 `1`
- 群内验收：用户已确认收到“云令飞书测试消息”
- 投递服务：`yunling-ops` 健康，使用持久化发件箱和分级退避重试

测试消息未包含凭据或业务数据，生产验收期间只发送一次，没有重复投递。

## 可靠派发与真实执行验收

- 数据库迁移：`000009_run_dispatch.up.sql`
- API 二进制 SHA-256：`56aca1cd3b36026b279776fed3400a31a9df3f379ba42949bfcc7e3186831999`
- Agent 二进制 SHA-256：`192ac76e6117fd6346c9763e2346cc6916bea7f286c147443919a8a0f0462a3f`
- 京东云 Polkit 规则 SHA-256：`d07d80f089d57a230622890794e438fea240ab791be2c93b56a0ce905443b27f`
- Services 镜像清单：`sha256:d1783f00d2a53b044c46037afa8aeed530db98b86dbfdf7d0f17c5c1a2811af8`

可靠派发器已从 PostgreSQL 原子领取 `assigned` 运行，通过现有代理 WebSocket 下发完整执行载荷，并按执行令牌在代理端幂等处理重复派发。无法连接节点时，运行保留在可重试状态；没有可分配服务器时，任务继续留在排队队列等待资源与节点恢复。

京东云真实节点验收沿用唯一运行实例，没有创建第二条运行：

- 任务：`京东云只读诊断验收任务`
- 运行 ID：`31109d87-f742-41b0-b198-9c45de7dc287`
- 执行服务器 ID：`6f445eb6-e388-4ebf-a67d-33834c816893`
- 最终状态：`succeeded`
- 退出码：`0`
- 同名运行数量：`1`
- 最终活动资源租约：`0`
- API/SSE 事件数量：`15`（14 条状态事件、1 条 stdout 日志）

中央日志已收到京东云主机真实输出：验收标识、主机名、ISO 时间、CPU 核数、可用内存和根分区可用空间。验收过程中定位并修复了代理重启后的心跳序号、固定 `systemctl` 路径、systemctl 有界错误诊断，以及京东云 Polkit 无法解析 `system_unit`/`no_new_privileges` 主体字段的问题。Polkit 最终仍只允许专用 `yunling-agent` 用户对 `yunling-run@…service` 固定模板执行 `start`、`stop`、`kill`，其他用户、其他单元与 `restart` 均拒绝。

最终回归结果：Go 全量测试通过；Web 14 个测试文件、22 项测试通过；Web 生产构建通过。

## 京东云执行节点

- 公网地址：`117.72.119.183`
- 控制面名称：`京东云执行节点-1`
- 云厂商与区域：京东云 / 中国大陆
- 代理版本：`0.1.0`
- 运行环境：Bash、Python 3
- systemd 服务：`yunling-agent.service`，已启用且运行中
- 专用账号：`yunling-agent`、`yunling-runner`
- 凭据文件：`/var/lib/yunling-agent/credentials.json`，权限 `0600`
- 工作目录：`/var/lib/yunling-agent`
- 固定任务模板：`yunling-run@.service`
- Polkit 权限：仅允许代理专用用户管理固定任务模板实例

首次接入验收时，控制面已收到在线心跳与资源数据：2 核 CPU、约 4 GB 内存、60 GB 系统盘，当前运行任务数为 0。一次性注册令牌已从腾讯云和京东云清理，代理环境文件中不保留控制面登录密码、注册令牌或用户此前提供的 root 密码。

执行节点常用检查命令：

```bash
systemctl status yunling-agent.service
journalctl -u yunling-agent.service --since '-30 min' --no-pager
systemctl list-units 'yunling-run@*.service'
```

## 管理员凭据

初始管理员邮箱是 `admin@aiwise.top`。`deploy/.env` 中的全部 `YUNLING_BOOTSTRAP_*` 临时变量已删除。

管理员已通过中文账号安全面板修改密码，使用新密码退出并重新登录成功。腾讯云本机将旧密码与数据库当前 Argon2id 哈希进行只读比对，结果为不匹配；初始密码没有通过网络发送，也没有写入命令输出。

用户确认恢复密钥已保存到离线密码管理器后，服务器上的两个 root-only 临时凭据文件均已永久删除并验证不存在：

- `/root/yunling-initial-admin.txt`
- `/root/yunling-recovery-key.txt`

离线恢复密钥现为唯一副本，后续不得通过聊天、工单、代码仓库或终端日志传递。

## Windmill 下线与恢复点

Windmill 容器、旧镜像、PostgreSQL 16 镜像和 `/opt/windmill` 已删除。以下恢复材料仍保留：

- Root-only 备份：`/opt/backups/windmill-before-yunling-20260829`
- PostgreSQL 全量导出：`database.sql`
- Windmill 配置归档：`windmill-config.tar.gz`
- Nginx 配置归档：`nginx-config.tar.gz`
- 完整性清单：`SHA256SUMS`
- 保留数据卷：`windmill_db_data`、`windmill_worker_dependency_cache`、`windmill_worker_logs`

备份的三个核心文件已通过 SHA-256 校验。旧宿主机 Nginx 已停止并禁用，80/443 由云令 Caddy 独占。

可靠派发上线前另保留了两份 root-only 恢复材料：

- PostgreSQL：`/opt/backups/yunling-before-dispatch-20260829.sql`，SHA-256 `56eadde6e141ef8922800e9c5c598db6a9c85aafda2bbce34edb576ecc000562`
- 源码归档：`/opt/backups/yunling-source-before-dispatch-20260829.tar.gz`，SHA-256 `f3b2cfcb7fcf0a8be94ecdffbf9731f23962534f96479d2c95cb92ec72a237a8`

## 资源与运维注意事项

主机为 4 核、约 3.6 GiB 内存、40 GiB 系统盘，并有约 2 GiB Swap。上线后系统盘约使用 16 GiB、剩余约 23 GiB；可用内存约 2.6 GiB。

该内存容量低于部署手册建议的 8 GiB。正式增加大量任务、日志或团队成员前，建议升级到至少 8 GiB，并持续关注 OOM、Swap 使用量和磁盘增长。Docker 全局构建缓存未自动清理，以免影响同机其他项目。

常用检查命令：

```bash
cd /opt/yunling
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 api scheduler caddy
curl --fail --show-error https://aiwise.top/api/health
```
