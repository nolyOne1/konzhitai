# 云令生产部署记录

## 当前部署

- 部署日期：2026-08-29（Asia/Shanghai）
- 控制面地址：`https://aiwise.top`
- 腾讯云主机：`134.175.131.19`
- 部署目录：`/opt/yunling`
- 部署提交：`8d79a2d`
- TLS：Caddy 自动签发 Let's Encrypt 证书，首次验收有效期至 2026-11-27

API、Scheduler、Web、Caddy、PostgreSQL、Redis 和 MinIO 七个长期服务均通过 Docker 健康检查。生产 HTTPS 验收结果：健康接口 200、登录 200、会话 200、退出 204。

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
