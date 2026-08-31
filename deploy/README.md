# 云令生产部署与恢复手册

本目录提供从零部署的单机控制面：Caddy、中文 Web 控制台、API、调度器、运维进程、PostgreSQL、Redis 和 MinIO。只有 Caddy 映射宿主机的 80/443，其他服务仅在 Docker 内部网络通信。运维进程不挂载 Docker Socket、数据库卷或对象存储卷，只能通过专用只读账号导出数据，并把加密快照写入自己的数据卷和腾讯云 COS。

MinIO 固定到修复安全问题的 `RELEASE.2025-10-15T17-29-55Z`。该版本官方不提供预构建容器，部署文件会按照官方发布说明从固定源码标签编译镜像；构建使用腾讯云 Go 模块镜像并通过 Go 校验数据库验证内容，不要改回更早的历史容器标签。

云令 Go 服务镜像也使用相同的腾讯云模块镜像和 Go 校验数据库，以避免中国大陆服务器无法访问默认模块代理时构建失败。

## 一、部署前准备

控制面服务器建议至少 4 核 CPU、8 GB 内存和 80 GB SSD，并安装 Docker Engine 与 Compose v2。为控制面域名添加指向腾讯云公网 IP 的 A/AAAA 记录。

腾讯云安全组只开放：

- TCP 80、443：面向需要访问控制台和执行服务器的来源；
- TCP 22：只允许固定运维出口 IP，禁止 `0.0.0.0/0`；
- 不开放 5432、6379、9000、8080。

执行服务器只需主动出站访问控制面 443。SSH 也应限制到固定运维出口 IP，不需要向控制面开放任何脚本执行端口。

## 二、创建配置、主密钥与备份凭据

在仓库根目录执行：

```bash
cp deploy/.env.example deploy/.env
mkdir -p deploy/secrets
chmod 600 deploy/.env
chown root:root deploy/.env deploy/secrets
chmod 700 deploy/secrets
```

编辑 `deploy/.env`，至少替换域名、TLS 邮箱、数据库/Redis/MinIO 密码以及 COS endpoint、地域、专用备份桶。COS 桶应单独创建，开启服务端加密和版本控制；不要与脚本对象桶复用。所有“请替换”值都必须消失。数据库、Redis 和 MinIO 密码建议分别使用 `openssl rand -hex 32` 生成，三项密码不要复用。

新部署使用 root 生成主密钥；已有生产环境必须复制当前正在使用的主密钥，绝对不能重新生成：

```bash
openssl rand -base64 32 > deploy/secrets/yunling-master-key
chown root:root deploy/secrets/yunling-master-key
chmod 600 deploy/secrets/yunling-master-key
```

在腾讯云 CAM 创建只能访问该专用备份桶所需路径的 API 密钥。通过受控密码管理器或 root 编辑器分别把 SecretId、SecretKey 写入 `deploy/secrets/cos-secret-id`、`deploy/secrets/cos-secret-key`。禁止把这两项密钥粘贴到聊天、工单、Shell 命令参数或 `deploy/.env`。写入后执行：

```bash
chown root:root deploy/secrets/cos-secret-id deploy/secrets/cos-secret-key
chmod 600 deploy/secrets/cos-secret-id deploy/secrets/cos-secret-key
sudo deploy/initialize-ops-secrets.sh
```

初始化脚本会生成互不复用的 Restic、数据库备份/校验和 MinIO 只读账号密钥，但拒绝覆盖任何已有文件。脚本还会创建 `/root/yunling-recovery-key.txt`，内容不会输出到终端。立即将该文件保存到离线密码库；确认离线副本可读后，安全删除服务器上的文件并验证不存在：

```bash
sudo rm -f /root/yunling-recovery-key.txt
sudo test ! -e /root/yunling-recovery-key.txt
```

主密钥与 Restic 密码都属于灾难恢复必需材料，丢失任意一项都无法完整恢复。宿主机源密钥保持 `root:root`、权限 `0600`；一次性离线容器会把全部运维凭据写入 Ops 专用卷，同时分别生成只含主密钥、数据库备份账号密钥、MinIO 备份账号密钥的三个最小权限卷，副本统一使用 UID/GID 10001、权限 `0400`。因此 API 和初始化服务都无法读取无关密钥：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml --profile tools run --rm ops-secrets-init
```

先检查配置：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml config --quiet
```

## 三、启动控制面

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
```

首次创建 PostgreSQL 数据卷时会按顺序执行全部 `*.up.sql` 迁移。启动过程还会幂等创建只读数据库备份账号、只能创建隔离验证库的恢复账号、私有脚本桶及其只读备份账号。已有数据卷不会重复执行数据库迁移，升级前应先创建恢复点并单独执行新增迁移。

检查健康状态：

```bash
curl --fail --show-error https://你的域名/api/health
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 api scheduler ops caddy
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
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs -f --tail=200 api scheduler ops
```

更新程序：

```bash
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/docker-compose.yml build --pull
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

更新前先执行测试与备份。不要使用 `docker compose down -v`，该命令会删除数据库、对象存储、Redis 和 Caddy 数据卷。

### 飞书通知配置

在目标飞书群中添加“自定义机器人”，使用 V2 Webhook，并务必启用签名校验。由管理员在云令控制台进入“运维中心—飞书通知”，同时录入 Webhook 和签名密钥，保存后先发送测试消息；控制台只会继续显示机器人 token 的脱敏尾号。

Webhook 和签名密钥属于生产凭据。只允许在控制台的加密表单中录入，禁止粘贴到终端命令或历史、工单、聊天记录、部署文档、环境文件和代码仓库。轮换时也必须在控制台同时提交新的 Webhook 与签名密钥。

测试消息成功后，确认 `ops` 服务健康，并检查其日志中没有持续重试：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps ops
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 ops
```

### 管理员改密功能上线

管理员改密涉及数据库加法迁移和会话撤销。生产升级必须按以下顺序执行，不得提前删除初始凭据文件。

1. 在当前生产版本仍正常运行时创建 PostgreSQL 备份：

```bash
cd /opt/yunling
mkdir -p backups
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' \
  > backups/yunling-before-password-change-$(date +%F-%H%M).dump
```

2. 拉取已经过测试的提交，先执行仅新增表和索引的迁移，再重建并启动服务：

```bash
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T postgres \
  sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  < migrations/000010_password_change_security.up.sql
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
```

3. 使用初始管理员凭据登录控制台，进入“运维中心—账号安全”，设置至少 12 位且不与旧密码相同的新密码，并保存到受控密码管理器。
4. 确认当前会话仍可访问；再打开无痕窗口，使用新密码重新登录成功，并确认旧密码登录失败。
5. 只有以上检查全部通过后，才删除服务器上的初始凭据文件，并立即验证文件已不存在：

```bash
sudo test -f /root/yunling-initial-admin.txt
sudo rm -f /root/yunling-initial-admin.txt
sudo test ! -e /root/yunling-initial-admin.txt
```

如果迁移、部署、改密或重新登录任一步失败，应保留初始凭据文件并停止后续操作，先使用备份和服务日志定位问题。

## 七、自动备份、COS 与保留策略

运维进程每天 00:30、06:30、12:30、18:30（Asia/Shanghai）自动执行以下流程：使用 `yunling_backup` 只读账号导出 PostgreSQL、使用 MinIO 只读账号镜像脚本对象、生成并校验清单、加密写入本机 Restic 仓库，再复制到 COS。每月 1 日 03:30 会从 COS 恢复到随机命名的隔离数据库并校验引用完整性，成功或失败都会删除隔离库。

本机成功但 COS 同步失败时，备份状态显示“仅本机成功”，系统只重试同一份已加密快照，不会重复导出数据库。状态异常、连续失败或恢复校验失败会进入运维告警和飞书通知。本机快照保留 7 天，COS 快照保留 30 天；删除策略由 Restic 执行，不直接删除业务卷。

管理员可进入“运维中心—备份与恢复”查看下一次自动备份、最近成功时间、COS 同步状态、历史记录和恢复校验结果，也可以手动发起备份或对指定 COS 恢复点执行隔离校验。页面请求带幂等键，重复点击不会创建重复任务。

腾讯云侧必须完成以下设置：

1. 专用备份桶与控制面同地域或明确接受跨地域费用；开启服务端加密、版本控制和访问日志；
2. CAM 密钥只授予指定桶与 `YUNLING_COS_PREFIX` 前缀的列举、读取、写入和删除快照权限，不授予其他云资源权限；
3. 配置生命周期规则清理已被 Restic 遗忘的历史对象版本，但保留窗口不得短于系统的 30 天周备份窗口；
4. COS SecretId/SecretKey 只存在于 root 源密钥文件、UID 10001 专用密钥卷和受控密码库，不写入日志或环境变量。

Redis 只保存可重建的调度租约和队列加速状态，已启用 AOF 以缩短普通重启恢复时间，但不属于恢复点的权威数据。

## 八、灾难恢复演练

在隔离测试环境中执行，禁止直接拿生产环境做首次演练：

1. 准备同一 Git 版本的代码、Compose 配置、新的空数据卷和离线恢复材料；
2. 恢复 `deploy/secrets/yunling-master-key` 与 `deploy/secrets/restic-password`，保持源文件 `root:root`、`0600`，再运行一次 `ops-secrets-init`；
3. 启动 PostgreSQL、Redis、MinIO 和运维进程，从控制台对目标 COS 恢复点发起隔离恢复校验；
4. 校验成功后，按审计记录中的快照 ID 恢复 PostgreSQL 和脚本对象，禁止混用不同恢复点；
5. 启动 API、调度器、Web 和 Caddy，验证管理员登录、敏感参数解密、脚本版本下载；
6. 清空测试 Redis 后重启调度器，确认运行中资源租约由数据库恢复；
7. 让测试代理断线并重连，确认匹配执行令牌的任务恢复为“运行中”；
8. 创建资源暂时不足的任务，确认显示“排队等待”，释放资源后自动变为“已分配”；
9. 检查审计日志和告警，记录实际恢复时间与数据恢复点，并销毁演练环境。

季度演练至少覆盖三种故障：COS 暂时不可用（本机备份成功且同一快照随后补传）、备份进程中途退出（租约超时后可接管且不产生重复快照）、恢复校验失败（隔离数据库仍被清理且飞书告警到达）。恢复完成前不要接入生产代理。
