# 自动重试与幂等本地检查

日期：2026-09-21。检查基线：`6573bec55155198abed865edb9536acebe3d0a73`，分支 `codex/agent-release-0.2.3`。

## 2026-09-22 隔离 CI 验收准备（尚未远程运行）

新增独立 `systemd-isolated` GitHub Actions Job、仅允许一次性 GitHub 托管 Linux 主机的测试夹具、真实 systemd 执行/独立进程重放用例，以及 CI 静态安全契约。详见 `docs/runbooks/retry-systemd-ci.md`。

本地 `go test ./tests/integration -run '^TestCI' -count=1`、Bash 语法检查、带 `systemdintegration` 标签的 Linux/amd64 测试二进制交叉编译通过。尚未提交、推送、触发 Actions 或执行真实 systemd 测试；这些本地结果不构成实机验收通过。此流程不使用生产账户、密钥、控制面或执行节点。

## 2026-09-22 续修：退避、旧租约与自动重试

继续在本地修复，未提交、未部署、未连接生产。保留以下 9 月 21 日修复记录和初始诊断作为历史证据。

- 退避使用固定间隔：下一实例的 `queued_at = max(重试创建时刻, 原实例 finished_at + backoff)`；对已确认消失的 unknown 实例，以首次确认的 `updated_at` 为起点。该时间表示允许进入资源排队的时刻，`created_at` 仍为实际创建时刻。最大等待时间从退避结束后计算；手动重试同样遵守退避。
- 调度入口先检查 `queued_at`，数据库 Assign 的行锁读取也检查时间，避免直接调用绕过；退避期间不预约资源、不触发脚本分发。重启后靠数据库排队实例恢复。
- 权威进程清单确认原进程消失时，在同一事务释放旧 PostgreSQL 租约；重试入口也在安全条件通过后补释放。非权威清单和仍在运行的匹配令牌不会释放。重复缺席报告不重复事件、不推迟退避起点。Redis 使用已有的每轮扫描释放机制，临时故障时保留数据库释放记录以便重试。
- 自动重试意图随新接收的 failed/timed_out 终态事件原子保存为服务端 payload 标记；调度器每轮最多消费 100 条。只有幂等、进程已确认结束、未超次数且任务定义启用的实例才会创建下一 attempt。手动与自动入口共用链锁和后继去重；子实例持久化即为消费凭据，进程重启后可再次扫描。
- 不自动重试 cancelled、expired、unknown、成功任务，也不重试已有 `run.cancel_requested` 事件的失败任务，避免取消与失败竞态导致重跑。任务停用会暂停尚未消费的重试意图；重新启用后可继续消费。已创建的 queued 重试遵循现有停用选项：勾选取消排队才取消。
- 历史终态事件没有自动重试标记，不补跑；历史事件重放也不会新增标记。无需数据库迁移，但 API 与 scheduler 需要配套更新；混用旧调度器会绕过新退避检查，因此不能按任意顺序滚动发布。

### 续修验证

Linux/amd64 的 agent、API、scheduler 交叉编译通过，`git diff --check` 通过。九个相关包完整本地测试全部通过：task、executor、agent、scheduler、dispatch、store/redis、logstream、cmd/agent、cmd/scheduler（`-count=1 -timeout=300s`）。原 9 个诊断场景现均通过，已全部纳入默认测试，不再需要 retryaudit 构建标签。

新增验证覆盖自动失败/超时重试、四个并发扫描器、手动入口复用、重试次数上限、取消请求竞态、停用/非幂等/未确认/历史失败保护，以及退避到期前禁止分配和调度器重建后的恢复。原退避与租约诊断已转入默认回归。

部署前仍需 Linux/systemd 实机与故障重放验收，并评估大量历史运行数据下自动重试查询的扫描成本。保持此前执行记录保留规则，不删除历史运行、凭据、缓存或执行 claim。

## 2026-09-21 状态：两个 P1 已本地修复，尚未发布

本轮只修复重复创建重试与已完成命令重复启动，不修改退避或旧租约策略。整体专项仍不能标记通过。新增默认回归和原高风险诊断已通过；诊断集现在为 7 个场景通过、2 个场景失败（退避与旧租约）。没有操作生产、提交 Git commit 或发布部署。

- 重试：按根运行 ID 对整条链加事务级 advisory lock；重复请求返回已存在的下一 attempt，不重复写入 queued 事件，也不会从祖先分叉。无需数据库迁移。此保护要求所有重试入口使用新实现，不能替代混用旧代码或直接数据库写入时的约束。
- 执行器：启动前以独占创建方式保存 claim；终态上报前保存完整事件结果。相同运行 ID/令牌再次到达时重放原事件，不重新启动，也不依赖原脚本缓存仍存在。
- 执行记录保存在运行根目录下的 `.execution-records`，与业务脚本的独立可写目录分离；目录 0700、记录文件 0600（POSIX）。只保存令牌哈希，不保存参数或环境变量。
- 未完成、损坏或不可读的记录均拒绝重新启动。代理记录需要对账的日志，继续处理其他命令，不伪造终态来提前释放原租约。

### 修复后验证

8 个相关包默认测试通过：task、executor、agent、scheduler、dispatch、store/redis、logstream、cmd/agent。关键执行记录并发/重放及不确定状态处理测试以 `-count=20` 连续通过；新增启动失败结果重放测试也通过。Linux/amd64 的 agent 与 API 交叉编译通过，未运行 Linux/systemd 实机测试。Windows 环境没有 gcc，未执行 Go race detector。

默认回归包括：8 个并发重试请求返回同一实例、完成后的祖先请求重放不新增事件、跨 Runner 对象竞争仅启动一次、重建 Runner 后精确重放结果、脚本缓存消失仍可重放、冲突令牌拒绝、不完整或损坏记录拒绝、存储失败时禁止启动、启动失败结果重放、不确定执行不误报失败且后续任务可继续。

### 发布前边界与保留规则

这是本地代码修复，不是线上修复完成。记录保护只适用于由新实现创建过 claim 的运行，不追溯旧代理历史；已有重复重试数据未做清理。保留执行记录，不自动按 TTL 删除，避免旧命令重新变得可执行；后续需设计与中央确认绑定的安全回收机制和容量监测。

进程崩溃留下未完成 claim 时选择安全拒绝，不能保证自动恢复可用性；必须在确认原进程状态后对账处理，不能靠删除 claim 或换令牌重放原运行。尚未验证断电、文件系统故障或 Linux 权限隔离的实机行为，不承诺跨节点严格恰好一次。

下一步仍需处理：重试退避、权威确认进程消失后的旧租约释放，以及失败后自动重试调用链。Linux/systemd 与故障重放验收、部署需另行安排。

## 初始检查结论（修复前）

本专项不能标记通过。现有 7 个相关包测试通过；新增 9 个诊断场景中，5 个基础安全条件通过，4 个预期安全断言失败。没有修改实现、连接生产、执行生产脚本或发布部署。

本报告针对本地工作树，不宣称与当前生产镜像源码完全一致，也不推翻此前六项生产专项测试的实测结果。Windows 测试不能代替 Linux/systemd 全链路验收。

## 修复前已复现问题（位置为初始基线）

### P1：重复请求能绕过一次重试限制

位置：`internal/task/reconcile.go:132-181`，重点为 151-171 行。

对 `max_retries=1`、原进程已确认结束的同一失败实例并发调用两次 RetryRun，创建了 **2 个不同的重试实例**。原实例被行锁串行保护，但事务未记录其重试已消费，也没有检查已有子实例；每次读取的原 attempt 仍为 1，因此都能通过限制。顺着子实例继续重试的次数检查有效，但不能防止重复从祖先创建分支。

影响：重复点击、HTTP 请求重放等可产生额外执行。单任务并发上限只能限制同时运行，不能消除多余的排队实例。

回归用例：`TestRetryAudit/duplicate_requests_share_one_retry`。

### P1：结束后的相同执行命令可再次启动

位置：`internal/executor/runner.go:150-210,261-270`。

用假执行器启动并结束一次运行，等待 active 项被清理，再发送完全相同的运行 ID/执行令牌，launcher 启动计数变为 **2**。当前去重仅覆盖 active map 中正在运行的任务，完成后没有对应拒绝或结果重放记录。`internal/agent/execution_client.go` 直接调用 Runner.Start，没有额外的完成记录防护。

影响：完成后的迟到重复派发可能重复执行。正常情况下中央已收到 started/终态就不会再次领取；风险窗口包括 started 上报失败、中央仍处于 assigned，以及已发出的迟到命令。本测试证明 Runner 边界可重复启动，未模拟真实网络故障或 Linux/systemd。

回归用例：`TestRetryAuditCompletedAssignmentCannotRestart`。

### P2：退避时间未阻止立即调度

位置：`internal/task/reconcile.go:157-171`、`internal/scheduler/postgres.go:35-36`、`internal/scheduler/service.go:43-109`。

配置 30 秒退避，在失败时刻立即重试，新实例仍被写为 queued、queued_at=当前时刻、scheduled_for=NULL，并立即被 ListQueued 返回。调度路径也没有消费 BackoffSeconds 的判断。该字段目前被保存及复制，但没有控制可调度时间。

影响：无法依靠该配置保证失败后的等待间隔。若产品明确允许手动重试跳过退避，应在界面和接口语义中说明；目前也未找到另行执行退避的自动重试入口。

回归用例：`TestRetryAudit/backoff_prevents_immediate_queue_eligibility`。

### P2：确认旧进程消失后重试，旧租约仍未释放

位置：`internal/task/reconcile.go:120-129,132-181`。

创建 unknown 运行及未过期资源租约，通过权威空进程清单确认原进程消失，再成功重试，原运行仍有 **1 条 released_at=NULL 的租约**。对账与重试事务均未释放该租约。

调度器的 ListReleasedLeases 只清理数据库已标记 released_at 的租约，所以这条记录不进入该清理路径；已有 Redis 预留可能继续影响容量，直到过期回收。本用例直接证明数据库旧租约残留，没有把它夸大为永久资源泄漏。

回归用例：`TestRetryAudit/confirmed_absent_retry_releases_old_lease`。

## 自动重试调用链缺口（静态检查）

在本地 cmd/internal 中追踪 RetryRun、Retry、MaxRetries、BackoffSeconds 等引用，任务重试生产调用链仅找到：HTTP `/api/runs/{id}/retry` → RunService → Reconciler → RetryRun。失败事件处理会更新终态、释放普通终态租约，但没有调用失败重试策略；调度扫描只处理 queued 运行。

因此不能把当前实现标记为“失败后自动按策略重试已验收”。派发器的固定间隔重投递是另一机制：重发同一 assigned 运行/令牌，不是失败后创建下一 attempt。此结论为静态调用链检查，不是生产等待测试。

## 测试结果与复现

已有测试：以下 7 包全部通过（`-count=1`）：

```powershell
$env:GOTOOLCHAIN='local'
$env:GOPROXY='off'
$env:GOCACHE=(Join-Path (Get-Location) '.tools/go-cache')
& ./.tools/go1.27.1/go/bin/go.exe test ./internal/task ./internal/scheduler ./internal/store/redis ./internal/dispatch ./internal/executor ./internal/logstream ./internal/agent -count=1 -timeout=240s
```

新增诊断测试使用 `retryaudit` build tag，不进入普通测试默认集合；当前保留失败断言，以便后续修复时验证：

```powershell
& ./.tools/go1.27.1/go/bin/go.exe test -tags retryaudit ./internal/task ./internal/executor -run '^TestRetryAudit' -v -count=1 -timeout=90s
```

通过：未声明幂等时拒绝、未确认进程结束时拒绝、0 次重试时拒绝、成功实例拒绝、沿重试子链执行次数上限。

失败：上述 4 项安全断言。数据库 3 项失败在两次执行中均复现。

首次沙箱执行因 Windows restricted token 和临时路径权限报错，不计为产品缺陷；获准在沙箱外重跑后，本地嵌入式 PostgreSQL 正常工作。数据库使用测试助手临时实例，执行器复现使用假 launcher，不实际运行脚本；未访问生产或读取生产凭据。

## 初始修复建议（当前进展见报告顶部）

优先项重试分支去重和完成命令去重已在本地落实，并加入默认回归覆盖。自动重试与退避策略、权威确认进程结束后的旧租约释放，以及 Linux/systemd 与故障重放验证仍未完成。生产测试和部署需另外确定影响范围与恢复方案。
