# 云令生产发布与回滚手册

本手册适用于 `https://aiwise.top` 控制面的版本化发布。日常更新只能通过 GitHub Actions 的“云令生产发布”工作流发起；禁止在生产机执行 `git pull`、现场构建镜像或用浮动的 `latest` 标签更新。

## 一、不可突破的边界

- 发布对象只有 `api`、`scheduler`、`web`、`ops` 四个应用服务。PostgreSQL、Redis、MinIO、Caddy 及所有命名卷不得被重建或删除。
- 发布清单只接受 `ghcr.io/nolyone1/yunling-services`、`yunling-web`、`yunling-ops` 的 `sha256` 摘要引用，禁止使用 `latest`、分支名或普通标签。
- 普通控制面发布不执行数据库迁移，不更新执行节点代理。迁移树、部署契约或代理锁摘要不同时，候选版本必须在更新容器前被拒绝；数据库迁移只有完成第四节或第四节补充的候选绑定 rollout 后才能获得一次精确授权。
- 代理发布内容保存在 `yunling_agent_releases` 命名卷，API 只读挂载。不得用普通发布替换或写入该卷。
- 永远禁止 `docker compose down -v`、删除生产命名卷、关闭 SSH 主机指纹校验、密码 SSH 发布、`curl -k` 或把私钥/飞书密钥写入仓库。

## 二、发布状态与根目录

`yunling-release` 只输出一个严格 JSON 结果。`status` 只有 `succeeded` 或 `failed`；`rollback_status` 只有以下值：

- `not-required`：成功，或在更新应用容器前已失败，无需自动回滚；
- `succeeded`：新容器更新后失败，已自动恢复上一个成功版本；
- `failed`：自动恢复也失败，必须立即人工处置。

下列文件全部位于生产机 `/opt/yunling/releases` 并必须保持 root-only：

- `current.json`：当前最后成功版本；
- `previous.json`：上一个成功版本；
- `<候选运行编号>/release-manifest.json` 和 `successful.json`：历史候选与成功标记；
- `migration-baselines/<候选运行编号>.json`：完成数据库迁移后写入的不可变、候选绑定基线；
- `audit.jsonl`：每次发布或回滚的追加审计记录；
- `diagnostics/<diagnostic_id>/`：更新后失败时保存的限长、脱敏容器状态与日志。

当前应用镜像覆盖文件为 `/opt/yunling/deploy/docker-compose.release.yml`。不要手工编辑这些状态文件或覆盖文件。公开健康检查固定为 `https://aiwise.top/healthz` 和 `https://aiwise.top/api/health`。

## 三、选择并核对候选版本

1. 在 GitHub 的“操作”页面打开“云令候选版本”，只选择结论为成功、分支为 `main` 的运行。候选运行自身的事件是 `workflow_run`，其内部授权门禁必须已验证上游“云令 CI”是同仓库、`main` 的成功 `push`。
2. 记录候选运行编号、完整 40 位源提交 SHA 和运行链接。下载名为 `yunling-release-<完整 SHA>` 的唯一产物。
3. 在可信工作站安装从该完整 SHA 审阅并编译的 `yunling-release`，确认它位于 `PATH`，然后做只读核对：

```bash
gh run view "<候选运行编号>" --json databaseId,headSha,headBranch,event,conclusion,url
mkdir -m 700 candidate-check
gh run download "<候选运行编号>" -n "yunling-release-<完整 SHA>" -D candidate-check
cd candidate-check
sha256sum -c SHA256SUMS
yunling-release manifest validate --input release-manifest.json
jq '{candidate_run_id,repository_id,source_sha,images,compatibility}' release-manifest.json
gh attestation verify "oci://$(jq -r '.images.services' release-manifest.json)" --repo nolyOne1/konzhitai
gh attestation verify "oci://$(jq -r '.images.web' release-manifest.json)" --repo nolyOne1/konzhitai
gh attestation verify "oci://$(jq -r '.images.ops' release-manifest.json)" --repo nolyOne1/konzhitai
gh attestation verify yunling-release-bootstrap.tar.gz --repo nolyOne1/konzhitai
```

`candidate_run_id`、下载的运行编号和产物名必须一致；`source_sha` 必须是预期的 `main` 提交；三个镜像必须是允许的 GHCR 名称和完整摘要。任何一项不一致都不得进入审批。

## 四、成员生命周期迁移 13 的受控 rollout

本节只适用于首次引入 `000013_member_lifecycle`、且当前生产迁移版本仍为 `12` 的候选。普通发布仍不会执行迁移：候选迁移摘要与 `current.json` 不同时，标准发布必须先以 `ErrIncompatibleRelease` 拒绝。不得手工修改 `current.json`、候选清单或摘要来绕过门禁，也不得把本节当成以后迁移的通用授权。

1. 按第三节核验唯一候选产物、`SHA256SUMS` 及四份来源证明。候选 bootstrap 包必须包含同一源提交构建的 `yunling-release-linux-amd64` 和完整 `migrations/`；清单中的全树摘要会在执行前、执行后各核对一次，v1–v12 的历史树摘要还必须与当前生产基线完全一致。
2. 建立单独的迁移变更审批并暂停该时段的其他发布。确认旧版应用仍健康，从“运维中心—备份与恢复”手动创建备份，等待本机和 COS 均成功，再对该备份执行隔离恢复核验。只有恢复核验为 `succeeded` 且报告迁移版本 `12` 才可继续；记录该备份运行 UUID，不能使用仅本机成功、失败、过期或其他版本的恢复点。
3. 在审批中记录 `users` 表规模、低流量维护窗口和可接受的锁等待。迁移使用事务内普通 `CREATE INDEX`，会对 `users` 取得锁；若现有规模无法接受该锁窗口，停止本候选，另行设计并复核在线索引迁移，不得现场把候选 SQL 改成 `CONCURRENTLY`。
4. 把已经验证的 `release-manifest.json`、`SHA256SUMS` 和 bootstrap 包传入生产机的 root-only 临时目录。再次执行 `sha256sum -c SHA256SUMS`，先检查归档成员没有绝对路径、`..` 或符号链接，再解压到权限 `0700` 的新目录。不要覆盖 `/opt/yunling` 中的程序、Compose 或迁移文件。
5. 在旧版应用仍运行时，从该候选的 root-only 解压目录显式执行一次：

```bash
cd "/root/yunling-migration-<候选运行编号>"
sudo ./deploy/release/yunling-release-linux-amd64 migration apply \
  --manifest "/root/yunling-migration-<候选运行编号>/release-manifest.json" \
  --migrations "/root/yunling-migration-<候选运行编号>/migrations" \
  --actor "<已审批操作者>" \
  --recovery-point "<已成功隔离恢复核验的备份运行 UUID>"
```

`--actor` 只填写审批记录中的字母、数字或连字符身份。程序与标准发布共用 `/opt/yunling/releases/release.lock`；它先验证候选来源、完整迁移树、不可变 v1–v12 历史、恢复点及数据库版本，再在显式事务中执行 v13。随后必须确认版本 `13`、两个字段、索引、用户数和活动会话数，最后才以排他创建方式写入 `migration-baselines/<候选运行编号>.json`。任何失败都不得手工补写基线；修复原因后只能用同一候选和同一恢复点重试。

6. 只读核对数据库版本为 `13`、基线文件内容绑定当前目标、候选运行编号、完整源 SHA、迁移前后摘要、迁移文件摘要、恢复点和操作者。再确认现有管理员会话可用、旧账号可重新登录；不要为了验收创建生产测试账号。
7. 基线成功后再按第五节发起同一候选的标准生产发布。发布成功后由管理员验收成员创建、首次登录强制改密、停用/恢复和旧密码失效，并把真实结果写入 `PRODUCTION.md`；本手册和代码中的测试结果不得记作生产成功。

失败处置保持保守：迁移事务失败会自动回滚且不会写基线，继续运行旧版并停止发布；若数据库已到 v13 但后置核验或基线写入失败，旧版仍与新增字段兼容，应先保留现场、查明原因并重试受控命令。应用发布失败则使用标准自动/人工应用回滚，但保留 v13 的加法式结构。不得执行 `000013_member_lifecycle.down.sql` 作为常规回滚，因为它会删除生命周期数据；只有经事故审批、确认必须恢复整个 v12 恢复点时，才按灾难恢复流程整体恢复数据库，并复核恢复后的版本与应用目标。

## 四、补充：版本 12 到 15 的代理升级管理发布

包含迁移 14、15 的完整候选使用同一个 `migration apply` 命令。该路径只支持当前发布基线为版本 12，数据库为版本 12（首次执行）或版本 15（事务已提交后的恢复）。版本 13、14 的生产数据库以及包含版本 16 及以后的候选会被拒绝，需要独立迁移方案。既有仅包含版本 13 的候选仍使用上一节路径。

沿用上一节的备份、COS 成功、隔离恢复核验为版本 12、候选摘要和历史迁移核对步骤，并在维护窗口停止会改变用户或会话数量的操作。先核实实际数据库版本及当前发布基线；本手册不是生产已处于版本 12 的证明。

程序按顺序将 13、14、15 放入同一个事务执行。失败时全部回滚，不写发布基线。成功后核对版本、成员字段、代理升级五张表、服务器能力字段、安装命令字段和关键唯一索引；还核对用户和活动会话数量，最终发布绑定版本 15 的基线。`migration_file_sha256` 对此路径记录三个 up SQL 按顺序用换行连接后的 SHA-256；完整迁移树摘要继续覆盖全部 up/down 文件。

若事务提交后程序中断，使用同一候选、恢复点和操作者重试：数据库已为 15 时跳过 SQL，只进行结构验证、摘要复核与基线发布。任何核验失败都不得手工补基线。应用回滚保留版本 15 的数据库结构，不执行 13–15 的 down SQL。

当前入口仍要求部署契约和代理锁与生产基线一致。若候选因 Compose 或代理目录契约变更被拒绝，这个迁移授权不会放行那些差异，必须另外准备相应发布配置变更。完成迁移不代表代理已升级；代理服务安装与升级计划应独立验收。

## 五、人工审批发布

1. 打开“云令生产发布”，点击“运行工作流”，选择 `main`。
2. `operation` 选 `deploy`，`target_id` 填写纯十进制的候选运行编号，不是提交 SHA。
3. 无生产密钥的预检必须先成功：它核对候选运行、唯一产物、摘要、清单和四份来源证明。
4. 工作流停在 `production` 环境审批时，审核人再次对照运行编号、SHA 和摘要。不符合预期就选“拒绝”并填写原因；符合才选“批准并部署”。
5. 完成后检查生产 Job 结论、飞书中文卡片和两个公开健康地址。通知步骤可以单独失败，但不能改写真实发布退出码。

工作流使用固定并发组 `production-release` 且不取消进行中的发布。后续请求必须排队，不要强制终止正在更新或回滚的运行。

## 六、自动回滚与人工回滚

更新四个应用容器后，任一容器健康、内部探测或公开探测失败，程序立即写回上一个成功版本的覆盖配置并重新检查健康。失败候选不会成为 `current.json`。

需要主动恢复历史成功版本时，仍使用“云令生产发布”：

- `operation` 选 `rollback`；
- `target_id` 填生产机已记录为成功的十进制候选运行编号，或填 `bootstrap` 恢复首次导入的本地基线；
- 通过同一个 `production` 环境人工审批。

人工回滚不依赖已过期的 Actions 产物，但目标必须存在于生产机 root-only 历史中且有 `successful.json`。不得用镜像标签、手工改 Compose 或删卷的方式“回滚”。

## 七、读取审计与诊断

使用腾讯云网页终端或已批准的运维连接执行只读检查：

```bash
cd /opt/yunling
sudo jq . releases/current.json
sudo jq . releases/previous.json
sudo tail -n 20 releases/audit.jsonl
sudo find releases/diagnostics -mindepth 1 -maxdepth 1 -type d -printf '%f\n'
sudo find "releases/diagnostics/<diagnostic_id>" -maxdepth 1 -type f -printf '%f\n'
sudo sed -n '1,200p' "releases/diagnostics/<diagnostic_id>/compose-ps.log"
```

`diagnostic_id` 必须来自发布结果或审计记录，不要猜测路径。诊断文件已做关键字脱敏和总量限制，仍不得整份粘贴到公开聊天或工单。

## 八、首次启用发布入口

这是一次性操作，必须在腾讯云网页终端中由 root 执行，且先备份 `/opt/yunling/deploy/docker-compose.yml`、当前四个应用容器的镜像 ID 和代理发布清单。

1. 按第三节验证候选产物和 `yunling-release-bootstrap.tar.gz` 来源证明。
2. 只把已验证的 bootstrap 包上传到 root-only 临时目录，解压前检查成员不含绝对路径或 `..`。解压后进入该目录，后续命令都从这里执行。
3. 备份现有 Compose 文件后，将包内经验证的 `deploy/docker-compose.yml` 用 root 安装到 `/opt/yunling/deploy/docker-compose.yml`，所有者为 root、权限 `0644`。这一步只更新配置文件，不能执行 `up`、`down` 或删卷。
4. 在服务器控制台核对 SSH 主机 Ed25519 指纹；不得把未核对的 `ssh-keyscan` 结果直接当成信任根。
5. 为发布专用密钥生成新的 Ed25519 密钥对，把公钥放在 root 所有、`0600` 的单行文件中，然后执行包内安装器：

```bash
cd "<已验证的 root-only 解压目录>"
sudo install -o root -g root -m 0600 /opt/yunling/deploy/docker-compose.yml \
  "/opt/yunling/deploy/docker-compose.yml.before-release-$(date +%Y%m%d-%H%M%S)"
sudo install -o root -g root -m 0644 deploy/docker-compose.yml \
  /opt/yunling/deploy/docker-compose.yml
sudo sh deploy/release/install.sh --public-key-file /root/yunling-deploy.pub
sudo /usr/local/sbin/yunling-release bootstrap
sudo /usr/local/sbin/yunling-release preflight
```

`bootstrap` 会锁定当前四个应用镜像，核对当前 API 容器里的代理包，将其发布到只读命名卷，并创建 `bootstrap`、`current`、覆盖文件和审计基线。任一摘要、文件类型或健康检查不一致都必须停止，不得手工补状态。

## 九、轮换或紧急停用发布密钥

专用私钥只存在 GitHub `production` 环境秘密 `PRODUCTION_SSH_PRIVATE_KEY`。服务器只保存公钥，账号固定为 `yunling-deploy`，入口被限制为 `/usr/bin/sudo -n /usr/local/sbin/yunling-release execute`。

轮换时必须保留旧公钥直到新密钥完成一次经审批的有效发布或回滚：

1. 在受控工作站用 `ssh-keygen -t ed25519 -a 100` 生成专用新密钥，不覆盖旧文件。
2. 在服务器网页终端把新公钥以同样的 `restrict,command="/usr/bin/sudo -n /usr/local/sbin/yunling-release execute"` 选项追加到 `/var/lib/yunling-deploy/.ssh/authorized_keys`，保持用户 `yunling-deploy`、权限 `0600`。
3. 在 GitHub `production` 环境替换私钥，不更改固定 SSH 用户和命令。
4. 完成一次人工审批的有效操作并核对审计后，才从 `authorized_keys` 删除旧公钥。
5. 删除工作站临时私钥副本，在密钥台账中记录轮换时间和指纹。

需要紧急停用自动发布时，在腾讯云网页终端把 `authorized_keys` 备份到 root-only 路径后清空原文件，同时删除或替换 GitHub `production` 环境私钥。不要关闭整台主机的 SSH，也不要放宽 `sshd` 或 sudoers。

SSH 主机密钥轮换时，必须先在云厂商控制台核对新指纹，再替换 `PRODUCTION_SSH_KNOWN_HOSTS`。禁止设置 `StrictHostKeyChecking=no` 或 `UserKnownHostsFile=/dev/null`。

## 十、故障处理

### 候选、GHCR 或来源证明失败

预检在生产密钥可见前失败，不得批准。核对运行编号、候选产物名、是否过期、三个摘要和四份 attestation。不要改用标签拉取，也不要手动重做清单。

### SSH 失败

确认安全组只允许 GitHub-hosted Runner 需要的路径或组织方案，核对 `PRODUCTION_SSH_HOST`、新私钥和经带外确认的 `PRODUCTION_SSH_KNOWN_HOSTS`。不得临时开启密码登录或关闭主机校验。在服务器终端只读检查 `sshd -T`、公钥文件所有者/权限、sudoers 校验和系统认证日志。

### 健康检查失败

先看结果的 `rollback_status`。为 `succeeded` 时，确认公网已恢复上一版，再使用 `diagnostic_id` 读取脱敏诊断。不要在发布程序运行时手工重启或改覆盖文件。

### 自动回滚失败

将事件视为高优先级：暂停新审批，使用云厂商终端检查四个应用容器、基础设施健康、磁盘/内存、`current.json`、`previous.json`、覆盖文件和诊断。不得执行 `down -v`。确认故障根因和当前服务状态后，再由另一个已审批的 `rollback` 运行恢复明确的历史成功目标。

### 飞书通知失败

工作流摘要会记录 `notification_failed`，但最终结论仍以真实发布退出码为准。先从 Actions、公开健康地址和生产审计确认发布结果，再在 GitHub `production` 环境轮换 `PRODUCTION_FEISHU_WEBHOOK` 和 `PRODUCTION_FEISHU_SIGNING_SECRET`。不要在日志中打印完整 Webhook 或签名密钥。

## 十一、GitHub `production` 环境配置清单

环境必须要求人工审核，只允许受保护的 `main` 分支，禁止管理员绕过。只在该环境保存以下秘密：

- `PRODUCTION_SSH_HOST`；
- `PRODUCTION_SSH_PRIVATE_KEY`；
- `PRODUCTION_SSH_KNOWN_HOSTS`；
- `PRODUCTION_FEISHU_WEBHOOK`；
- `PRODUCTION_FEISHU_SIGNING_SECRET`。

工作流的预检 Job 不得引用上述任何秘密。部署 Job 固定使用 `BatchMode=yes`、`IdentitiesOnly=yes`、`StrictHostKeyChecking=yes`、`PasswordAuthentication=no` 和独立 `known_hosts`，远程命令只能是 `execute`。
