# 云令脚本调度中心

云令是面向团队的全中文多服务器脚本管理系统，用于集中管理脚本版本、普通参数与敏感参数、手动和定时执行、任务启停、资源感知调度、排队等待、运行状态及实时日志。

核心运行规则：任务创建后先进入中央队列；调度器根据服务器在线状态、标签、运行环境、并发数、CPU、内存、磁盘和脚本同步状态选择节点。没有可用节点时任务保持排队，服务器上线或资源释放后自动重试分配。脚本由中央对象存储保存不可变版本，并按内容校验值同步到各执行服务器。

## 目录

- `cmd/api`：认证、管理接口、代理长连接和定时计划扫描
- `cmd/scheduler`：资源租约、优先级队列和自动分配
- `cmd/agent`：执行服务器代理、脚本同步、运行与日志上报
- `apps/web`：全中文管理控制台
- `migrations`：PostgreSQL 数据库结构
- `deploy`：Docker Compose、Caddy、代理安装和恢复文档
- `tests/integration`：调度恢复与代理重连集成测试

## 持续集成

GitHub Actions 会并行运行五项完整质量门禁，但不会部署或连接生产环境。检查名称、失败诊断和 `main` 分支保护要求见 [docs/CI.md](docs/CI.md)。

## 本地验证

需要 Go 1.27、Node.js 24 和 npm。PostgreSQL 相关测试会自动启动临时数据库。

```bash
npm ci
make test
make test-integration
npx playwright install chromium
make test-e2e
make build
```

Windows 未安装 `make` 时，可以直接执行 Makefile 中对应的 Go 与 npm 命令。

## 部署

生产部署采用 Caddy 作为唯一入口，只对外开放 80/443；API、调度器、PostgreSQL、Redis 和 MinIO 均不映射宿主机端口。完整的初始化、腾讯云安全组、执行服务器接入、备份和恢复步骤见 [deploy/README.md](deploy/README.md)，当前腾讯云上线状态见 [deploy/PRODUCTION.md](deploy/PRODUCTION.md)。

不要把 `.env`、主密钥、管理员初始化密码、代理一次性注册令牌或代理凭据提交到仓库。
