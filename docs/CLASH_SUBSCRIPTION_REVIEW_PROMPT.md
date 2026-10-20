# 给 Claude 的实现复核提示词

请复核 feat/clash-subscription 分支相对 main 5cb965d3b90346f025103e469f39f97c544fc06b 的全部改动。
隔离 worktree：/Users/dalu/aiworkspace/codex/runtime/sub2api-clash-subscription。
对照 docs/CLASH_SUBSCRIPTION_DESIGN.md 和 docs/superpowers/plans/2026-10-20-clash-subscription.md，按六项任务复核。请只读评审，不合并、不发布、不启用生产。

已接受的边界：配置含代理凭据、链接可转发、下载的密码不能收回；供应商仅导出被分配的有效代理，管理员仅公共池。原列表继续脱敏、供应商不能修改或删除分配代理。

请重点核对：
1. 迁移 247、唯一约束、用户触发器和 users→subscription 锁序。并发测试会观察 pg_blocking_pids；移除用户锁的变异测试确实失败。
2. 显式 SQL 过滤用户/代理软删除、状态、到期、角色、两种归属；repeatable-read 快照；有效零节点与无效 token 的区别。零节点 create/reset 409 且不改变旧记录。
3. token 32 字节 CSPRNG，仅保存 hash，链接仅一次返回；version 冲突，不复用 JWT 或模型 API key。管理接口采用 JWT 和现有角色/合规校验。
4. 下载 CORS 清理仅用于下载，不影响管理接口；OPTIONS 保持全局 204/403。错误 Host、方法、重复 query、路径编码/斜杠不泄漏 token 或重定向；Redis 故障 503。
5. 外层 panic recovery 对该功能省略 request dump/panic payload；正常日志不含 query/正文。审计不保存 grant 响应。
6. YAML 特殊字符、稳定 ID 名称、HTTPS 校验、socks5h→socks5 远端 DNS、非法 SOCKS 认证原因码、空节点 REJECT；无 DIRECT 回退。
7. 弹窗关闭后清除链接，晚返回丢弃；操作/复制前刷新节点数；冲突或响应丢失不自动 reset；普通用户没有入口。
8. 未改变网关调度、计费、代理分配或旧迁移。

本地验证：
- 后端完整 -race -tags=unit、完整 -tags=integration、vet、build、golangci-lint 0 issues。
- 前端 385 文件 / 3052 测试、typecheck、lint:check、build。
- Mihomo v1.19.32 对真实生成的非空/空 YAML 校验，通过假 SOCKS 上游确认域名 ATYP。
- 隔离 Nginx 1.28-alpine：200/404/405/429/503（真实连接拒绝），access/error/轮转无假 token。
- Clash Verge 2.5.8：导入通过，空节点更新保存为 REJECT，404 更新失败。测试前桌面内核已不可用，所以实际选择/应用、运行内核保留节点仍待验。测试订阅已删除，原订阅保留。

独立预审的管理 CORS、合规 guard、panic 泄漏和弱并发屏障均已修正并加回归；无凭据跳过原因和快照边界也已补齐。
实现裁定：v1 不支持 public_base_url 路径前缀，配置拒绝；生产根域名地址不受影响。

证据：worktree/.superpowers/sdd/2026-10-20-clash-subscription/，包括 progress.md、*reviewed.log、client-result.md、nginx-result.txt、变异测试 RED 日志和客户端截图。
生产 DNS/日志检查未执行，功能默认 false，生产启用另行授权。请按阻塞/建议/可接受边界给结论，并明确实际运行和仅阅读的检查。
