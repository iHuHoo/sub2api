# 给 Claude 的实现复核提示词

请复核 feat/clash-subscription 分支相对 main 5cb965d3b90346f025103e469f39f97c544fc06b 的全部改动。
隔离 worktree：/Users/dalu/aiworkspace/codex/runtime/sub2api-clash-subscription。
对照 docs/CLASH_SUBSCRIPTION_DESIGN.md 和 docs/superpowers/plans/2026-10-20-clash-subscription.md，按六项任务复核。请只读评审，不合并、不发布、不启用生产。

已接受的边界：配置含代理凭据、链接可转发、下载的密码不能收回；供应商仅导出被分配的有效代理，管理员仅公共池。原列表继续脱敏、供应商不能修改或删除分配代理。

## PR #73 第二轮复核

上轮 review：pullrequestreview-5581350937，修订基点 `1df6be116`。用户 2026-10-20 14:08 已决定供应商也需 step-up。请优先复核本轮差异：

1. 两种角色 POST 创建/重置均使用真实 `stepUpAuth`，GET/DELETE 不加。路由测试分别覆盖开关默认关闭、开启无 grant（403、不生成 token、不改 version/hash）、开启有 session grant（200），而不是只测一个模拟门控。
2. `SupplierSelfServiceGuard` 原本已允许 `/user/totp/*`；本轮补 GET status/verification-method、POST send-code/setup/enable/disable/step-up 和两种 backend mode 的回归。供应商侧栏已有 `/profile`，该路由允许供应商，页面无角色限制地呈现 `ProfileTotpCard`，所以无需新增设置页面。
3. 前端复用 `useStepUp.prompt()` / `TotpStepUpDialog`，刻意不使用会自动重试的 `run()`。验证后只提示手动重试；关闭弹窗取消 pending prompt；未设置 TOTP 提示到个人资料设置。两种角色创建/重置均有 Vitest 覆盖。
4. `TestClashSubscriptionDownloadOwnerDefense` 临时禁用 `users_revoke_proxy_subscription` 并 cleanup 恢复，确认 version=1 且 revoked_at=NULL，然后独立验证 disabled、deleted、supplier→admin/user 下载均 NotFound。分别删掉 `u.deleted_at IS NULL`、`u.status='active'`、`u.role=s.owner_role` 已实际变异，三次均得到“预期 NotFound、实际 nil”的失败（含不同的受支持角色，避免仅 IN 条件掩盖 owner_role 比较）。
5. 元数据使用只读 REPEATABLE READ 快照，无 FOR UPDATE；创建/重置/撤销的用户行锁保留。新增集成测试在另一个事务持有余额写锁时调用 GetMetadata，仍能返回。
6. 运维文档写明 HTTP/HTTPS 空用户名加密码在 Mihomo 的认证限制；保留停用再启用供应商撤销泄漏链接的办法。
7. 本轮启动了 `go build ./cmd/server` 的真实 Linux 二进制（完整 Wire 与路由），在无发布端口的 internal 网络内连接全新 PostgreSQL/Redis，下载 200/404/405 均通过、启动无 panic。隔离阻断版本同步和缺少价格资源造成的启动 warning 已记录，不当成生产故障或零告警声明。

本轮证据使用 `review2-*` 文件；此前的 `*reviewed.log` 为上一轮记录。特别查看 `review2-stepup-red.log` / `review2-stepup-green.log`、`review2-mutation-*-red.log`、`review2-integration-restored.log`、`review2-ui.log`、`review2-binary-result.json`、`reply.md` 中本轮验证结果。测试链接和数据均为隔离假数据，不使用生产凭据。

请重点核对：
1. 迁移 247、唯一约束、用户触发器和 users→subscription 锁序。并发测试会观察 pg_blocking_pids；移除用户锁的变异测试确实失败。
2. 显式 SQL 过滤用户/代理软删除、状态、到期、角色、两种归属；repeatable-read 快照；有效零节点与无效 token 的区别。零节点 create/reset 409 且不改变旧记录。
3. token 32 字节 CSPRNG，仅保存 hash，链接仅一次返回；version 冲突，不复用 JWT 或模型 API key。管理接口采用 JWT 和现有角色/合规校验。
4. 下载 CORS 清理仅用于下载，不影响管理接口；OPTIONS 保持全局 204/403。错误 Host、方法、重复 query、路径编码/斜杠不泄漏 token 或重定向；Redis 故障 503。
5. 外层 panic recovery 对该功能省略 request dump/panic payload；正常日志不含 query/正文。审计不保存 grant 响应。
6. YAML 特殊字符、稳定 ID 名称、HTTPS 校验、socks5h→socks5 远端 DNS、非法 SOCKS 认证原因码、空节点 REJECT；无 DIRECT 回退。
7. 弹窗关闭后清除链接，晚返回丢弃；操作/复制前刷新节点数；冲突或响应丢失不自动 reset；普通用户没有入口。
8. 未改变网关调度、计费、代理分配或旧迁移。

上一轮完整实现验证（本轮增量结果以 `review2-*` 和 `reply.md` 为准）：
- 后端完整 -race -tags=unit、完整 -tags=integration、vet、build、golangci-lint 0 issues。
- 前端 385 文件 / 3052 测试、typecheck、lint:check、build。
- Mihomo v1.19.32 对真实生成的非空/空 YAML 校验，通过假 SOCKS 上游确认域名 ATYP。
- 隔离 Nginx 1.28-alpine：200/404/405/429/503（真实连接拒绝），access/error/轮转无假 token。
- Clash Verge 2.5.8：导入通过，空节点更新保存为 REJECT，404 更新失败。测试前桌面内核已不可用，所以实际选择/应用、运行内核保留节点仍待验。测试订阅已删除，原订阅保留。

独立预审的管理 CORS、合规 guard、panic 泄漏和弱并发屏障均已修正并加回归；无凭据跳过原因和快照边界也已补齐。
实现裁定：v1 不支持 public_base_url 路径前缀，配置拒绝；生产根域名地址不受影响。

证据：worktree/.superpowers/sdd/2026-10-20-clash-subscription/，包括 progress.md、*reviewed.log、client-result.md、nginx-result.txt、变异测试 RED 日志和客户端截图。
生产 DNS/日志检查未执行，功能默认 false，生产启用另行授权。请按阻塞/建议/可接受边界给结论，并明确实际运行和仅阅读的检查。
