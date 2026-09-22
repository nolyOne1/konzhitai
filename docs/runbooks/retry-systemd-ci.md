# 隔离 systemd 与重试验收

本流程属于 `.github/workflows/ci.yml` 的独立 `systemd-isolated` Job；不替代现有五项 CI，也不触发发布。仅允许 GitHub 托管的一次性 Ubuntu VM，不使用 self-hosted Runner、生产密钥、注册令牌或外部控制面地址。

## 覆盖范围

- 本地嵌入 PostgreSQL 和 miniredis：失败/超时自动重试、退避、次数上限、并发去重、取消与停用保护、历史事件保护、旧租约回收和调度器重建恢复。
- 真实 systemd：使用仓库中的 `yunling-run@.service` 与 polkit 规则，测试进程以 `yunling-agent` 运行，脚本以 `yunling-runner` 运行。验证退出码 0、退出码 7、取消、超时，以及后两者的子进程终止。
- 持久记录：脚本不能读取 agent 私有 claim；结束后删除测试脚本缓存，启动独立测试进程重放同一运行，事件必须完全一致，启动计数仍为 1，冲突令牌必须拒绝。

不声称覆盖代理进程被 SIGKILL 时的恢复、断电、跨节点恰好一次、网络断线恢复或生产性能。现有执行记录不完整时的 fail-closed 用例继续由普通 executor 测试覆盖。此 Job 的数据库重试测试与 systemd 执行测试是分层验证，不是完整控制平面端到端测试。

## 安全与失败行为

`deploy/agent/systemd_ci_test.sh` 检查 GitHub 托管环境、root、Linux、PID 1 为 systemd、产物目录位于 Runner 临时目录，以及不存在云令账户、安装路径或单元；不满足时停止，不覆盖既有安装。

脚本只创建一次性测试账户，安装测试二进制、执行单元模板和 polkit 规则；不安装或启动代理守护服务，不执行注册或生产安装器。测试调用清空环境，减少继承 CI 环境变量。测试完毕由 GitHub 销毁整台临时 VM；测试本身只停止本轮具体单元，不递归清理系统目录。

缺少 systemd/polkit、测试名称不匹配、权限不足或执行超时均失败，不允许 Skip 或 continue-on-error。Job 限时 15 分钟，systemd 测试限时 3 分钟。失败输出保留在 Actions 步骤日志，不上传凭据、运行目录或发布包。

## 运行与结果判定

先确认提交范围并提交/推送，再运行或等待此分支对应的 CI。检查 “隔离 systemd 与重试验收” Job：数据库测试和四个真实执行子用例全部 PASS 才算本项通过。仅交叉编译成功、YAML 校验通过或未包含本地改动的旧 CI 成功，都不能算实机验收成功。

本地 Windows 可以运行静态契约测试、Bash 语法检查及带 `systemdintegration` 标签的 Linux 测试二进制交叉编译；不能执行真实 systemd 部分。不要将此脚本复制到生产机器运行，也不要伪造环境变量绕过隔离检查。
