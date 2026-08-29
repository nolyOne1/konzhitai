# 云令生产部署与恢复手册

本目录提供从零部署的单机控制面：Caddy、中文 Web 控制台、API、调度器、PostgreSQL、Redis 和 MinIO。只有 Caddy 映射宿主机的 80/443，其他服务仅在 Docker 内部网络通信。

MinIO 固定到修复安全问题的 `RELEASE.2025-10-15T17-29-55Z`。该版本官方不提供预构建容器，部署文件会按照官方发布说明从固定源码标签编译镜像；构建使用腾讯云 Go 模块镜像并通过 Go 校验数据库验证内容，不要改回更早的历史容器标签。

云令 Go 服务镜像也使用相同的腾讯云模块镜像和 Go 校验数据库，以避免中国大陆服务器无法访问默认模块代理时构建失败。

## 一、部署前准备

控制面服务器建议至少 4 核 CPU、8 GB 内存和 80 GB SSD，并安装 Docker Engine 与 Compose v2。为控制面域名添加指向腾讯云公网 IP 的 A/AAAA 记录。

腾讯云安全组只开放：

- TCP 80、443：面向需要访问控制台和执行服务器的来源；
- TCP 22：只允许固定运维出口 IP，禁止 `0.0.0.0/0`；
- 不开放 5432、6379、9000、8080。

执行服务器只需主动出站访问控制面 443。SSH 也应限制到固定运维出口 IP，不需要向控制面开放任何脚本执行端口。

## 二、创建配置和主密钥

在仓库根目录执行：

```bash
cp deploy/.env.example deploy/.env
mkdir -p deploy/secrets
openssl rand -base64 32 > deploy/secrets/master.key
chmod 600 deploy/.env
chown 10001:10001 deploy/secrets/master.key
chmod 400 deploy/secrets/master.key
```

控制面镜像固定以 UID/GID 10001 运行，上述归属和权限使 API 能读取主密钥，同时阻止宿主机其他普通用户读取。若当前不是 root，请给 `chown`、`chmod` 加 `sudo`；备份或轮换主密钥也应使用受控的 root 运维流程。

编辑 `deploy/.env`，至少替换域名、TLS 邮箱和所有“请替换”值。数据库、Redis 和 MinIO 密码建议分别使用 `openssl rand -hex 32` 生成，十六进制内容也不会破坏数据库连接 URL；三项密码不要复用。主密钥必须离线备份；丢失主密钥将无法解密系统中的敏感参数。

先检查配置：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml config --quiet
```

## 三、启动控制面

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
```

首次创建 PostgreSQL 数据卷时会按顺序执行全部 `*.up.sql` 迁移；MinIO 初始化任务会创建私有脚本桶。已有数据卷不会重复执行初始化脚本，升级前应先备份并单独执行新增迁移。

检查健康状态：

```bash
curl --fail --show-error https://你的域名/api/health
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 api scheduler caddy
```

## 四、初始化管理员

`bootstrap` 是显式的一次性命令。首次运行会创建四种内置角色和管理员；对同一邮箱重复运行会重置该账号密码。

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml --profile tools run --rm bootstrap
```

登录成功后，立即从 `deploy/.env` 删除 `YUNLING_BOOTSTRAP_PASSWORD`，并再次执行 `chmod 600 deploy/.env`。不要让初始化密码长期留在服务器磁盘或命令历史中。

## 五、接入京东云或其他执行服务器

先通过 HTTPS 登录并创建仅显示一次、十分钟有效的注册令牌。当前可使用 API 完成：

```bash
curl --fail --show-error --cookie-jar /tmp/yunling-cookie \
  -H 'Content-Type: application/json' \
  -d '{"email":"管理员邮箱","password":"管理员密码"}' \
  https://你的域名/api/auth/login

curl --fail --show-error --cookie /tmp/yunling-cookie \
  -H 'Content-Type: application/json' \
  -d '{"name":"京东云执行节点-1","cloud_provider":"京东云","region":"填写地域","labels":{"用途":"批处理"}}' \
  https://你的域名/api/servers/enrollment-tokens

rm -f /tmp/yunling-cookie
```

执行服务器需要 systemd 240 或更高版本，并安装 polkit；推荐 Debian 12/13、Ubuntu 22.04/24.04 或同等的新版本发行版。在可信构建机生成 Linux 代理并校验后再传到执行服务器：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/yunling-agent ./cmd/agent
sha256sum bin/yunling-agent
scp bin/yunling-agent deploy/agent/install.sh deploy/agent/yunling-agent.service \
  deploy/agent/yunling-run@.service deploy/agent/50-yunling-agent.rules \
  运维账号@执行服务器:/tmp/
```

在执行服务器上使用一次性令牌安装：

```bash
sudo env \
  YUNLING_CONTROL_URL=https://你的域名 \
  YUNLING_ENROLLMENT_TOKEN=仅显示一次的令牌 \
  bash /tmp/install.sh /tmp/yunling-agent
sudo journalctl -u yunling-agent.service -n 100 --no-pager
```

安装器不会保存一次性令牌；注册成功后会清理令牌环境并重启代理，只保留权限为 0600 的独立代理凭据。控制连接以无登录权限的 `yunling-agent` 账号运行；polkit 只允许它启停 root 预装的 `yunling-run@.service` 实例，不能创建任意临时单元。所有业务脚本固定以无登录权限的 `yunling-runner` 账号运行，调度器按任务声明值预留资源，模板另设 16 核、64 GiB、1024 个进程和 7 天的节点级硬上限。运行中的标准输出和错误日志会从独立文件持续回传。

不要把云服务器 root 密码写入云令配置、脚本参数、环境文件或部署文档。若安装期间曾使用临时 root 密码，应在代理验证在线后立即轮换，随后改用密钥登录和专用运维账号。

以后新增服务器时重复“创建一次性注册令牌—安装代理”即可。脚本发布后由中央生成不可变版本，兼容服务器会共享同一内容校验值并自动同步；发生校验漂移的节点会被阻止运行该版本。

## 六、日常运维

查看服务：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs -f --tail=200 api scheduler
```

更新程序：

```bash
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/docker-compose.yml build --pull
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

更新前先执行测试与备份。不要使用 `docker compose down -v`，该命令会删除数据库、对象存储、Redis 和 Caddy 数据卷。

## 七、备份

至少备份以下三项，并把副本保存到另一台服务器或对象存储：

1. PostgreSQL 逻辑备份；
2. MinIO `minio_data` 卷中的脚本对象；
3. `deploy/secrets/master.key` 和 `YUNLING_MASTER_KEY_VERSION` 的离线加密副本。

数据库备份示例：

```bash
mkdir -p backups
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' \
  > backups/yunling-$(date +%F-%H%M).dump
```

MinIO 建议使用 `mc mirror` 同步到独立备份桶，或在停止写入后使用基础设施提供的卷快照。Redis 仅保存可重建的调度租约和队列加速状态，但仍启用了 AOF 以缩短普通重启恢复时间。

## 八、灾难恢复演练

在隔离测试环境中执行，禁止直接拿生产环境做首次演练：

1. 准备同版本代码、Compose 配置和新的空数据卷；
2. 恢复主密钥文件，执行 `chown 10001:10001 deploy/secrets/master.key` 和 `chmod 400 deploy/secrets/master.key`；
3. 启动 PostgreSQL、Redis、MinIO，恢复数据库和脚本对象；
4. 启动 API、调度器、Web 和 Caddy；
5. 验证管理员登录、敏感参数解密、脚本版本下载；
6. 清空测试 Redis 后重启调度器，确认运行中资源租约被数据库恢复；
7. 让测试代理断线并重连，确认匹配执行令牌的任务恢复为“运行中”；
8. 创建一个资源暂时不足的任务，确认显示“排队等待”，释放资源后自动变为“已分配”；
9. 检查审计日志和告警，再记录恢复时间目标与数据恢复点。

数据库恢复示例：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T postgres \
  sh -c 'dropdb -U "$POSTGRES_USER" --if-exists "$POSTGRES_DB" && createdb -U "$POSTGRES_USER" "$POSTGRES_DB"'
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T postgres \
  sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists' \
  < backups/指定备份.dump
```

恢复完成前不要接入生产代理；确认数据库、对象存储和主密钥属于同一备份时间点后，再逐台恢复代理连接。
