# 云令自动备份、COS 与恢复校验 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 每 6 小时生成 PostgreSQL 与 MinIO 数据的加密快照，保留本机 7 天、腾讯云 COS 30 天，并每月从 COS 自动完成一次隔离恢复校验。

**Architecture:** `yunling-ops` 使用持久化备份状态机和数据库租约协调任务；先导出 PostgreSQL 和 MinIO 到受限暂存区，再由 Restic 建立本机加密快照，并用 `restic copy` 复制到 COS，远端失败时从本机快照续传。恢复校验只恢复到随机临时数据库和目录，不挂载 Docker Socket、不覆盖生产数据。

**Tech Stack:** Go 1.27、pgx v5、PostgreSQL client 18.6、MinIO Client `RELEASE.2025-08-13T08-35-41Z`、Restic 0.19.1、腾讯云 COS S3 兼容接口、React 19、TypeScript 7、Vitest、Playwright、Docker Compose。

**Spec:** `docs/superpowers/specs/2026-08-31-yunling-operations-hardening-design.md`

## Global Constraints

- 先依次完成管理员改密计划和飞书告警计划；本计划从迁移 000012 开始，并扩展已经存在的 `yunling-ops` 与 `/operations` 页面。
- 每天 Asia/Shanghai `00:30`、`06:30`、`12:30`、`18:30` 自动备份；每月 1 日 `03:30` 自动恢复校验。
- 本机保留 7 天、COS 保留 30 天；只有新快照成功并完整时才执行清理。
- COS 上传失败保留本机快照并从该快照重试，不重新导出生产数据。
- Restic 客户端加密和 COS 服务端加密同时启用；Restic 密码、COS SecretId/SecretKey 只从 root-only 文件读取。
- COS 按[腾讯云 S3 兼容配置](https://cloud.tencent.com/document/product/436/41284)使用地域 endpoint；新存储桶使用 virtual-hosted style。
- Restic 按[官方 S3-compatible storage 文档](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html#s3-compatible-storage)设置 `-o s3.bucket-lookup=dns` 和明确 region。
- Ops 不挂载 Docker Socket、不以 root 运行、不把密钥写到命令行、日志、数据库或普通环境清单。
- 恢复校验不得连接或覆盖生产数据库名，不得创建业务任务。
- 先写失败测试，再写最小实现；每个任务独立提交。

---

### Task 1: 备份与恢复校验状态模型

**Files:**
- Create: `migrations/000012_backup_recovery.up.sql`
- Create: `migrations/000012_backup_recovery.down.sql`
- Create: `internal/backup/model.go`
- Create: `internal/backup/postgres.go`
- Create: `internal/backup/postgres_test.go`
- Modify: `internal/store/postgres/migrations_test.go`

**Interfaces:**
- Produces: `backup_runs` 和 `restore_verifications` 表。
- Produces: `backup.Repository` 的调度、原子领取、状态转换、降级续传和历史查询接口。
- Produces: 同一时刻最多一个备份和一个恢复校验持有活动租约。

- [ ] **Step 1: 写迁移失败测试**

新增测试要求两张表、到期索引和活动租约唯一索引存在：

```go
func TestBackupRecoveryMigrationCreatesOperationalTables(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	for _, table := range []string{"backup_runs", "restore_verifications"} {
		if !tableExists(t, db, table) { t.Fatalf("缺少表 %s", table) }
	}
}
```

- [ ] **Step 2: 运行迁移测试确认失败**

Run: `go test ./internal/store/postgres -run BackupRecovery -count=1`

Expected: FAIL，提示新表不存在。

- [ ] **Step 3: 编写向上迁移**

`000012_backup_recovery.up.sql` 使用：

```sql
CREATE TABLE backup_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_type text NOT NULL CHECK (trigger_type IN ('scheduled', 'manual')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'exporting', 'snapshotting', 'uploading', 'succeeded', 'degraded', 'failed')),
    scheduled_for timestamptz,
    idempotency_key text,
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    local_snapshot_id text NOT NULL DEFAULT '',
    cos_snapshot_id text NOT NULL DEFAULT '',
    manifest_sha256 text NOT NULL DEFAULT '',
    byte_size bigint NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    object_count bigint NOT NULL DEFAULT 0 CHECK (object_count >= 0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scheduled_for)
);

CREATE UNIQUE INDEX backup_runs_idempotency_idx ON backup_runs (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX backup_runs_active_lease_idx ON backup_runs ((true))
    WHERE status IN ('exporting', 'snapshotting', 'uploading') AND lease_until IS NOT NULL;
CREATE INDEX backup_runs_due_idx ON backup_runs (next_attempt_at, created_at)
    WHERE status IN ('queued', 'degraded');

CREATE TABLE restore_verifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_run_id uuid NOT NULL REFERENCES backup_runs(id) ON DELETE RESTRICT,
    trigger_type text NOT NULL CHECK (trigger_type IN ('scheduled', 'manual')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'restoring', 'checking', 'succeeded', 'failed')),
    scheduled_for timestamptz,
    idempotency_key text,
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    temporary_database text NOT NULL DEFAULT '',
    migration_version text NOT NULL DEFAULT '',
    checked_objects bigint NOT NULL DEFAULT 0 CHECK (checked_objects >= 0),
    lease_until timestamptz,
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scheduled_for)
);

CREATE UNIQUE INDEX restore_verifications_idempotency_idx
    ON restore_verifications (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX restore_verifications_active_lease_idx ON restore_verifications ((true))
    WHERE status IN ('restoring', 'checking') AND lease_until IS NOT NULL;
CREATE INDEX restore_verifications_due_idx ON restore_verifications (created_at)
    WHERE status='queued';

INSERT INTO schema_migrations (version) VALUES (12)
ON CONFLICT (version) DO NOTHING;
```

向下迁移先删除 `schema_migrations` 的版本 12，再删 `restore_verifications` 和 `backup_runs`。

- [ ] **Step 4: 运行迁移测试确认通过**

Run: `go test ./internal/store/postgres -run BackupRecovery -count=1`

Expected: PASS。

- [ ] **Step 5: 写仓储状态机失败测试**

覆盖：相同 scheduled_for 只创建一次；两个 worker 只能领取一个；租约过期可接管；非法状态转换被拒绝；degraded 保留 local snapshot 并重新排期；历史按时间倒序。

```go
claimed, ok, err := repository.ClaimBackup(ctx, now, 30*time.Minute)
if err != nil || !ok || claimed.Status != backup.StatusExporting {
	t.Fatalf("领取错误：run=%+v ok=%v err=%v", claimed, ok, err)
}
```

- [ ] **Step 6: 运行仓储测试确认失败**

Run: `go test ./internal/backup -run 'Repository|State|Claim' -count=1`

Expected: FAIL，提示包或仓储不存在。

- [ ] **Step 7: 实现模型与 PostgreSQL 仓储**

定义 `BackupRun`、`RestoreVerification`、状态常量和：

```go
type Repository interface {
	EnsureSchedules(context.Context, time.Time) error
	RequestBackup(context.Context, string, string, time.Time) (BackupRun, error)
	ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error)
	MarkLocalSnapshot(context.Context, string, SnapshotResult, time.Time) error
	MarkBackupSucceeded(context.Context, string, string, time.Time) error
	MarkBackupDegraded(context.Context, string, string, time.Time) error
	MarkBackupFailed(context.Context, string, string, time.Time) error
	RequestVerification(context.Context, string, string, string, time.Time) (RestoreVerification, error)
	ClaimVerification(context.Context, time.Time, time.Duration) (RestoreVerification, bool, error)
	CompleteVerification(context.Context, VerificationResult, time.Time) error
	ListBackups(context.Context, int) ([]BackupRun, error)
	ListVerifications(context.Context, int) ([]RestoreVerification, error)
}
```

`RequestBackup` 的字符串参数依次为 actorID、idempotencyKey；`RequestVerification` 的字符串参数依次为 actorID、backupRunID、idempotencyKey。所有错误字符串截断到 4096 字节。手动请求要求 UUID 幂等键，并在同一事务追加 `operations.backup.request` 或 `operations.verification.request` 审计；定时请求 actor 和幂等键均为空，依靠 `scheduled_for` 去重。

- [ ] **Step 8: 运行模型与仓储测试**

Run: `go test ./internal/backup ./internal/store/postgres -count=1`

Expected: PASS。

- [ ] **Step 9: 提交备份状态模型**

```bash
git add migrations/000012_backup_recovery.* internal/backup internal/store/postgres/migrations_test.go
git commit -m "feat: 建立备份恢复状态机"
```

### Task 2: 受限命令执行器与 Ops 备份配置

**Files:**
- Create: `internal/backup/config.go`
- Create: `internal/backup/config_test.go`
- Create: `internal/backup/command.go`
- Create: `internal/backup/command_test.go`
- Create: `internal/backup/paths.go`
- Create: `internal/backup/paths_test.go`
- Modify: `cmd/ops/main.go`
- Modify: `cmd/ops/main_test.go`
- Modify: `deploy/Dockerfile.ops`

**Interfaces:**
- Produces: `backup.Config`，所有秘密以 `*_FILE` 路径表示。
- Produces: `CommandRunner.Run(ctx, name string, args []string, env map[string]string) (CommandResult, error)`。
- Produces: `RunPaths`，只在 `/var/lib/yunling-ops/{staging,local-repo,restore}` 下创建随机目录。

- [ ] **Step 1: 写配置失败测试**

要求缺少数据库、备份数据库密码文件、校验数据库密码文件、MinIO、COS endpoint/region/bucket、Restic password file 或 COS credential file 时失败；禁止 secret 直接环境变量；默认路径固定在 `/var/lib/yunling-ops`。启动备份前要求可用空间至少为 `max(2 GiB, 最近成功备份字节数 × 1.5)`，否则不执行导出并触发空间不足告警。

```go
if _, err := LoadConfig(mapEnv(validEnvWithout("YUNLING_COS_SECRET_KEY_FILE"))); err == nil {
	t.Fatal("缺少 COS SecretKey 文件必须失败")
}
```

- [ ] **Step 2: 写命令执行失败测试**

覆盖允许命令白名单仅为 `/usr/bin/pg_dump`、`/usr/bin/pg_restore`、`/usr/bin/psql`、`/usr/bin/mc`、`/usr/bin/restic`；拒绝相对路径；stdout/stderr 各最多 4096 字节；超时杀死进程组；错误不包含 env 值。

- [ ] **Step 3: 运行定向测试确认失败**

Run: `go test ./internal/backup ./cmd/ops -run 'Config|Command|Paths' -count=1`

Expected: FAIL，提示配置或执行器不存在。

- [ ] **Step 4: 实现配置、路径和命令执行器**

`CommandRunner` 使用 `exec.CommandContext`，参数用切片传递，不经过 shell；秘密只通过子进程环境或 password file 传递。实现固定的有界 buffer，并在 context 取消后确保进程组退出。

`RunPaths.For(runID)` 先验证 UUID，再使用 `filepath.Rel` 确认最终路径仍位于根目录，权限为 `0700`，禁止符号链接逃逸。

- [ ] **Step 5: 固定 Ops 工具链**

`deploy/Dockerfile.ops` 使用多阶段镜像：

- builder：Go 1.27.0 Alpine，构建 `yunling-ops`。
- PostgreSQL 基础：`postgres:18.6-alpine`，提供 `pg_dump`、`pg_restore`、`psql`。
- Restic：复制官方 `restic/restic:0.19.1` 二进制。
- MinIO Client：复制 `quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z` 二进制。
- 最终用户仍为 `10001:10001`，工具和应用文件不可写。

在镜像测试中运行 `yunling-ops --check-tools`，输出只包含工具名和版本，不包含配置。

- [ ] **Step 6: 运行命令、配置和镜像构建测试**

Run: `go test ./internal/backup ./cmd/ops -run 'Config|Command|Paths|Tools' -count=1`

Expected: PASS。

Run: `docker build -f deploy/Dockerfile.ops -t yunling-ops:test .`

Expected: 成功构建。

- [ ] **Step 7: 提交安全执行基础**

```bash
git add internal/backup/config.go internal/backup/config_test.go internal/backup/command.go internal/backup/command_test.go internal/backup/paths.go internal/backup/paths_test.go cmd/ops deploy/Dockerfile.ops
git commit -m "feat: 建立受限备份执行环境"
```

### Task 3: 数据导出、清单与本机 Restic 快照

**Files:**
- Create: `internal/backup/export.go`
- Create: `internal/backup/export_test.go`
- Create: `internal/backup/manifest.go`
- Create: `internal/backup/manifest_test.go`
- Create: `internal/backup/restic.go`
- Create: `internal/backup/restic_test.go`
- Create: `internal/backup/service.go`
- Create: `internal/backup/service_test.go`

**Interfaces:**
- Consumes: `Repository`（Task 1）、`Config`/`CommandRunner`/`RunPaths`（Task 2）。
- Produces: `Exporter.Export(ctx, BackupRun) (ExportResult, error)`。
- Produces: `BuildManifest(root string) (Manifest, error)` 与 `VerifyManifest(root string, manifest Manifest) error`。
- Produces: `ResticRepository.SnapshotLocal(ctx, root, runID string) (snapshotID string, error)`。
- Produces: `Service.RunBackup(ctx) error` 的本机快照阶段。

- [ ] **Step 1: 写导出失败测试**

使用 fake runner 断言顺序：先执行 `pg_dump --format=custom --file=/var/lib/yunling-ops/staging/{runID}/database/yunling.dump`，然后执行 `mc mirror --overwrite --remove local/yunling /var/lib/yunling-ops/staging/{runID}/objects`。数据库导出必须先于对象镜像，使导出时已被数据库引用的不可变对象包含在随后镜像中；并发新增但未被导出引用的对象可以作为无害额外文件进入快照。

- [ ] **Step 2: 写清单失败测试**

用临时目录创建数据库 dump 和两个对象，断言路径按 `/` 标准化、排序稳定、每项含 size/SHA-256、总字节和对象数正确；篡改一个字节后 Verify 返回文件名但不返回内容。

```go
manifest, err := BuildManifest(root)
if err != nil || manifest.Database.Path != "database/yunling.dump" { t.Fatalf("清单错误：%+v %v", manifest, err) }
```

- [ ] **Step 3: 运行导出与清单测试确认失败**

Run: `go test ./internal/backup -run 'Exporter|Manifest' -count=1`

Expected: FAIL，提示实现不存在。

- [ ] **Step 4: 实现导出与清单**

导出树固定为：

```text
database/yunling.dump
objects/
metadata/deployment.json
manifest.json
```

`objects/` 下完整保留 MinIO 对象键的逐级目录。`metadata/deployment.json` 只包含 Git revision、镜像摘要、迁移版本、生成时间和对象存储 bucket 名；不包含 `.env`。先生成其他文件，再以 canonical JSON 写 `manifest.json`，最后重新读取校验。

- [ ] **Step 5: 写本机 Restic 失败测试**

fake runner 必须收到：repository 通过 `--repository-file`，密码通过 `--password-file`，标签 `backup-run={UUID}`，不在参数中出现密钥。解析 `restic snapshots --json` 获得唯一 snapshot ID；零个或多个匹配均失败。

- [ ] **Step 6: 实现本机快照与服务阶段**

`SnapshotLocal` 先执行 `restic cat config`：退出码 0 表示已初始化；退出码 10 表示仓库不存在，此时执行一次 `restic init`；其他退出码直接失败，禁止依赖错误文案。快照成功后再次运行 `restic check --read-data-subset=5%`，然后调用 `Repository.MarkLocalSnapshot`。清理暂存目录前必须已持久化 snapshot ID、manifest SHA-256、字节和对象数量。

- [ ] **Step 7: 运行备份包测试**

Run: `go test ./internal/backup -run 'Exporter|Manifest|Restic|LocalSnapshot' -count=1`

Expected: PASS。

- [ ] **Step 8: 提交本机加密快照**

```bash
git add internal/backup
git commit -m "feat: 生成本机加密备份快照"
```

### Task 4: COS 快照复制、重试与保留策略

**Files:**
- Modify: `internal/backup/restic.go`
- Modify: `internal/backup/restic_test.go`
- Create: `internal/backup/retention.go`
- Create: `internal/backup/retention_test.go`
- Modify: `internal/backup/service.go`
- Modify: `internal/backup/service_test.go`

**Interfaces:**
- Produces: `ResticRepository.CopyToCOS(ctx, localSnapshotID, runID string) (cosSnapshotID string, error)`。
- Produces: `Retention.Apply(ctx, completedRun BackupRun) error`。
- Produces: degraded 备份从 `local_snapshot_id` 续传。

- [ ] **Step 1: 写 COS 参数失败测试**

断言 Restic 命令使用：

```text
-o s3.bucket-lookup=dns
-o s3.region=ap-guangzhou
--repository-file /run/config/cos-repository
--password-file /run/secrets/restic-password
--from-repository-file /run/config/local-repository
--from-password-file /run/secrets/restic-password
```

单元测试使用 `ap-guangzhou` fixture；生产值从 `YUNLING_COS_REGION` 读取。测试必须确认 repository 文件内容形如 `s3:https://cos.ap-guangzhou.myqcloud.com/yunling-backup-1250000000/yunling`，命令行不含 SecretId/SecretKey；它们只进入子进程 `AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`。

- [ ] **Step 2: 写降级续传失败测试**

第一次 COS copy 失败时断言 `MarkBackupDegraded` 保存本机 snapshot ID 和有界错误；第二次 Claim 同一 run 时直接调用 `CopyToCOS`，不得再次调用 Exporter 或 SnapshotLocal。

- [ ] **Step 3: 运行 COS 与重试测试确认失败**

Run: `go test ./internal/backup -run 'COS|Degraded|Resume' -count=1`

Expected: FAIL，直到复制和续传实现。

- [ ] **Step 4: 实现 COS 复制**

远端仓库首次使用前执行以下命令，保证复制后的快照可以跨仓库去重：

```text
restic -o s3.bucket-lookup=dns -o s3.region={YUNLING_COS_REGION} --repository-file /run/config/cos-repository --password-file /run/secrets/restic-password init --from-repository-file /run/config/local-repository --from-password-file /run/secrets/restic-password --copy-chunker-params
```

复制时执行：

```text
restic -o s3.bucket-lookup=dns -o s3.region={YUNLING_COS_REGION} --repository-file /run/config/cos-repository --password-file /run/secrets/restic-password copy --from-repository-file /run/config/local-repository --from-password-file /run/secrets/restic-password {localSnapshotID}
```

Restic 原生支持中断后继续复制。复制后用同一组目标仓库全局参数执行 `snapshots --json --tag backup-run={runID}` 查询远端唯一 snapshot ID，并运行 `check --read-data-subset=5%`。只有两个操作成功才 `MarkBackupSucceeded`。

- [ ] **Step 5: 写保留策略失败测试**

覆盖新备份未成功时完全不执行 forget；成功时本机 `--keep-within 7d`、COS `--keep-within 30d`；任一 prune 失败只产生 `backup_retention_failed` 告警，不把成功备份改为失败。

- [ ] **Step 6: 实现保留策略和备份告警**

本机与 COS 分别运行：

```text
restic forget --prune --keep-within 7d
restic forget --prune --keep-within 30d
```

Service 使用 Plan 2 的 `alert.Service`：导出/快照失败 Raise `backup_failed`；COS 失败 Raise `backup_cos_degraded`；后续成功 Resolve；保留失败 Raise `backup_retention_failed`。不得把子进程完整 stderr 放入飞书载荷。

- [ ] **Step 7: 运行 COS、重试和保留测试**

Run: `go test ./internal/backup -run 'COS|Degraded|Resume|Retention' -count=1`

Expected: PASS。

- [ ] **Step 8: 提交 COS 备份链路**

```bash
git add internal/backup
git commit -m "feat: 同步加密快照到腾讯云 COS"
```

### Task 5: 隔离恢复校验

**Files:**
- Create: `internal/backup/verify.go`
- Create: `internal/backup/verify_test.go`
- Modify: `internal/backup/service.go`
- Modify: `internal/backup/service_test.go`
- Modify: `internal/ops/loop.go`
- Modify: `internal/ops/loop_test.go`

**Interfaces:**
- Produces: `Verifier.Verify(ctx, verification RestoreVerification, backup BackupRun) (VerificationResult, error)`。
- Produces: Ops 每月调度和手动队列的恢复校验执行。

- [ ] **Step 1: 写恢复安全边界失败测试**

测试临时数据库名严格匹配 `^yunling_verify_[0-9a-f]{32}$`，且任何等于生产数据库名、含引号/连字符/空白的值都在执行命令前被拒绝。runner 顺序必须是远端 restore、manifest verify、`createdb` 等价 SQL、`pg_restore`、一致性查询、drop database。

- [ ] **Step 2: 写失败清理测试**

在 restore、pg_restore、SQL 校验三个阶段分别注入错误，断言都尝试删除临时数据库和目录；清理也失败时返回包含“恢复校验失败”和“清理失败”的有界错误，并 Raise critical 告警。

- [ ] **Step 3: 运行恢复测试确认失败**

Run: `go test ./internal/backup -run 'Verifier|RestoreCleanup|TemporaryDatabase' -count=1`

Expected: FAIL，提示 Verifier 不存在。

- [ ] **Step 4: 实现恢复校验**

固定流程：

1. 从 COS 按 backup-run tag 恢复到 `/var/lib/yunling-ops/restore/{verificationID}`。
2. 对 `manifest.json` 中所有文件执行大小和 SHA-256 校验。
3. 使用 verifier 数据库角色创建随机临时数据库。
4. `pg_restore --exit-on-error --no-owner --no-privileges`。
5. 查询 `schema_migrations` 最新版本，并验证 `users`、`servers`、`script_versions`、`task_runs`、`audit_logs` 可读。
6. 读取 `script_versions.artifact_uri`、`run_log_archives.object_key` 和 `run_artifacts.object_key`，与清单 `objects/` 路径逐一匹配。
7. 对已经通过正则校验的数据库名调用 `pgx.Identifier{databaseName}.Sanitize()`，用 `"DROP DATABASE " + sanitizedName + " WITH (FORCE)"` 构造并执行语句，然后删除恢复目录。

结果只保存迁移版本、校验对象数和有界错误，不保存数据内容。

- [ ] **Step 5: 接入 Ops 调度**

`internal/ops/loop.go` 每次扫描先 `EnsureSchedules(now)`；备份与恢复校验各使用独立 30 分钟租约。每月 1 日 `03:30` 只选择最新 `succeeded` 且有 `cos_snapshot_id` 的备份。恢复成功 Resolve `backup_verification_failed` 并 Raise info `backup_verification_succeeded`，失败 Raise critical。

- [ ] **Step 6: 运行恢复和 Ops 测试**

Run: `go test ./internal/backup ./internal/ops -run 'Verif|Schedule|Cleanup' -count=1`

Expected: PASS。

- [ ] **Step 7: 提交恢复校验**

```bash
git add internal/backup internal/ops/loop.go internal/ops/loop_test.go
git commit -m "feat: 自动校验 COS 恢复能力"
```

### Task 6: 运维摘要、备份 API 与中文控制台

**Files:**
- Modify: `internal/operationshttp/handler.go`
- Modify: `internal/operationshttp/handler_test.go`
- Create: `apps/web/src/features/operations/BackupStatusPanel.tsx`
- Create: `apps/web/src/features/operations/BackupStatusPanel.test.tsx`
- Create: `apps/web/src/features/operations/BackupHistoryPanel.tsx`
- Create: `apps/web/src/features/operations/BackupHistoryPanel.test.tsx`
- Modify: `apps/web/src/features/operations/OperationsPage.tsx`
- Modify: `apps/web/src/features/operations/OperationsPage.test.tsx`
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/api/client.test.ts`
- Modify: `apps/web/src/app/styles.css`
- Modify: `apps/web/src/features/settings/AuditPage.tsx`

**Interfaces:**
- Consumes: `backup.Repository`（Task 1）。
- Produces: operations summary、备份列表/请求、校验列表/请求 API。
- Produces: `BackupStatusPanel` 和 `BackupHistoryPanel`。

- [ ] **Step 1: 写 API 失败测试**

覆盖设计中的五个接口：

```text
GET  /api/operations/summary
GET  /api/operations/backups
POST /api/operations/backups
GET  /api/operations/verifications
POST /api/operations/verifications
```

viewer 只能 GET；admin 可以 POST；POST 要求 Origin、JSON 和 `Idempotency-Key` UUID；重复 key 返回同一资源而不是新建；summary 包含 nextBackupAt、latestLocalBackup、latestCOSBackup、latestVerification 和状态。

- [ ] **Step 2: 运行 API 测试确认失败**

Run: `go test ./internal/operationshttp -run 'Backup|Verification|Summary' -count=1`

Expected: FAIL，直到服务依赖和路由扩展。

- [ ] **Step 3: 实现 API**

扩展 `operationshttp.Services`：

```go
type BackupManager interface {
	Summary(context.Context) (backup.Summary, error)
	ListBackups(context.Context, int) ([]backup.BackupRun, error)
	RequestBackup(context.Context, string, string, time.Time) (backup.BackupRun, error)
	ListVerifications(context.Context, int) ([]backup.RestoreVerification, error)
	RequestVerification(context.Context, string, string, string, time.Time) (backup.RestoreVerification, error)
}
```

列表 limit 固定最大 100，错误返回中文有界消息。POST 返回 202。数据库未配置或 Ops 尚未运行时 summary 返回明确状态，不伪装为成功备份。

- [ ] **Step 4: 写前端失败测试**

客户端类型必须映射所有状态。组件覆盖：首次未备份空状态、local succeeded/COS degraded、最近校验失败、admin 立即备份、admin 立即校验、viewer 按钮不可见、请求后显示 queued、错误焦点。

```tsx
expect(await screen.findByText('下一次自动备份')).toBeVisible()
await user.click(screen.getByRole('button', { name: '立即备份' }))
expect(await screen.findByText('备份请求已进入队列')).toBeVisible()
```

- [ ] **Step 5: 运行前端测试确认失败**

Run: `npm --workspace apps/web test -- --run src/features/operations/BackupStatusPanel.test.tsx src/features/operations/BackupHistoryPanel.test.tsx`

Expected: FAIL，提示组件不存在。

- [ ] **Step 6: 实现控制台**

`OperationsPage` 顶部为运行保障摘要，中部为备份历史/恢复校验，下部保留飞书通知和账号安全。状态中文映射：排队中、正在导出、正在生成快照、正在上传 COS、成功、仅本机成功、失败、正在恢复、正在校验、校验成功。

手动请求由客户端生成 `crypto.randomUUID()` 作为 `Idempotency-Key`。页面以 5 秒间隔刷新存在活动状态的数据，离开页面停止定时器。错误消息不展示底层命令行。

`AuditPage.tsx` 增加 `operations.backup.request` 和 `operations.verification.request` 中文映射。

- [ ] **Step 7: 运行 API、Web 测试与构建**

Run: `go test ./internal/operationshttp -count=1`

Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts src/features/operations src/app/App.test.tsx`

Expected: PASS。

Run: `npm --workspace apps/web run build`

Expected: PASS。

- [ ] **Step 8: 提交运维备份控制台**

```bash
git add internal/operationshttp apps/web/src
git commit -m "feat: 在运维中心管理备份恢复"
```

### Task 7: Compose、故障演练与生产上线

**Files:**
- Create: `tests/integration/backup_recovery_test.go`
- Create: `apps/web/e2e/operations-backup.spec.ts`
- Modify: `apps/web/e2e/fixtures.ts`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/.env.example`
- Modify: `deploy/postgres-init.sh`
- Create: `deploy/initialize-ops-secrets.sh`
- Modify: `tests/integration/deployment_security_test.go`
- Modify: `Makefile`
- Modify: `deploy/README.md`
- Modify: `deploy/PRODUCTION.md`

**Interfaces:**
- Consumes: 三份计划的全部后端、Ops 和 Web 能力。
- Produces: 八个长期生产服务（原七个服务加 Ops；`bootstrap` 与 `minio-init` 仍为一次性工具服务）。
- Produces: 真实飞书、真实 COS、真实恢复校验和改密的生产验收证据。

- [ ] **Step 1: 写完整集成失败测试**

测试启动临时 PostgreSQL、两个 MinIO 实例（第二个模拟 S3/COS）、fake 飞书服务器和 Ops 单次扫描，验证：

1. 数据库与三个对象被导出。
2. 本机和远端 Restic 快照都存在。
3. 远端快照可恢复到临时数据库。
4. 所有对象 SHA-256 通过。
5. COS 第一次失败后从本机 snapshot 续传且导出调用仍为 1 次。
6. Ops 中途取消后租约到期可接管。

- [ ] **Step 2: 运行集成测试确认失败**

Run: `go test ./tests/integration -run BackupRecovery -count=1`

Expected: FAIL，直到真实工具链和测试 fixture 完成。

- [ ] **Step 3: 完成 Compose 最小权限配置**

Ops 配置使用以下文件挂载，宿主机权限均为 `0600`，目录为 `0700`：

```text
deploy/secrets/yunling-master-key
deploy/secrets/cos-secret-id
deploy/secrets/cos-secret-key
deploy/secrets/restic-password
deploy/secrets/backup-postgres-password
deploy/secrets/verify-postgres-password
deploy/secrets/backup-minio-access-key
deploy/secrets/backup-minio-secret-key
```

非敏感值放在 root-only `deploy/.env`：COS region、bucket、前缀、endpoint 和本地路径。宿主机源 secret 保持 `0600 root:root`；新增一次性、无网络的 `ops-secrets-init` 工具服务，以 root 读取这些 bind mount 并复制到专用 `yunling_ops_secrets` 卷，目标文件为 `0400 10001:10001`。长期 Ops 只读挂载该专用卷并继续以 UID 10001 运行。Compose 把 `yunling_ops_data` 挂载到 `/var/lib/yunling-ops`，不发布端口，不挂载宿主机 `/`、Docker Socket 或 PostgreSQL/MinIO 原始数据卷。

`postgres-init.sh` 为新安装创建只读 backup 角色和可创建临时数据库的 verifier 角色；生产升级文档使用幂等 SQL 为现有数据库创建相同角色。MinIO 初始化创建只读备份用户，只允许列出和读取云令 bucket。

`initialize-ops-secrets.sh` 必须设置 `umask 077`，为 Restic、backup/verifier 数据库角色和 MinIO backup 用户生成独立随机值，已有文件存在时拒绝覆盖。脚本把 Restic 密码和现有应用主密钥的 Base64 表示写入 `/root/yunling-recovery-key.txt`（`0600 root:root`），不打印内容；管理员把这份恢复密钥包保存到服务器之外的密码管理器并执行 `sudo rm -f /root/yunling-recovery-key.txt` 后，部署才可继续。COS 凭据由腾讯云 CAM 创建，不进入恢复密钥包。

- [ ] **Step 4: 写部署安全测试并验证**

断言所有 secret 通过文件挂载，不出现在 Compose 展开后的 environment；`ops-secrets-init` 无网络、只写专用 secret 卷且不是长期服务；Ops 只有 backend+egress；数据卷只挂载专用路径；read-only/no-new-privileges/UID 10001 生效；COS endpoint 必须 HTTPS。

Run: `go test ./tests/integration -run 'BackupRecovery|OpsDeployment' -count=1`

Expected: PASS。

- [ ] **Step 5: 添加 Web E2E**

`operations-backup.spec.ts` 覆盖摘要、立即备份、状态刷新、COS 降级显示和立即恢复校验。fixture 不真正执行备份，只返回确定状态。

Run: `npm --workspace apps/web run test:e2e -- operations-backup.spec.ts`

Expected: PASS。

- [ ] **Step 6: 完成故障演练测试**

分别注入 COS 503、飞书 500、Ops SIGTERM、暂存空间不足和损坏快照，断言：调度器与 API 健康；备份/通知持久化重试；损坏快照不进入恢复数据库；没有临时数据库或目录残留。

Run: `go test ./tests/integration -run 'BackupRecovery|OperationsFailure' -count=1`

Expected: PASS。

- [ ] **Step 7: 运行上线前全量验证**

Run: `go test ./... -count=1`

Expected: PASS。

Run: `go vet ./...`

Expected: 退出码 0。

Run: `npm --workspace apps/web test -- --run`

Expected: PASS。

Run: `npm --workspace apps/web run build`

Expected: PASS。

Run: `npm --workspace apps/web run test:e2e`

Expected: PASS。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`

Expected: 退出码 0。

Run: `git diff --check`

Expected: 无输出，退出码 0。

- [ ] **Step 8: 提交集成与部署文档**

```bash
git add tests/integration apps/web/e2e apps/web/e2e/fixtures.ts deploy Makefile
git commit -m "test: 覆盖备份恢复与运维故障演练"
```

- [ ] **Step 9: 创建生产变更前恢复点**

通过腾讯云 SSH 只读核对 `/opt/yunling`、磁盘、内存和九个目标服务配置。在 `/opt/backups` 创建 PostgreSQL、MinIO 对象和部署配置恢复点，权限 `0600 root:root`，生成 SHA-256。确认恢复点可读后才继续；不得覆盖 2026-08-29 的既有备份。

- [ ] **Step 10: 获取生产外部配置并做无写入预检**

从用户现有 COS 取得准确 bucket 名和 region；创建只允许指定备份前缀读写/列出/删除的 CAM 子账号凭据。启用存储桶版本控制、默认服务端加密和生命周期规则：非当前对象版本 30 天后删除，未完成分块上传 7 天后清理。先用临时容器执行 HTTPS endpoint、virtual-hosted lookup、列出指定前缀和写入/删除随机探针对象测试。凭据只写入 root-only secret 文件，不在聊天、命令输出或 shell history 中显示。

飞书配置由管理员登录云令控制台后自行录入；执行者不要求用户在聊天中发送 Webhook 或签名密钥。

确认管理员已经把 `/root/yunling-recovery-key.txt` 内容保存到受控密码管理器，再删除该临时文件并验证不存在；只记录保存确认和删除结果，不记录密钥内容。

- [ ] **Step 11: 部署迁移、API、Web 与 Ops**

上传已提交源码，构建固定镜像，先运行数据库迁移，再启动 API/Web/Ops。检查：

```bash
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
curl --fail --show-error https://aiwise.top/api/health
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T ops wget -qO- http://127.0.0.1:8081/healthz
```

Expected: 所有长期服务 healthy/running，API 返回 `{"status":"ok"}`，Ops 返回 `ok`，京东云代理仍在线。

- [ ] **Step 12: 执行唯一生产验收链路**

按顺序执行且不创建业务任务：

1. 控制台发送一条带“云令生产测试”标识的飞书测试消息。
2. 请求一份真实手动备份，等待本机和 COS 都 succeeded。
3. 对该备份请求一次恢复校验，等待 succeeded。
4. 核对临时数据库和恢复目录均已清理。
5. 管理员修改密码，使用新密码重新登录，并验证旧密码失败。
6. 删除 `/root/yunling-initial-admin.txt`，随后 `sudo test ! -e` 验证不存在。
7. 核对调度器、Agent、现有唯一验收运行和队列数量未被改变。

- [ ] **Step 13: 观察连续两次定时备份**

保持固定计划不变，等待随后两个 `scheduled` 备份在各自时间窗内完成。两条记录都必须是本机和 COS `succeeded`，计划时间分别与四个每日时点之一精确对应，且没有重复 `scheduled_for`。期间监控磁盘、Swap、Ops 重启次数和飞书异常；任何一次失败都先修复并从新的连续两次成功重新计数。

- [ ] **Step 14: 更新生产证据并提交**

`deploy/PRODUCTION.md` 记录：部署提交、镜像摘要、迁移 000010–000012、真实飞书 delivery ID、backup run ID、local/COS snapshot ID、manifest SHA-256、verification ID、文件删除结果、服务健康和测试数量。不得记录密码、Webhook、签名、COS 凭据、Restic 密码或业务数据内容。

```bash
git add deploy/PRODUCTION.md
git commit -m "docs: 记录运维加固生产验收"
```

### Plan 3 完成检查点

- [ ] 自动备份时间、7/30 天保留和月度恢复计划均有确定性测试。
- [ ] COS 使用 HTTPS、明确 region 和 `s3.bucket-lookup=dns`。
- [ ] COS 失败从本机 snapshot 续传，导出不重复。
- [ ] 恢复校验使用随机临时数据库，失败和成功均清理。
- [ ] 备份与恢复 UI、API、权限、审计和 E2E 全部通过。
- [ ] 生产飞书测试、真实备份、真实恢复校验和管理员改密均有无敏感信息的证据。
- [ ] `/root/yunling-initial-admin.txt` 已在新密码重新登录验证后删除。
- [ ] 工作区干净，所有任务均有独立提交。
