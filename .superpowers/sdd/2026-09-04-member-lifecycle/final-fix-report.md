# 团队成员生命周期最终集中修复报告

日期：2026-09-04（Asia/Shanghai）
分支：`codex/member-lifecycle`
修复基准：`691b52cc369b1b116260c9243164e6b4162f1a45`

## 结论

`final-review-report.md` 中的 6 个 Important 和 2 个 Minor 已在一次集中修复中全部处理。每个缺陷类别均先增加可复现测试并观察预期 RED，再做最小实现转为 GREEN。聚焦测试、全仓 Go、前端全量单测、前端生产构建、完整 E2E 和 `git diff --check` 均通过。

本次只修改并验证本地仓库，没有推送、创建或合并 PR，没有部署、连接或写入生产服务器，也没有执行任何生产迁移。

## 逐项修复与 RED/GREEN 证据

### Important 1：登录验证与会话创建不原子

修复：

- `StoredSession` 携带登录时读取的 `ExpectedPasswordHash`。
- PostgreSQL 会话创建改为短事务：锁定并重新确认同一用户仍启用、未移除且密码哈希仍等于认证快照，随后才插入会话并提交。
- 服务将该条件失效统一映射为 `ErrInvalidCredentials`，不会向客户端返回已经失效的会话。
- 新增真实 PostgreSQL 并发测试，分别把登录暂停在密码验证与会话创建之间，同时执行停用、移除、管理员重置密码和用户自行改密。

RED：`TestPostgresLoginRejectsCredentialSnapshotAfterConcurrentMutation` 的四个子场景都能在生命周期/密码事务提交后插入未撤销的新会话，旧凭据仍得到成功登录结果。

GREEN：四个子场景均拒绝旧认证快照，且没有留下可用或可复活的会话；包含该组的 `internal/auth` 真实 PostgreSQL 测试通过。

### Important 2：UUID 原始文本可绕过自操作保护

修复：

- HTTP 路径边界通过 `google/uuid` 解析目标 ID，非法 UUID 在调用服务前返回 400。
- `TeamService` 对 actor/target 都做 UUID 解析、规范化和身份比较。
- PostgreSQL 仓储的所有成员写入口再次规范化并执行自操作保护，形成纵深校验。

RED：

- 大写和无连字符的同一 UUID 没有触发自操作保护，服务返回成功。
- 非法 UUID 能下沉到服务/数据库；HTTP 回归测试观察到 200 且服务被调用，而不是 400。

GREEN：

- `TestTeamServiceRejectsEquivalentUUIDSelfMutation`、`TestPostgresTeamServiceRejectsEquivalentUUIDSelfMutation` 覆盖大写和无连字符等价写法并通过。
- `TestTeamServiceRejectsInvalidUUIDMutation` 与 `TestMemberMutationRejectsInvalidPathUUIDBeforeCallingService` 通过；非法路径返回 400，服务调用数为 0。

### Important 3：最后管理员交叉操作死锁

修复：

- 角色替换、停用、移除三个可能减少有效管理员的事务统一先获取 `yunling-member-admin` advisory transaction lock，再锁目标用户行。
- 最后管理员计数改为只在统一锁序内读取，不再在目标行锁之后尝试获取 advisory lock。

RED：`TestPostgresMemberCrossAdminMutationsSerializeWithoutDeadlock` 使用两个管理员、两个真实数据库连接和同步屏障交叉执行降权、停用、移除；三个场景均复现 PostgreSQL `SQLSTATE 40P01 deadlock_detected`。

GREEN：三个场景均无死锁，恰有一个操作成功，另一个得到最后管理员冲突，最终至少保留一名有效管理员。

### Important 4：前端局部更新破坏筛选语义

修复：

- 增加单一 `matchesFilter` 规则和 `upsertMember` 更新路径。
- 创建、角色调整、启停、恢复、密码重置成功后，成员按当前筛选插入、替换或移除。
- 切换筛选开始请求时立即清空旧行；请求失败显示错误和空列表，不把上一筛选数据冒充当前筛选结果。

RED：`MembersPage.test.tsx` 新增场景首次运行 7 项失败、6 项通过；失败覆盖旧筛选行残留、在“已停用”中插入新启用成员、启停后留在相反筛选、恢复后仍留在“已移除”等行为。

GREEN：`MembersPage.test.tsx` 13/13 通过；前端全量为 22/22 文件、102/102 测试通过。

### Important 5：首次改密 E2E 是伪闭环

修复：

- stateful fixture 在管理员创建成员后保持管理员会话，不再发生现实中不可能的会话跳变。
- fixture 增加账号凭据、登录、退出、会话、改密和 `password_change_required` 后端门禁状态。
- E2E 捕获一次性密码，验证管理员刷新后仍为管理员，再退出并用临时密码登录；验证受保护成员 API 精确返回 403 门禁；真实提交当前/新密码后进入运行总览；最后验证旧临时密码失败、新密码登录成功。

RED：修正后的 E2E 首次运行时，“管理员创建成员并完成首次改密流程”在创建后刷新即失败，页面没有“创建成员”按钮，直接暴露旧 fixture 把管理员会话替换为新成员的错误。

GREEN：成员生命周期聚焦 E2E 2/2 通过；最终完整 Playwright E2E 7/7 通过。

### Important 6：缺少 v12→v13 真实升级和可执行 rollout

修复：

- 新增真实 PostgreSQL 升级测试：只应用迁移 1–12，确认版本 12，写入既有用户、管理员角色和活动会话，再应用迁移 13；验证默认字段、索引、密码登录和现有会话 Principal 均兼容。
- `yunling-release migration apply` 提供 root-only 显式入口，与标准发布共用 release lock。
- rollout 校验受信任候选清单、完整候选迁移树摘要、精确 1–13 up/down 文件、v13 文件摘要，以及 v1–v12 历史树摘要与当前生产基线完全一致；因此不会降低或绕过迁移摘要保护。
- 数据库预检要求当前版本为 12 或可恢复重试的 13，并要求指定备份同时具备本机/COS 成功快照、有效清单摘要和迁移版本 12 的成功隔离恢复核验。
- v12 状态在显式事务中执行候选 v13 SQL；后置核验版本、两个字段、索引、用户数和活动会话数。所有核验完成后才排他写入 root-only、不可变且绑定当前目标/候选 ID/完整源 SHA/前后摘要/v13 文件摘要/恢复点/操作者的 `migration-baselines/<candidate>.json`。
- 标准发布仍默认拒绝兼容性摘要变化，只接受与当前版本和目标候选精确匹配、且所有非迁移兼容字段不变的迁移基线。
- 候选 bootstrap 包现在携带完整 `migrations/`，受既有 SHA256SUMS 与 artifact attestation 保护。
- `deploy/RELEASE.md`、`deploy/README.md` 和 `deploy/PRODUCTION.md` 记录恢复点、审批、锁窗口、执行、核验、基线、重试和保守回滚；生产记录明确标为 v13 待执行，没有伪造上线结果。

RED：

- rollout/CLI 测试最初因不存在 `MigrationRollout`、`MigrationRequest`、基线 API 和 CLI 依赖而编译失败。
- append-only 测试将 `000001` 改写并同步更新候选全树摘要后，旧实现仍访问数据库并开始迁移（`calls=2`），而不是在 DB 访问前以 `ErrMigrationDigestMismatch` 拒绝。
- 候选制品/手册绑定测试失败，明确报告 bootstrap 缺少 `install -d -m 0700 "$stage/migrations"`。

GREEN：

- `TestMigrationRolloutAppliesV13AndAuthorizesOnlyBoundCandidate`、篡改/历史改写/后置核验失败、CLI root 门禁及制品/手册绑定测试全部通过。
- `go test ./internal/release ./cmd/yunling-release -count=1` 通过。
- `TestMemberLifecycleMigrationUpgradesExistingV12LoginAndSession` 在真实 PostgreSQL 上通过（测试体 9.90s，包 10.628s）。

### Minor 1：已移除成员仍显示“调整角色”

修复：已移除成员的操作菜单只渲染“恢复成员”；恢复后才重新提供角色、启停、重置和移除操作。

RED：`已移除成员只能恢复，不能调整角色` 首次观察到“调整角色”仍可见。

GREEN：该测试通过，并由完整前端单测覆盖。

### Minor 2：触发元素卸载及角色保存失败时焦点丢失

修复：

- 关闭对话框时先尝试仍连接到文档的原触发元素；若行已因筛选语义修正而卸载，则聚焦当前筛选按钮。
- 角色对话框拥有独立错误摘要 ref/effect，保存失败时把焦点移到可见 `role=alert`。

RED：恢复后原行卸载时焦点没有落在可见控件；角色保存错误摘要渲染后未获得焦点。

GREEN：`恢复成员后移出已移除筛选并把焦点退回当前筛选按钮` 和 `角色保存失败时把焦点移到对话框错误摘要` 均通过；完整 E2E 也验证恢复后当前筛选按钮获得焦点。

## 完整验证

所有 Go 命令均使用仓库内 Go 1.27.1、`.tools/gomodcache`、`.tools/gocache` 和 `GOPROXY=off`，没有联网下载依赖。

| 验证 | 结果 |
| --- | --- |
| 后端服务/HTTP 聚焦测试 | PASS |
| 真实 PostgreSQL 登录竞态与交叉管理员测试 | PASS；包含停用、移除、管理员重置、自行改密及降权/停用/移除交叉操作 |
| `go test ./internal/auth ./internal/securityhttp -count=1` | PASS；`auth 259.397s`，`securityhttp 0.436s`（聚焦阶段） |
| `go test ./internal/release ./cmd/yunling-release -count=1` | PASS；`release 1.770s`，`cmd/yunling-release 0.453s` |
| 真实 v12→v13 PostgreSQL 升级 | PASS；测试体 `9.90s`，包 `10.628s` |
| `go test ./... -count=1` | PASS，退出码 0；最慢 `internal/auth 419.425s`，全仓所有包完成 |
| `npm run test:web` | PASS；22/22 文件、102/102 测试，最终复跑 9.08s |
| `npm run build:web` | PASS；TypeScript 与 Vite 生产构建完成 |
| `npm run test:e2e` | PASS；7/7，6.5s |
| `git diff --check` | PASS；无空白错误 |

E2E 环境说明：本机没有 Playwright 捆绑 Chromium，且默认 4173 端口被另一个旧服务占用。最终验证使用系统 Chrome（`YUNLING_E2E_CHANNEL=chrome`）和当前 worktree 独立 Vite 地址 `http://127.0.0.1:43173`；7/7 通过后已停止该服务。两项环境问题不作为产品 RED 证据。

Race 验证没有伪称通过：

- `go test -race -p=1 ./... -count=1` 立即返回 `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`。
- 设置 `CGO_ENABLED=1` 后聚焦尝试返回 `cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%`。
- 因此本机无法执行 race；合并门禁仍须在具备 GCC/CGO 的 Linux CI 上运行并通过 `go test -race -p=1 ./... -count=1`。

Windows 普通权限还无法创建 symlink，所以既有迁移摘要 symlink 子测试按测试自身逻辑跳过；空树、内容/路径摘要、候选全树与历史前缀保护均已执行并通过。

## 提交

- `14a6c80ebe47aba8edde7c74a0b2aed792ceaf3e` — `fix(auth): close member lifecycle race windows`
- `385ed1405208c31f869506b3f0188f735f0117d5` — `fix(web): preserve member lifecycle state and focus`
- `810d9a7ef911613611058e89bf30e09560581821` — `feat(release): add controlled member lifecycle migration rollout`

本报告在上述实现提交之后单独提交；报告提交 SHA 不在文件内自引用。

## 残余风险与上线条件

1. 本机缺少 GCC，未运行 race。必须以 Linux CI 的完整 race 结果作为合并条件。
2. 首次改密浏览器闭环使用忠实的 stateful Playwright fixture；后端安全边界另由真实 PostgreSQL 测试覆盖，但仍建议在候选环境保留一条连接真实 Go API/PostgreSQL 的冒烟验收。
3. 迁移 13 尚未在生产执行。生产仍为版本 12；必须先走独立审批、双份成功备份、版本 12 隔离恢复核验和候选绑定 rollout，不能直接部署新应用或手工补基线。
4. v13 使用事务内普通 `CREATE INDEX`，真实锁时长取决于生产 `users` 表规模。审批前必须记录表规模和锁窗口；若不可接受，应停止该候选并另行设计在线索引迁移，不能现场修改已摘要绑定的 SQL。
5. 迁移基线入口刻意只支持本次 12→13 成员生命周期迁移；未来迁移摘要变化仍会被默认发布链拒绝，必须另行设计、测试和审批。
