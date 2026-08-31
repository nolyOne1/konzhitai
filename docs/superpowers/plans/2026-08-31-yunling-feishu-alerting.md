# 云令飞书告警与运维通知 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把云令内部告警扩展为可靠、可恢复、可在控制台配置的飞书通知，并按已确认阈值检测服务器、资源、任务失败和排队超时。

**Architecture:** PostgreSQL 保存飞书配置引用、规则状态和通知发件箱；告警表触发器在同一事务中生成开启/恢复通知，避免进程崩溃造成丢消息。独立 `yunling-ops` 服务负责规则扫描和发件箱发送，API 只负责配置与查询，Web 在现有运维中心增加通知面板。

**Tech Stack:** Go 1.27、pgx v5、PostgreSQL 18、AES-GCM 信封加密、飞书 V2 自定义机器人、React 19、TypeScript 7、Vitest、Playwright、Docker Compose。

**Spec:** `docs/superpowers/specs/2026-08-31-yunling-operations-hardening-design.md`

## Global Constraints

- 先完成 `2026-08-31-yunling-admin-password-hardening.md`，本计划从迁移 000011 开始并复用其同源请求边界和运维中心页面。
- 飞书 Webhook 必须是 `https://open.feishu.cn/open-apis/bot/v2/hook/{token}`，禁止重定向。
- 签名请求使用秒级 `timestamp`、`sign`、`msg_type` 和 `content`；签名规则以[飞书自定义机器人官方指南](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)为准。
- Webhook 和签名密钥复用现有秘密服务加密，API 永不返回明文或密文。
- 同类告警 5 分钟合并；通知退避为 1 分钟、5 分钟、15 分钟、1 小时、6 小时。
- 服务器离线阈值 2 分钟；排队阈值 10 分钟；磁盘 15% 告警/20% 恢复；内存 10% 告警/15% 恢复；资源规则均要求连续两个有效样本。
- 飞书失败不得阻塞调度、API 或 Agent，也不得递归向飞书发送“飞书失败”告警。
- 消息不得包含敏感参数、日志正文、数据库详情或凭据。
- 先写失败测试，再写最小实现；每个任务独立提交。

---

### Task 1: 通知、发件箱与规则状态迁移

**Files:**
- Create: `migrations/000011_notifications.up.sql`
- Create: `migrations/000011_notifications.down.sql`
- Modify: `internal/store/postgres/migrations_test.go`
- Modify: `internal/secret/service.go`
- Modify: `internal/secret/service_test.go`
- Modify: `internal/secret/postgres.go`
- Modify: `internal/secret/postgres_test.go`

**Interfaces:**
- Produces: `notification_configs`、`notification_outbox`、`alert_rule_states`。
- Produces: `secret.ScopeUser`、`secret.ScopeSystem`、`Service.CreateSystem(ctx context.Context, name string, plaintext []byte) (Metadata, error)` 和 `Service.Resolve(ctx context.Context, ids []ID) (map[ID][]byte, error)`。
- Produces: 告警 INSERT/RESOLVE 与发件箱 INSERT 的数据库原子性。

- [ ] **Step 1: 写迁移失败测试**

新增测试，要求三个新表存在、`secrets.scope` 存在且默认为 `user`，并验证告警触发器：启用飞书配置后插入一条 `open` 告警必须产生一个 `opened` 发件箱项；把告警更新为 `resolved` 必须产生一个 `recovered` 项。

```go
func TestNotificationMigrationCreatesAtomicAlertOutbox(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	for _, table := range []string{"notification_configs", "notification_outbox", "alert_rule_states"} {
		if !tableExists(t, db, table) { t.Fatalf("缺少表 %s", table) }
	}
}
```

- [ ] **Step 2: 运行迁移测试确认失败**

Run: `go test ./internal/store/postgres -run Notification -count=1`

Expected: FAIL，提示新表不存在。

- [ ] **Step 3: 编写向上迁移**

`000011_notifications.up.sql` 包含以下核心结构：

```sql
ALTER TABLE secrets
    ADD COLUMN scope text NOT NULL DEFAULT 'user'
    CHECK (scope IN ('user', 'system'));

CREATE TABLE notification_configs (
    channel text PRIMARY KEY CHECK (channel IN ('feishu')),
    enabled boolean NOT NULL DEFAULT false,
    webhook_secret_id uuid NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    signing_secret_id uuid NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    masked_destination text NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id uuid REFERENCES alerts(id) ON DELETE RESTRICT,
    event_type text NOT NULL CHECK (event_type IN ('opened', 'recovered', 'test')),
    payload jsonb NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'retrying', 'sent', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    last_error text NOT NULL DEFAULT '',
    response_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_outbox_due_idx
    ON notification_outbox (next_attempt_at, created_at)
    WHERE status IN ('pending', 'retrying', 'sending');

CREATE TABLE alert_rule_states (
    code text NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    desired_active boolean NOT NULL DEFAULT false,
    consecutive_bad integer NOT NULL DEFAULT 0 CHECK (consecutive_bad >= 0),
    consecutive_good integer NOT NULL DEFAULT 0 CHECK (consecutive_good >= 0),
    last_value double precision,
    last_evaluated_at timestamptz NOT NULL,
    PRIMARY KEY (code, source_type, source_id)
);

INSERT INTO schema_migrations (version) VALUES (11)
ON CONFLICT (version) DO NOTHING;
```

再创建 `enqueue_alert_notification()` 触发器函数：仅当 `notification_configs.channel='feishu' AND enabled=true` 时，在告警 INSERT/open 或 UPDATE 到 resolved 时插入冻结的中文 JSON 载荷；载荷只包含 code、severity、title、source_type、source_id、occurrence_count 和时间，不复制可能含底层诊断的 `alerts.message`。幂等键分别为 `alert:{id}:opened` 与 `alert:{id}:recovered`。recovered 事件还必须存在同一 alert ID 的 opened 发件箱项，避免通知停用期间打开的旧告警在重新启用后只发送孤立恢复消息。触发器必须与告警写入处于同一事务。

- [ ] **Step 4: 编写向下迁移并验证**

向下迁移先删除 `schema_migrations` 的版本 11，再按依赖顺序删除触发器、函数、三个新表，最后删除 `secrets.scope`。

Run: `go test ./internal/store/postgres -run Notification -count=1`

Expected: PASS。

- [ ] **Step 5: 写秘密作用域失败测试**

测试 `Create` 产生 `scope=user`，`CreateSystem` 产生 `scope=system`，`List` 只返回用户秘密，`Resolve` 可以按 ID 解密系统秘密：

```go
systemSecret, err := service.CreateSystem(ctx, "notification/feishu/signing/test-id", []byte("signing-secret"))
if err != nil { t.Fatal(err) }
values, err := service.Resolve(ctx, []secret.ID{systemSecret.ID})
if err != nil || string(values[systemSecret.ID]) != "signing-secret" { t.Fatalf("解密失败：%v", err) }
```

- [ ] **Step 6: 运行秘密测试确认失败**

Run: `go test ./internal/secret -run 'System|Scope|Resolve' -count=1`

Expected: FAIL，提示作用域或方法不存在。

- [ ] **Step 7: 扩展秘密服务**

新增：

```go
type Scope string
const (
	ScopeUser Scope = "user"
	ScopeSystem Scope = "system"
)

func (s *Service) CreateSystem(ctx context.Context, name string, plaintext []byte) (Metadata, error)
func (s *Service) Resolve(ctx context.Context, ids []ID) (map[ID][]byte, error)
```

`Create` 固定 `ScopeUser`；`CreateSystem` 固定 `ScopeSystem`。仓储 `List` 加 `WHERE scope='user'`，避免内部通知秘密出现在“参数与密钥”页面。`Resolve` 调用者负责使用后 `clear` 每个明文字节切片。

- [ ] **Step 8: 运行迁移和秘密包测试**

Run: `go test ./internal/secret ./internal/store/postgres -count=1`

Expected: PASS。

- [ ] **Step 9: 提交通知基础结构**

```bash
git add migrations/000011_notifications.* internal/store/postgres/migrations_test.go internal/secret
git commit -m "feat: 建立可靠通知数据结构"
```

### Task 2: 飞书配置服务与安全 API

**Files:**
- Create: `internal/notification/model.go`
- Create: `internal/notification/config.go`
- Create: `internal/notification/config_test.go`
- Create: `internal/notification/postgres.go`
- Create: `internal/notification/postgres_test.go`
- Create: `internal/operationshttp/handler.go`
- Create: `internal/operationshttp/handler_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Consumes: `secret.Service.CreateSystem` 和 `secret.Service.Resolve`（Task 1）。
- Produces: `notification.ConfigService.Get(ctx) (FeishuConfigView, error)`。
- Produces: `notification.ConfigService.Update(ctx, actorID, ipAddress string, input FeishuConfigInput) (FeishuConfigView, error)`。
- Produces: `notification.PostgresRepository.DeleteUnreferencedSystemSecrets(ctx) error`。
- Produces: `GET/PUT /api/operations/notifications/feishu`。

- [ ] **Step 1: 写 Webhook 校验失败测试**

表驱动测试必须拒绝 HTTP、用户信息、端口、查询串、片段、其他主机、错误路径和转义路径，只接受标准 V2 地址：

```go
valid := "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"
if err := notification.ValidateFeishuWebhook(valid); err != nil { t.Fatal(err) }
for _, value := range []string{
	"http://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
	"https://127.0.0.1/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
	"https://open.feishu.cn.evil.example/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
} {
	if notification.ValidateFeishuWebhook(value) == nil { t.Fatalf("必须拒绝 %s", value) }
}
```

- [ ] **Step 2: 运行配置测试确认失败**

Run: `go test ./internal/notification -run 'Webhook|Config' -count=1`

Expected: FAIL，提示包或方法不存在。

- [ ] **Step 3: 实现模型、校验和配置保存**

定义：

```go
type FeishuConfigInput struct {
	Enabled       bool   `json:"enabled"`
	Webhook       string `json:"webhook"`
	SigningSecret string `json:"signingSecret"`
}
type FeishuConfigView struct {
	Configured        bool      `json:"configured"`
	Enabled           bool      `json:"enabled"`
	MaskedDestination string    `json:"maskedDestination"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}
```

更新规则明确如下：首次配置或轮换时 Webhook 与签名密钥必须同时提供；只修改启停状态时两者同时留空并复用已有引用；只提供其中一个必须拒绝；没有已有配置时不能用空值启用。需要轮换时，`Update` 先完成校验，再用 `notification/feishu/` 前缀加随机 UUID 名称创建两个 system scope 秘密，最后在同一事务中 upsert `notification_configs` 并追加审计：启用/轮换使用 `operations.feishu.update`，停用使用 `operations.feishu.disable`。响应只返回 `FeishuConfigView`。`masked_destination` 只保留固定前缀和 token 最后 4 位；被替换或因配置事务失败而失去引用的 system secret 不出现在用户列表。`DeleteUnreferencedSystemSecrets` 只删除名称以 `notification/feishu/` 开头、`scope='system'` 且未被 `notification_configs` 引用的记录，并在配置事务成功后调用；清理失败记录脱敏日志，但不回滚已生效配置。

- [ ] **Step 4: 写 PostgreSQL 与 HTTP 失败测试**

覆盖配置保存后数据库仅有 secret ID；GET 不包含 `webhook`、`ciphertext`、`signingSecret`；viewer PUT 返回 403；admin PUT 成功；跨域 Origin 与错误 Content-Type 被拒绝；审计动作为 `operations.feishu.update`。

- [ ] **Step 5: 运行接口测试确认失败**

Run: `go test ./internal/notification ./internal/operationshttp -run 'Config|Feishu' -count=1`

Expected: FAIL，直到仓储和处理器存在。

- [ ] **Step 6: 实现仓储和 HTTP 路由**

`operationshttp.NewHandler(Services{Notifications: configService}, allowedOrigin)` 注册 GET/PUT。GET 使用 `auth.PermissionRead`，PUT 使用 `auth.PermissionAdmin`。PUT 限制 8 KiB、拒绝未知 JSON 字段、要求同源和 `application/json`。成功 PUT 返回 200 的脱敏 view；配置禁用仍保留秘密引用，便于再次启用。

在 `cmd/api/main.go` 初始化现有 `secretService` 后组装 handler，并注册：

```go
router.Handle("/api/operations/", operationsHandler)
```

未配置主密钥时 GET 返回明确的 503，不能返回空配置冒充可用。

- [ ] **Step 7: 运行配置服务和 API 测试**

Run: `go test ./internal/notification ./internal/operationshttp ./cmd/api -count=1`

Expected: PASS。

- [ ] **Step 8: 提交飞书配置 API**

```bash
git add internal/notification internal/operationshttp cmd/api/main.go
git commit -m "feat: 提供飞书通知安全配置"
```

### Task 3: 飞书签名发送器与持久化发件箱

**Files:**
- Create: `internal/notification/feishu.go`
- Create: `internal/notification/feishu_test.go`
- Create: `internal/notification/outbox.go`
- Create: `internal/notification/outbox_test.go`
- Modify: `internal/notification/postgres.go`
- Modify: `internal/notification/postgres_test.go`
- Modify: `internal/operationshttp/handler.go`
- Modify: `internal/operationshttp/handler_test.go`

**Interfaces:**
- Produces: `SignFeishu(timestamp int64, secret string) string`。
- Produces: `FeishuClient.Send(ctx context.Context, webhook, secret string, payload FrozenMessage) (responseID string, err error)`。
- Produces: `OutboxService.EnqueueTest(ctx, actorID string) (Delivery, error)` 和 `DeliverDue(ctx) error`。
- Produces: `POST /api/operations/notifications/feishu/test`、`GET /api/operations/notifications/{id}`。

- [ ] **Step 1: 写签名与 HTTP 客户端失败测试**

用固定时间戳和密钥独立计算 HMAC-SHA256/Base64 期望值，断言请求 JSON 含 `timestamp`、`sign`、`msg_type="interactive"`。测试客户端：

- 不跟随 301/302。
- 总超时 10 秒。
- 响应体最多读取 64 KiB。
- HTTP 2xx 但 JSON `code != 0` 仍视为失败。
- 错误中不含 webhook token、secret、sign 或完整响应体。

- [ ] **Step 2: 运行发送器测试确认失败**

Run: `go test ./internal/notification -run 'SignFeishu|FeishuClient' -count=1`

Expected: FAIL，提示发送器不存在。

- [ ] **Step 3: 实现签名与发送器**

签名实现必须与飞书 V2 规则一致：

```go
func SignFeishu(timestamp int64, secret string) string {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
```

HTTP client 设置 `CheckRedirect` 返回 `http.ErrUseLastResponse`。只接受 `open.feishu.cn` 的已验证 URL；发送中文交互卡片但不使用可回调按钮。成功兼容官方 `code: 0` 响应和 V2 可能返回的消息字段，其他响应统一形成有界、脱敏错误。

- [ ] **Step 4: 写发件箱领取与退避失败测试**

覆盖两个 worker 并发时同一项只被领取一次、租约过期可接管、成功变 `sent`、失败按固定表进入 `retrying`、第 24 次失败进入终态 `failed`。退避函数固定：

```go
func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1: return time.Minute
	case 2: return 5 * time.Minute
	case 3: return 15 * time.Minute
	case 4: return time.Hour
	default: return 6 * time.Hour
	}
}
```

- [ ] **Step 5: 运行发件箱测试确认失败**

Run: `go test ./internal/notification -run 'Outbox|Retry|Claim' -count=1`

Expected: FAIL，直到领取和状态机实现。

- [ ] **Step 6: 实现发件箱服务**

定义 `Delivery`、`FrozenMessage`、`OutboxRepository`。PostgreSQL 领取使用 `FOR UPDATE SKIP LOCKED`，领取时把状态设为 `sending`、`lease_until=now+30s`、`attempts=attempts+1`。发送前通过配置引用解密两个秘密，用后立即 `clear`。飞书不可用只更新 delivery，不调用 `alert.Service.Raise`，避免递归。

`EnqueueTest` 写入 `event_type=test`，幂等键包含请求 UUID；返回 202 delivery。GET 只返回状态、尝试次数、脱敏错误和时间。

- [ ] **Step 7: 接入测试消息 API 并验证**

Run: `go test ./internal/notification ./internal/operationshttp -count=1`

Expected: PASS。

- [ ] **Step 8: 提交发送器与发件箱**

```bash
git add internal/notification internal/operationshttp
git commit -m "feat: 可靠发送飞书告警"
```

### Task 4: 告警恢复生命周期与阈值规则

**Files:**
- Modify: `internal/alert/service.go`
- Modify: `internal/alert/service_test.go`
- Modify: `internal/alert/postgres.go`
- Modify: `internal/alert/postgres_test.go`
- Create: `internal/ops/rules.go`
- Create: `internal/ops/rules_test.go`
- Create: `internal/ops/postgres.go`
- Create: `internal/ops/postgres_test.go`
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/main_test.go`

**Interfaces:**
- Produces: `alert.Service.Resolve(ctx, resourceType, resourceID, code string) error`。
- Produces: `ops.RuleEngine.Scan(ctx context.Context) error`。
- Produces: `ops.RuleRepository`，原子保存 `alert_rule_states` 并返回需要 Raise/Resolve 的转换。

- [ ] **Step 1: 写告警恢复失败测试**

服务和 PostgreSQL 测试必须证明：确认后的告警仍可恢复；重复 Resolve 幂等；恢复时 `resolved_at` 有值；数据库触发器只生成一条 recovered 发件箱消息。

```go
if err := service.Resolve(ctx, "server", "server-1", "agent_offline"); err != nil { t.Fatal(err) }
items, _ := service.List(ctx)
if items[0].Status != alert.StatusResolved || items[0].ResolvedAt == nil { t.Fatalf("未恢复：%+v", items[0]) }
```

- [ ] **Step 2: 运行恢复测试确认失败**

Run: `go test ./internal/alert -run Resolve -count=1`

Expected: FAIL，提示 `Resolve` 或 `ResolvedAt` 不存在。

- [ ] **Step 3: 实现恢复仓储与模型**

给 `Alert` 增加 `ResolvedAt *time.Time`，仓储查询补齐字段。`Resolve` 更新同一 code/source 的最新 `open` 或 `acknowledged` 告警为 `resolved`；没有活动告警时成功返回，不创建新记录。

- [ ] **Step 4: 写规则引擎失败测试**

使用固定时钟和内存仓储覆盖：

- 已启用服务器最后心跳 119 秒不告警，120 秒告警；恢复心跳后 Resolve。
- 已停用服务器不告警，排空服务器仍监控。
- queued 599 秒不告警，600 秒告警；assigned/cancelled/finished 后 Resolve。
- failed/timed_out 运行每个 ID 只 Raise 一次。
- 内存低于 10% 连续两次 Raise，高于 15% 连续两次 Resolve。
- 磁盘低于 15% 连续两次 Raise，高于 20% 连续两次 Resolve。
- 过期或总量为 0 的快照不参与资源规则。

- [ ] **Step 5: 运行规则测试确认失败**

Run: `go test ./internal/ops -run Rule -count=1`

Expected: FAIL，提示 `RuleEngine` 不存在。

- [ ] **Step 6: 实现规则查询和状态机**

定义：

```go
type AlertSink interface {
	Raise(context.Context, alert.Event) error
	Resolve(context.Context, string, string, string) error
}
type RuleEngine struct { repository RuleRepository; alerts AlertSink; now func() time.Time }
```

PostgreSQL 仓储每次读取：启用服务器及最新两个快照、已有 active/desired 服务器规则对应的服务器、10 分钟前仍排队的运行、已有排队告警对应的运行，以及最近终止失败且规则状态尚不存在的运行。资源连续计数只在快照 `collected_at` 晚于 `last_evaluated_at` 时增加，禁止同一心跳样本被多次计数；已停用服务器把离线和资源规则目标状态设为 inactive。事务更新连续计数和 `desired_active`，但 `active` 表示已经成功应用到告警服务的状态。扫描器对 `desired_active != active` 的记录先调用 Raise/Resolve，成功后再用 compare-and-swap 把 `active` 更新为目标值；若进程在两步之间退出，下次扫描会重放，而 Raise 合并、Resolve 幂等，因此不会丢失转换。

从 `cmd/api/main.go` 的 `offlineRunPublisher` 移除 15 秒离线告警，只保留运行状态对账；服务器是否可调度仍可在 15 秒失联后变化，外部告警统一由 Ops 按 2 分钟规则发出。

- [ ] **Step 7: 运行告警、Ops 与 API 测试**

Run: `go test ./internal/alert ./internal/ops ./cmd/api -count=1`

Expected: PASS。

- [ ] **Step 8: 提交告警规则**

```bash
git add internal/alert internal/ops cmd/api/main.go cmd/api/main_test.go
git commit -m "feat: 检测并恢复运维告警"
```

### Task 5: 独立 Ops 服务与安全部署

**Files:**
- Create: `cmd/ops/main.go`
- Create: `cmd/ops/main_test.go`
- Create: `internal/ops/loop.go`
- Create: `internal/ops/loop_test.go`
- Create: `deploy/Dockerfile.ops`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/.env.example`
- Modify: `Makefile`
- Modify: `tests/integration/deployment_security_test.go`

**Interfaces:**
- Consumes: `ops.RuleEngine.Scan`（Task 4）和 `notification.OutboxService.DeliverDue`（Task 3）。
- Produces: `yunling-ops` 可执行文件、容器健康端点 `GET /healthz` 和 15 秒扫描循环。

- [ ] **Step 1: 写配置和循环失败测试**

测试缺少数据库或主密钥文件时配置失败；默认扫描间隔 15 秒；单次规则失败不会阻止发件箱扫描；取消 context 后 1 秒内退出。

```go
config, err := loadConfig(mapEnv(map[string]string{
	"YUNLING_DATABASE_URL": "postgres://example",
	"YUNLING_MASTER_KEY_FILE": "/run/secrets/yunling-master-key",
}))
if err != nil || config.ScanInterval != 15*time.Second { t.Fatalf("配置错误：%+v %v", config, err) }
```

- [ ] **Step 2: 运行 Ops 测试确认失败**

Run: `go test ./cmd/ops ./internal/ops -run 'Config|Loop' -count=1`

Expected: FAIL，提示入口或循环不存在。

- [ ] **Step 3: 实现入口、循环和健康端点**

`cmd/ops/main.go` 使用 `signal.NotifyContext`，连接 PostgreSQL，加载主密钥提供者，组装 alert、notification、rules 和 outbox。循环每 15 秒分别以 10 秒超时调用规则扫描和发件箱发送；错误只写脱敏日志。健康状态记录最近一次成功扫描时间，`/healthz` 在数据库不可用或超过 60 秒未完成任何扫描时返回 503，否则 200 `ok`。

- [ ] **Step 4: 写部署安全失败测试**

断言 Compose 中 Ops：不发布宿主机端口、`read_only: true`、`no-new-privileges:true`、用户 10001、只读主密钥、仅有 `/tmp` tmpfs、连接 backend 与新建 egress 网络、不挂载 Docker Socket。

- [ ] **Step 5: 运行部署测试确认失败**

Run: `go test ./tests/integration -run OpsDeployment -count=1`

Expected: FAIL，提示 Ops 服务不存在。

- [ ] **Step 6: 增加镜像、Compose 和构建目标**

`deploy/Dockerfile.ops` 先只构建/复制 `yunling-ops`，运行时使用 Alpine 3.24 和 UID/GID 10001。Compose 添加 `egress` 非 internal 网络；Ops 连接 `backend` 与 `egress`，不 expose 端口，仅由容器内 healthcheck 访问 127.0.0.1:8081。

`Makefile build` 增加：

```make
	go build -o bin/yunling-ops ./cmd/ops
```

- [ ] **Step 7: 运行 Ops 与部署验证**

Run: `go test ./cmd/ops ./internal/ops ./tests/integration -run 'Ops|Notification|Rule' -count=1`

Expected: PASS。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`

Expected: 退出码 0。

- [ ] **Step 8: 提交 Ops 服务**

```bash
git add cmd/ops internal/ops/loop.go internal/ops/loop_test.go deploy/Dockerfile.ops deploy/docker-compose.yml deploy/.env.example Makefile tests/integration/deployment_security_test.go
git commit -m "feat: 启动独立运维通知服务"
```

### Task 6: 飞书通知控制台与端到端测试

**Files:**
- Create: `apps/web/src/features/operations/NotificationSettingsPanel.tsx`
- Create: `apps/web/src/features/operations/NotificationSettingsPanel.test.tsx`
- Create: `apps/web/e2e/operations-notifications.spec.ts`
- Modify: `apps/web/src/features/operations/OperationsPage.tsx`
- Modify: `apps/web/src/features/operations/OperationsPage.test.tsx`
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/api/client.test.ts`
- Modify: `apps/web/src/app/styles.css`
- Modify: `apps/web/e2e/fixtures.ts`
- Modify: `apps/web/src/features/settings/AuditPage.tsx`
- Modify: `deploy/README.md`

**Interfaces:**
- Consumes: 飞书配置与测试 delivery API（Tasks 2–3）。
- Produces: `NotificationSettingsPanel` 和客户端类型 `FeishuNotificationConfig`、`NotificationDelivery`。

- [ ] **Step 1: 写客户端失败测试**

新增并测试：

```ts
export interface FeishuNotificationConfig {
  configured: boolean
  enabled: boolean
  maskedDestination: string
  updatedAt?: string
}
export interface NotificationDelivery {
  id: string
  status: 'pending' | 'sending' | 'retrying' | 'sent' | 'failed'
  attempts: number
  lastError?: string
  sentAt?: string
}
```

方法为 `getFeishuNotificationConfig`、`updateFeishuNotificationConfig`、`testFeishuNotification`、`getNotificationDelivery`，路径必须与设计一致。

- [ ] **Step 2: 运行客户端测试确认失败**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts`

Expected: FAIL，提示新方法未导出。

- [ ] **Step 3: 实现客户端方法**

PUT 请求体只包含 `enabled`、`webhook`、`signingSecret`。测试通知 POST 返回 delivery；轮询 GET 不超过 30 秒，页面卸载后停止。

- [ ] **Step 4: 写面板失败测试**

覆盖：初次未配置、保存成功后只显示脱敏目标、DOM 不含完整 Webhook/密钥、发送测试后从 pending 轮询到 sent、failed 显示有界中文错误、viewer 只读、admin 可编辑。

```tsx
await user.type(screen.getByLabelText('飞书 Webhook'), validWebhook)
await user.type(screen.getByLabelText('签名密钥'), 'signing-secret')
await user.click(screen.getByRole('button', { name: '保存通知设置' }))
expect(await screen.findByText(/已配置.*cdef/)).toBeVisible()
expect(document.body).not.toHaveTextContent(validWebhook)
```

- [ ] **Step 5: 运行面板测试确认失败**

Run: `npm --workspace apps/web test -- --run src/features/operations/NotificationSettingsPanel.test.tsx`

Expected: FAIL，提示组件不存在。

- [ ] **Step 6: 实现面板、审计文案和样式**

面板输入值只保存在组件 state；GET 后仅显示 `maskedDestination`；保存成功立即清空密钥字段。测试按钮在未配置/停用时禁用。把上一份“管理员改密”计划创建的账号安全面板和通知面板一起放在 `OperationsPage`，保持移动端单列、焦点可见和错误区域可聚焦。

`AuditPage.tsx` 增加 `operations.feishu.update`、`operations.feishu.disable`、`operations.feishu.test` 中文映射。

- [ ] **Step 7: 增加 E2E 和部署说明**

E2E 模拟管理员保存、刷新后仅见脱敏值、发送测试并显示“飞书测试消息已发送”。`deploy/README.md` 说明从飞书群添加 V2 自定义机器人、启用签名校验、通过控制台录入，明确禁止把 Webhook 或密钥粘贴到终端历史、工单或代码仓库。

- [ ] **Step 8: 运行完整验证**

Run: `go test ./... -count=1`

Expected: PASS。

Run: `npm --workspace apps/web test -- --run`

Expected: PASS。

Run: `npm --workspace apps/web run build`

Expected: PASS。

Run: `npm --workspace apps/web run test:e2e -- operations-notifications.spec.ts`

Expected: PASS。

Run: `git diff --check`

Expected: 无输出，退出码 0。

- [ ] **Step 9: 提交飞书控制台与验收**

```bash
git add apps/web/src apps/web/e2e deploy/README.md
git commit -m "feat: 完成飞书通知控制台"
```

### Plan 2 完成检查点

- [ ] 飞书配置只以系统秘密形式保存，普通秘密列表不可见。
- [ ] 告警开启/恢复与发件箱写入具备数据库原子性。
- [ ] 并发领取、租约接管、五档退避和终态失败测试通过。
- [ ] 离线、排队、失败、内存和磁盘规则及恢复阈值测试通过。
- [ ] `yunling-ops` 具备独立健康检查和最小容器权限。
- [ ] 控制台保存后只显示脱敏目标，测试消息 E2E 通过。
- [ ] 尚未在生产发送消息；真实飞书测试留到三份计划全部实现后的生产上线检查点。
