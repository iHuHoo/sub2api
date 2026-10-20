# Clash Subscription Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. 本轮只写计划，执行时才创建隔离 worktree 和修改产品代码。

**Goal:** 管理员和供应商可生成独立、可撤销的 Clash 订阅；供应商只能导出管理员分配给自己的有效代理。

**Architecture:** 新增独立订阅表、用户变更自动撤销触发器、订阅管理服务与专用下载路由。每次下载按数据库快照校验当前权限并生成完整 Mihomo YAML；不接入模型推理调度热路径。

**Tech Stack:** Go / Gin、PostgreSQL、现有 Redis 客户端、现有 YAML 序列化依赖、Vue / TypeScript / Vitest；沿用仓库依赖注入与错误响应约定。

**Spec:** [已通过复核的设计](../../CLASH_SUBSCRIPTION_DESIGN.md)，2026-10-20 第二轮复核通过；评审基线 main 合并提交 `5cb965d3b`。执行前核对实际 main；本机 main 引用可能陈旧，不在旧运维分支直接实现。

## Global Constraints

- 本文件最初为计划；设计及计划通过后，用户已授权隔离实现并提交待审 PR。生产迁移、PR 合并、发布和启用仍不包含在本轮工作中。
- 每用户一个订阅，供应商仅导出 `assigned_supplier_user_id = user_id`；管理员仅导出两个归属字段均 NULL 的公共池。现有列表继续脱敏，供应商改/删权限不变。
- token 至少 32 字节 CSPRNG，base64url 无 padding；仅存 SHA-256，只在创建/reset 返回一次。有效期 30/90/365 天，默认 90。
- 用户改角色、离开 active、软删除自动撤销；恢复用户不恢复旧链接。users → subscription 的锁顺序；所有写操作有明确事务边界。
- 下载按显式 SQL 过滤 user/proxy 软删除、角色、状态、到期和归属；不使用脱敏 DTO、缓存凭据、backup_proxy_id 或代理检测。
- HTTP/HTTPS/SOCKS5/SOCKS5h；socks5h 输出 socks5；SOCKS 空用户名非空密码跳过；HTTPS 开启证书验证；完整配置不加 DIRECT 自动回退。
- 零可导出节点：生成/reset 返回 409 `CLASH_NO_EXPORTABLE_PROXIES` 且不改记录；已有有效链接仍返回 200 REJECT YAML。前端生成/reset/复制禁用，撤销允许。
- `clash_subscription.enabled=false` 默认；true 必须配置 HTTPS `public_base_url`，生产为 `https://api.aoxtoken.com`；不使用 frontend_url，不加后台开关。
- 限流初值 IP 60/min、订阅 10/min，依赖故障 503；不复用 Redis 失败时放行的面板 limiter。
- 用户接受已下载凭据不可追回、在途快照可能完成、客户端可能保留旧配置；不得把这些边界包装成即时撤回保证。

## 对设计的两项纠正（本计划优先）

1. **OPTIONS/CORS：**全局 `middleware/cors.go` 在路由前处理 OPTIONS。保留其现有 204/403；OPTIONS 不纳入 405 或错误 Host 必须 404 的断言，不要求改全站中间件行为。订阅固定路径的非 OPTIONS 请求在任何可能写响应的路由处理器之前清除所有 `Access-Control-Allow-*` 及 `Access-Control-Expose-Headers`，随后才检查开关、Host、限流、token。覆盖成功、404、405、429、503；允许全局 OPTIONS 返回 CORS 头，因为没有配置正文。验证生产 CORS 配置及 allowAll=true 时，GET 下载无通配符 ACAO。
2. **DNS 前置检查：**启用前必须确认 api.aoxtoken.com 的有效 A/AAAA 与部署批准的源站地址吻合、不属于 Cloudflare 官方公布的 IPv4/IPv6 网段，并通过控制台/API核对 DNS 记录是“仅 DNS”（proxied=false）。不能只检查其中一项，也不能只看有没有 cf-ray。未通过或无证据则保持功能关闭。DNS/CDN 配置后续变更必须重新审核此条件。

这些是用户已接受的复核修正，不再发起设计复核；原设计 §6/§11 中关于 OPTIONS 的绝对表述以本节为准。

## Review Focus

1. 禁用与 reset 同时发生，恢复 active 后旧 token 不能复活（任务 1、2）。
2. 用户和代理的软删除不能被原生 SQL 漏掉；零节点与无效订阅必须区分（任务 2）。
3. CORS 抢先写响应、错误 Host、Gin 重定向不能泄漏配置或 token（任务 4）。
4. 特殊字符凭据、名称冲突、SOCKS5h 域名解析不能被“YAML 可解析”掩盖（任务 3、6）。
5. 复制前节点数改变、客户端拒绝空配置、DNS 改成代理不能导致虚假的撤销/脱敏承诺（任务 5、6）。

## 文件与接口分工

新文件均为计划路径，执行时依据真实结构确认，不为命名强行重构现有大文件。

| 责任 | 新增或修改路径 |
|---|---|
| 表、约束、触发器 | `backend/migrations/247_proxy_subscriptions.sql`；编号被占则取下一号 |
| 领域与业务 | `backend/internal/service/clash_subscription.go`、`clash_subscription_service.go` |
| 原生 SQL 与事务 | `backend/internal/repository/clash_subscription_repo.go`、`repository/wire.go` |
| YAML 生成 | `backend/internal/service/clash_subscription_yaml.go` |
| API | `backend/internal/handler/clash_subscription_handler.go`、`handler/wire.go`、Handlers 聚合所在文件 |
| 路由与限流 | `backend/internal/server/routes/clash_subscription.go`、现有路由注册点、`middleware/clash_subscription_limit.go` |
| 配置/依赖注入 | `backend/internal/config/config.go`、`config/config_test.go`、`service/wire.go`、`backend/cmd/server` 的 Wire 源与生成文件 |
| 前端 | `frontend/src/api/clashSubscription.ts`、`components/proxy/ClashSubscriptionDialog.vue`、两端 `ProxiesView.vue`、zh/en 文案模块 |
| 说明与上线清单 | 本计划、`docs/CLASH_SUBSCRIPTION_DESIGN.md`、新增 `docs/CLASH_SUBSCRIPTION_OPERATIONS.md`、现有配置示例 |

共享接口先定义，再实现：

- `SubscriptionActor{UserID int64, Role string}`：来自已鉴权会话，repository 仍从 users 重读权限。
- `SubscriptionMetadata{Exists, Active bool; Version int64; ExpiresAt, RevokedAt *time.Time; ExportableCount, SkippedCount int}`。
- `SubscriptionGrant{Metadata SubscriptionMetadata; SubscriptionURL string}`：只用于创建/reset 响应，不传入日志。
- Service：`GetMetadata(ctx, actor)`、`Create(ctx, actor, durationDays)`、`Reset(ctx, actor, expectedVersion, durationDays)`、`Revoke(ctx, actor, expectedVersion)`；返回具体领域类型与 error。
- Repository：`WithLockedOwner(ctx, actor, fn)` 在事务中先 `SELECT users … FOR UPDATE`；回调里的授权、查询节点、订阅 mutation 必须共用事务，不能调用另一个 DB connection。回调仅内存计算，不能网络访问。
- 下载 Repository：`ReadDownloadSnapshot(ctx, tokenHash, visitFn)` 在同一只读 REPEATABLE READ 快照中先取有效订阅+用户，再取符合范围的代理并调用 visitFn。有效订阅零节点与无效订阅分别返回，不用 inner join 的空结果混淆二者。计算并写回 HTTP 响应在事务结束后进行。
- `FilterClashProxies(proxies []Proxy) ExportSet`：统一生成/reset/metadata/download 的可导出判定；`RenderClashConfig(set ExportSet) ([]byte,error)` 只接收已授权数据。ExportSet 不实现会输出凭据的 String()/日志接口。

## Task 1：表、自动撤销与并发约束

**Files:** 新迁移；`repository/clash_subscription_repo.go`、`clash_subscription_repo_integration_test.go`；`service/clash_subscription.go`。

- [ ] 写隔离 PostgreSQL 集成测试：初次创建唯一约束、role/status/deleted_at 触发器、恢复后仍撤销、余额更新不触发、同值更新不触发、多条件更新仅 version+1、无订阅用户更新成功、物理删除级联。
- [ ] 运行 `cd backend && go test -race -tags=integration ./internal/repository -run 'TestClashSubscription' -count=1`，确认测试在缺少表/功能时失败；记录失败原因，不能以 Docker 未启动代替回归失败。
- [ ] 实现表、user_id/token_hash 唯一约束、owner_role 限定、version 正值、时间列，以及 AFTER UPDATE 触发器；只在真实角色变化/离开 active/首次软删除时更新订阅。已撤销记录保留首次 revoked_at，事件仍 version+1。
- [ ] 实现 users → subscription 锁顺序与唯一冲突映射 409。用两个独立事务、同步屏障测试“先 reset 后停用”及“先停用后 reset”，不靠 sleep 碰运气；恢复用户后旧 token 仍失败。
- [ ] 重跑集成测试；验证旧版本在保留新表和触发器的数据库上更新用户/余额仍正常。通过后提交 `feat: persist revocable clash subscriptions`。

## Task 2：订阅业务、实时授权与零节点

**Files:** `service/clash_subscription_service.go`、对应 `_test.go`；repository 实现与集成测试；`config/config.go` 与测试；现有配置示例。

- [ ] 先写失败测试：token 单次返回/仅 hash 落库，30/90/365 天，创建重复 409，reset/revoke version 冲突，普通用户/禁用用户/角色不符拒绝。
- [ ] 实现元数据、创建、reset、revoke，所有管理操作核对当前 user；创建/reset 在锁住用户的事务内查可导出节点，0 节点返回指定 409，旧 token/version/到期均不变。首次创建并发唯一冲突也返回 409。
- [ ] 下载 SQL 逐项写明 users.deleted_at/status/role、订阅 hash/revoked_at/expires_at、proxies.deleted_at/status/expires_at、归属谓词。用 A/B 供应商、自建节点、公共池、过期节点夹具证明互不越权；元数据/生成/reset 共用同一过滤规则。
- [ ] 为下载查询实现一致性快照，结果返回内部 subscription_id 供限流，凭据不能进入响应缓存。事务结束后可完成一次旧快照响应，不持锁等待客户端下载。
- [ ] 配置测试覆盖 disabled 默认值、enabled 且 URL 缺失/non-HTTPS/userinfo/query/fragment 启动失败；配置来源固定，验证路径前缀拼接。生成地址永不来自请求 Host。
- [ ] 运行 `go test -race -tags=unit ./internal/service ./internal/config -run 'TestClashSubscription'` 及任务 1 集成集；通过后提交 `feat: authorize clash subscription lifecycle`。

## Task 3：完整 Mihomo YAML

**Files:** `service/clash_subscription_yaml.go`、`clash_subscription_yaml_test.go`，必要的纯测试夹具。

- [ ] 表驱动失败测试：HTTP、HTTPS、SOCKS5、SOCKS5h；IPv6/域名；空认证、HTTP 空用户名非空密码、SOCKS 非法认证；密码含冒号/引号/换行、Unicode 名称、截断后重名、恶意 YAML 内容。
- [ ] 实现统一 Filter 与结构化 YAML encoder；节点名保留稳定 ID 后缀、最终唯一，不输出用户身份或自动拼接凭据。HTTP/HTTPS 保留认证值；SOCKS 非法用户名组合跳过。unsupported/invalid 只返回无凭据原因码。
- [ ] 固定配置按设计输出：mode rule、allow-lan false、mixed-port 7890、select AoxToken、单条 `MATCH,AoxToken`。零节点组只含 REJECT。HTTPS tls true 且不 skip verify，SOCKS udp false。
- [ ] `go test -race -tags=unit ./internal/service -run 'TestClashSubscription.*(YAML|Filter)'`；用固定版本 Mihomo 对非空与空夹具执行 `mihomo -t -f <fixture>`。工具/网络不可用时标待验，不能宣称客户端验收完成。
- [ ] 用隔离假 SOCKS 服务捕获目标地址类型，证明 socks5h 的目标域名原样送到代理端；不访问生产代理。通过后提交 `feat: render clash subscription profiles`。

## Task 4：路由、Host、CORS、限流与日志

**Files:** handler、routes、middleware 新文件及各自 `_test.go`；Wire/Handlers 注册；必要的现有日志中间件最小脱敏改动。

- [ ] 先搭包含真实全局 CORS 的路由测试：OPTIONS 依当前配置返回 204/403 且无配置正文；GET/HEAD/其他方法、正常/错误 Host 各覆盖。为订阅非 OPTIONS 链首个处理器加入响应头清理；不能在响应写出后才删除。
- [ ] 清理后按顺序执行：开关/Host → 方法 → IP 限流 → token 格式/有效快照 → subscription_id 限流 → YAML 响应。关闭/错误 Host 无配置；有效 Host 非 GET 且非 OPTIONS 显式 405。不要打开全站 HandleMethodNotAllowed，不依赖 Gin 默认重定向。
- [ ] 仅 canonical 固定路由导出；测试尾斜杠、双斜杠、编码路径不绕过鉴权，不把含 token 的 URL 放进 Location。若 Gin 自动重定向触发，针对该路径拒绝变体，不改全站路由习惯。
- [ ] 实现专用 Redis 原子计数；IP 60/min、有效订阅10/min。Redis 故障返回503且无配置；无效随机 token 不产生无限 token key，客户端 IP 只信任现有可信代理配置。
- [ ] 配置正文和管理 grant 响应从审计/日志正文采集排除；外层 logger 必须在输出前使用固定安全路径，不可只在 handler 脱敏而漏掉全局 logger。错误码、订阅内部 ID、数量、耗时可记录，不记录 hash/密码/token/完整 URL。
- [ ] 测试所有成功与错误的 no-store、nosniff/no-referrer，GET 无 Access-Control-Allow-*，allowAll=true 及生产等效配置均覆盖；OPTIONS 例外单独断言。Host 大小写/默认端口规范化，X-Forwarded-Host 不能绕过。
- [ ] 运行 `go test -race -tags=unit ./internal/handler ./internal/server/... -run 'TestClashSubscription'`；接线后运行编译与配置测试，检查 Wire 生成 diff，不带入 go.sum 噪音。通过后提交 `feat: serve authenticated clash subscriptions`。

## Task 5：管理员/供应商页面

**Files:** `api/clashSubscription.ts`、`components/proxy/ClashSubscriptionDialog.vue`、对应 `__tests__/ClashSubscriptionDialog.spec.ts`，两端 ProxiesView、zh/en 文案。

- [ ] 先写 Vitest 交互失败测试：未创建、有有效链接但明文不可恢复、撤销/到期、0 节点、409 version 冲突、feature disabled；供应商现有只读行按钮不退化。
- [ ] 页面级“Clash 订阅”弹窗共用组件，两端使用各自登录管理路由；默认90天。grant 链接只保存在组件内存，关闭即清除，不写本地持久存储、不把链接塞进 URL/analytics。
- [ ] 生成/reset/复制前更新元数据；0节点全部禁用并显示解释，撤销可用。后端409后刷新状态且不展示旧 grant 为新链接；并发冲突提示刷新重试，不自动再次 reset。
- [ ] token 请求晚返回而弹窗已关闭时丢弃并清空数据；创建成功却响应丢失时允许用户看到已存在状态并显式 reset，不自动重试轮换。
- [ ] 文案解释凭据可见、撤销不能回收旧配置、90天过期需重新导入；提供手动导入说明。profile-update-interval=1 可作为建议，客户端行为记录实测，不保证准时刷新。
- [ ] 在 frontend 运行 `pnpm exec vitest run src/components/proxy/__tests__/ClashSubscriptionDialog.spec.ts`、相关 ProxiesView 回归、`pnpm run typecheck`、`pnpm run lint:check`；通过后提交 `feat: manage clash subscriptions in proxy pages`。

## Task 6：完整验证、操作文档与 PR

**Files:** 新增 `docs/CLASH_SUBSCRIPTION_OPERATIONS.md`；设计/计划修正对齐；隔离验收脚本与证据放 `$CODEX_RUNTIME_DIR` 或 `/Users/dalu/aiworkspace/codex/runtime`，不放 /tmp。

- [ ] 对照设计逐项勾验；将设计 §6/§11 的 OPTIONS 规则同步为本计划例外，避免后续实现者读到两个版本。没有实现的功能不得标完成。
- [ ] 测试仅用新隔离库、Redis、假代理和假凭据；不克隆生产数据。用固定版本 Clash Verge 实机导入/更新/选择/重置；记录空订阅后旧节点是否保留，不能以“清除”作为强制结论。
- [ ] 执行 `cd backend && make test-unit`、`make test-integration`（隔离环境）、`go vet ./...`、`go build ./...`、`golangci-lint run ./...`；在 frontend 运行 `pnpm run test:run`、`pnpm run typecheck`、`pnpm run lint:check`、`pnpm run build`。失败先归因，不把前端 lint 通过当成全栈通过。
- [ ] 编写上线前清单（见下）；在隔离 Nginx 中以假 token 验证 access/error/轮转日志脱敏，GET/405/404/429/503 均不泄漏。无生产权限时生产项明确待验。
- [ ] 独立复核最终 diff：不改模型网关、计费、代理分配规则；新增迁移兼容回滚；检查敏感信息与无关文件。设计、计划、操作文档因 docs/* 被忽略，精确 `git add -f`，不放开全部 docs。
- [ ] 创建待审 PR，正文说明功能、权限改变、配置开关默认关闭、测试证据和生产待验项；不自动合并、不发布、不启用。待用户安排 Claude 最终复核。

## 上线前检查清单（未来部署使用，不在本轮执行）

- [ ] api.aoxtoken.com 权威和实际客户端递归解析的 A/AAAA（含 CNAME 链）与批准源站地址一致；不存在遗留 AAAA/CDN 路由。
- [ ] 取 Cloudflare 当时的官方 IP 段（https://www.cloudflare.com/ips/），记录核验时间；所有解析地址都不在其 IPv4/IPv6 段内。
- [ ] 控制台/API 核验该 DNS 记录“仅 DNS”/proxied=false；保存不含账户凭据的证据。无法核验则不开启。
- [ ] Nginx 的 api vhost 保留/校验 Host；下载无 token query 日志，access/error 与轮转、应用审计及实际接入采集器都检查；只用假标记。aoxtoken.com 上同路径拒绝且不跳转，理解 Cloudflare 可记录误发的请求。
- [ ] 启用配置为 HTTPS 专用 public_base_url，开关默认仍关闭；验证 CORS 实际配置下非 OPTIONS 下载响应无 ACAO，OPTIONS 204/403无配置正文。
- [ ] 迁移、备份/恢复与回退窗口获得相应授权；测试矩阵通过、生产日志前置检查通过后才安排开启。DNS/CDN改动后重新核验；变成代理时先关订阅功能并处理潜在 token 暴露，不能仅靠 Host 校验。
- [ ] 关闭开关/回滚代码会停止未来下载；保留表和触发器不恢复旧链接；上游凭据轮换是独立操作，需单独安排。

## 当前交付状态

2026-10-20：任务 1–5 已实现，任务 6 的自动测试与隔离 Nginx/Mihomo 验收完成；待审 PR 保持默认关闭。下面是实际验证摘要，历史步骤复选框不作为已执行凭证。

- 后端完整 race 单测、完整 integration、vet/build、golangci-lint（0 issues）通过。
- 前端 385 文件、3052 测试以及 typecheck/lint/build 通过。
- 新增并发测试观察 PostgreSQL 锁等待；移除 users 锁的变异测试失败，恢复后通过。
- 独立复核修正管理接口 CORS、管理员合规校验、panic 日志脱敏，均有回归测试；设计要求的无凭据跳过原因和快照边界测试已补齐。
- Mihomo v1.19.32 非空/空配置及假 SOCKS 远端 DNS 通过；隔离 Nginx 200/404/405/429/503 及轮转日志检查通过。
- Clash Verge 2.5.8 已验证导入、空节点更新（保存为 REJECT 配置）、404 更新失败；测试前已有内核不可用问题，实际选择/应用配置仍待验。测试订阅已删除，原订阅保留。
- 实现裁定：第一版拒绝 public_base_url 路径前缀，避免未经验证的反向代理重写假设；生产使用的根地址不受影响。
- 生产 DNS/日志前置检查仍未执行，启用需另行授权。
