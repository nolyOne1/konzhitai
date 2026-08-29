# 云令生产部署记录

## 当前部署

- 部署日期：2026-08-29（Asia/Shanghai）
- 控制面地址：`https://aiwise.top`
- 腾讯云主机：`134.175.131.19`
- 部署目录：`/opt/yunling`
- 生产代码提交：`2225068`（最终安全断言提交：`7af69d0`）
- TLS：Caddy 自动签发 Let's Encrypt 证书，首次验收有效期至 2026-11-27

API、Scheduler、Web、Caddy、PostgreSQL、Redis 和 MinIO 七个长期服务均通过 Docker 健康检查。生产 HTTPS 验收结果：健康接口 200、登录 200、会话 200、退出 204。

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

初始管理员邮箱是 `admin@aiwise.top`。随机初始密码仅保存在服务器 `/root/yunling-initial-admin.txt`，权限为 `0600 root:root`；`deploy/.env` 中的全部 `YUNLING_BOOTSTRAP_*` 临时变量已删除。

登录并把凭据保存到受控密码管理器后，应删除服务器上的初始凭据文件：

```bash
sudo rm -f /root/yunling-initial-admin.txt
```

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
