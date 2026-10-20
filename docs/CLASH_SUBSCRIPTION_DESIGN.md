# Clash 订阅：已评审需求与设计

日期：2026-10-20。评审代码基线：main 合并提交 `5cb965d3b`；第 3 节所列文件与初稿核对的 `ae16375ce` 无差异。当前本机 main 引用仍为 `7b6e54ead`，不据此声称本机已同步 main；本轮未切换或更新分支。
状态：设计和计划已通过 Claude 复核，用户已授权隔离实现与待审 PR；本文件不授权部署或生产启用。

修订依据：Claude 的 `codex-prompt.md`（2026-10-20 12:44 +08 用户决定）。本版落实 SOCKS5h、自动撤销、专用下载域名、配置文件开关、零节点限制；两轮设计复核已通过，实现修正见计划与 PR。

## 1. 已确认需求与权限调整

在 IP 管理页增加 Clash 订阅入口，让 Clash Verge / Mihomo 导入并定期更新代理配置。管理员可以使用；供应商也可以下载管理员分配给自己的代理。

用户明确接受以下风险：

- 配置包含实际代理地址、端口、用户名、密码；持有者可以复制配置或脱离 AoxToken 使用。
- 订阅链接是 bearer credential，泄漏链接等于泄漏订阅范围内代理的访问能力。
- 收回代理、重置链接或停用供应商只能阻止之后的下载，不能撤回已下载的凭据。立即阻断旧配置必须由管理员另行轮换上游凭据或停用上游代理。

这一授权只扩展“导出已分配代理凭据”能力。供应商仍不能修改、删除、转分配代理，不能访问其他供应商的代理。现有代理列表和详情继续脱敏，凭据只在授权订阅响应中导出。

## 2. 第一版范围与设计默认值

下文注明“用户决定”的项目已于 2026-10-20 12:44 +08 确认；其余为设计默认值，Claude 可提出修改：

- 每个用户最多一个订阅，一条链接动态覆盖其范围内全部有效代理。
- 供应商范围：仅 `assigned_supplier_user_id = 当前供应商 ID`，不混入供应商自建代理。
- 管理员范围：管理员公共池，即 `supplier_user_id IS NULL AND assigned_supplier_user_id IS NULL`。已分配代理不混入管理员订阅；如需管理员选取任意代理，另扩展，不隐式扩大范围。
- 普通用户无订阅管理权限。管理员第一版只管理自己的订阅；供应商链接由供应商自己创建。
- 支持 HTTP、HTTPS、SOCKS5、SOCKS5h（用户决定：SOCKS5h 导出为 Mihomo socks5，域名在代理侧解析，需客户端实测）；不添加订阅流量统计、带宽配额、收费、节点测速、自动故障转移、规则订阅、SS/VMess 等新协议。
- 提供完整 Mihomo YAML 配置，直接作为 Clash Verge 远程订阅导入；不是只含 `proxies` 的 provider 文件。
- 默认有效期 90 天，可选 30 / 90 / 365 天；第一版不支持永久链接。
- 有效期内保持同一链接；代理分配变化无需重置链接。过期后重置会生成新链接，客户端需要重新导入。

## 3. 现有代码约束

| 现有入口 | 设计约束 |
|---|---|
| `backend/internal/server/routes/supplier.go` | 当前供应商操作使用 JWT、SupplierOnly、面板限流和审计中间件 |
| `backend/internal/handler/supplier_handler.go` | 保持现有代理 CRUD/test 的鉴权边界 |
| `backend/internal/service/supplier_service.go` | 列表包含供应商自建与分配代理；订阅不能直接复用整个可见列表 |
| `backend/internal/service/supplier_oauth_service.go` | 已有分配代理与供应商自建代理来源区分；不改账号绑定规则 |
| `backend/internal/repository/proxy_repo.go` | 使用专用查询读取允许导出的代理，不通过脱敏 DTO 获取密码 |
| `backend/migrations/246_proxy_supplier_assignment.sql` | 保留单一归属、备用代理和绑定约束，不修改旧迁移 |
| `frontend/src/views/{admin,supplier}/ProxiesView.vue` | 增加页面级按钮，保持供应商行级只读行为 |

不改网关账号调度、模型计费、诊断、代理过期切换逻辑；尤其不向每个模型推理请求添加数据库校验。订阅下载是独立低频路径。

## 4. 方案选择

| 方案 | 结论 |
|---|---|
| URL 带账号密码 / HTTP Basic | 不采用：暴露登录凭据，难以独立撤销 |
| 复用模型 API key | 不采用：权限与生命周期混在一起 |
| 登录 JWT 直接订阅 | 不采用：客户端长期更新和会话过期冲突 |
| 独立随机订阅 token | 采用：可撤销、可到期、仅能下载指定主体的代理 |

每次下载都检查数据库的当前用户状态、角色、订阅状态及代理归属；不信任 token 内自报的用户 ID，不用 URL 参数控制供应商 ID 或代理集合。

## 5. 订阅数据与生命周期

新增 `proxy_subscriptions` 表及用户变更撤销触发器。评审基线最新迁移是 246，新增编号从 247 起；实现前再检查编号冲突，不修改 246 或其他旧迁移：

| 字段 | 含义 |
|---|---|
| id | 内部主键 |
| user_id | 用户外键，唯一；一人一条生命周期记录 |
| owner_role | 创建时角色 admin/supplier，拉取时必须与当前角色一致 |
| token_hash | SHA-256 哈希，唯一索引；不保存原始 token |
| version | 乐观锁版本，用于创建/重置/撤销并发冲突检测 |
| expires_at | UTC 到期时间，必填 |
| revoked_at | 撤销时间，可空 |
| created_at / updated_at | 元数据 |

token 使用 CSPRNG 生成至少 32 字节，base64url 无填充编码。仅在首次创建/重置成功响应中返回原始链接；刷新页面后不可恢复明文，需重置才能再次复制。不得把原始 token 写入 localStorage、审计日志或数据库。

只要用户已有订阅记录，创建均返回 409，不偷偷轮换或新插入。撤销、过期、角色变化后的恢复统一使用 reset：原子替换 hash、到期时间，清空 revoked_at，按当前已鉴权角色设置 owner_role，并增加 version；撤销清除可用性并增加 version。修改带 expected_version；两个并发操作只有一个成功，另一个返回 409，避免用户复制到已失效链接。

用户决定：角色发生变化、status 从 active 变成非 active（现有值为 active/disabled）、首次软删除时，自动撤销其订阅，写入 revoked_at、version+1。重新启用、恢复用户或改回原角色都不恢复旧链接，必须在用户当前可用且角色受支持时 reset。角色变化不能把旧供应商订阅升级为管理员订阅。

采用新迁移中的 users 表 `AFTER UPDATE OF role, status, deleted_at` 行级触发器覆盖单个编辑、批量编辑和直接数据库更新：使用 OLD/NEW 的 `IS DISTINCT FROM` 判断真实变化，只在角色改变、离开 active 或 deleted_at 从 NULL 变为非 NULL 时撤销；无变化更新不增加版本。自动撤销与用户变更处于同一事务，无订阅则无操作；同一次更新同时满足多个条件也只撤销一次。已有 revoked_at 保留首次撤销时间，但每次有效撤销事件仍增加 version。

创建/reset 必须在事务内先锁定并重读 users 行，再修改订阅；触发器按相同 users → subscription 顺序更新，避免与禁用/改角色竞争后生成可复活链接。初次并发创建命中 user_id 唯一约束时映射为 409，不返回 500。下载仍检查当前用户状态、角色、软删除及 revoked_at，作为第二道防线。物理删除使用订阅外键 ON DELETE CASCADE，不阻塞用户删除；软删除由触发器撤销。

## 6. API 与鉴权

管理端与供应商端使用各自现有登录鉴权链；路径建议：

2026-10-20 14:08 决定：管理员和供应商的创建、重置 POST 均接现有 step-up 门控，由 `step_up_enabled` 控制，默认关闭。GET 元数据、DELETE 撤销不加。供应商沿用 `/profile` 的 TOTP 设置入口及 `/user/totp/*` 允许名单。前端验证后只提示手动重试，不自动生成或重置链接。

| 方法与路径 | 作用 |
|---|---|
| GET `/api/v1/{admin,supplier}/clash-subscription` | 元数据、是否有效、expires_at、version、可导出/跳过数量；不返回 token 或密码 |
| POST 同路径 | 首次创建，接收 duration_days；只返回一次 subscription_url |
| POST `…/reset` | expected_version、duration_days；旧链接失效，返回新链接 |
| DELETE 同路径 | expected_version；撤销 |
| GET `/api/v1/clash/subscription?token=…` | 无登录 cookie 要求，使用专用 token 鉴权后返回 YAML |

公开下载接口不是匿名开放资源。只允许一个 token 参数，拒绝重复参数、超长或格式错误；不接受 supplier_id、proxy_ids、格式模板等扩权参数。

下载判定：无效、过期、撤销 token，用户不可用或角色不匹配统一 404；限流 429；内部故障 503，绝不能退回无鉴权或缓存的旧配置。仅 GET 支持下载；不对 HEAD 返回配置，其他方法 405。仓库未开启 HandleMethodNotAllowed，需为此固定路径显式注册方法拒绝处理并测试 HEAD/POST/PUT/DELETE/PATCH 等，不改变全站方法行为。错误 Host 优先统一 404（包括非 GET 方法，但 OPTIONS 例外）。OPTIONS 保留全局 CORS 中间件的 204/403，不返回配置正文；其余下载响应在写出前删除跨域允许头。管理接口按既有 401/403 约定。

成功与错误响应均 `Cache-Control: private, no-store`、`Referrer-Policy: no-referrer`；成功设置 YAML Content-Type、固定无用户信息的下载文件名及 `X-Content-Type-Options: nosniff`。不返回 304，不使用 CDN、Redis 或服务端响应体缓存。跨域读取不开放 wildcard CORS。

用户决定：新增配置文件项 `clash_subscription.enabled`（默认 false）、`clash_subscription.public_base_url`。生产地址固定为 `https://api.aoxtoken.com`，该域名无 CDN；不复用指向 Cloudflare 前端域名的 `server.frontend_url`，不提供后台开关。

enabled=true 时 public_base_url 必须为有效 HTTPS 绝对地址，否则启动校验失败；禁止 userinfo、query、fragment。第一版只接受根地址，不支持路径前缀；未来支持时须同时测试 URL 拼接和反向代理路由，不从请求猜测。链接生成不使用未经验证的 Host/X-Forwarded-Host。

下载仅接受请求 Host 与配置主机一致的请求，其余统一 404；主机比较规范化大小写、HTTPS 默认端口，不接受后缀匹配或伪造 X-Forwarded-Host。Nginx 须保留并校验原始 Host，不能把任意来源 Host 重写为允许主机。aoxtoken.com 上该路径必须拒绝，不能跳转到 api 域名。

## 7. 实时范围与竞态边界

数据库查询同时限定：token有效、`users.deleted_at IS NULL`、`users.status='active'`、`users.role=owner_role`、`proxies.deleted_at IS NULL`、`proxies.status='active'`、`proxies.expires_at IS NULL OR proxies.expires_at > 当前数据库时间`、归属符合第 2 节。原生 SQL 不会自动应用 SoftDeleteMixin，以上条件必须显式写入且逐项测试。使用同一 SQL 语句快照或一致性只读事务，避免先取 ID 后无条件读取凭据。

撤销/收回提交后的新下载必须不再导出；已在旧快照上开始的请求可能仍完成一次返回。这与无法追回已下载凭据属于同一边界，文案必须说明。第一版不为整个 YAML 网络响应持有数据库行锁。

过期不等于网络不可达；按现有记录中的到期字段判断，不解析代理用户名里的 session/sesstime 作为权威到期时间，不主动检测端点，不自动跟随 backup_proxy_id。

用户决定：可导出节点为 0（包含全部被格式校验跳过）时，前端生成/重置/复制链接按钮禁用，并显示 0 个节点；后端创建/reset 使用同一过滤规则检查，返回 409 `CLASH_NO_EXPORTABLE_PROXIES`，不得修改 token、version 或原有订阅。不能只靠前端校验。复制前刷新元数据；其后并发收回无法靠客户端彻底消除，下载时仍以实时快照为准。

已有有效链接在节点降为 0 时，下载返回 200 的可解析“拒绝出网”配置：`proxies: []`，选择组只包含 `REJECT`，最终规则指向该组。不能返回 DIRECT。管理页显示 0 个节点，撤销仍可操作。客户端未刷新或拒绝更新时可能保留旧配置，不承诺强制清除；Clash Verge 对旧节点的保留/替换行为只记录实测结果，不作为清除旧节点的强制通过项。

## 8. 配置生成

使用成熟 YAML 序列化器从结构体生成，禁止拼接用户可控 YAML。字段显式白名单；代理名称加稳定 ID 后缀以去重，例如 `US-OH.Euclid-ATT [p10]`，限制名称长度、处理换行/控制字符，并在处理后校验最终名称唯一。名称不得自动包含 host、username、password 或供应商邮箱。

| 库内协议 | Mihomo 输出 |
|---|---|
| http | `type: http`，server、port、可选 username/password |
| https | `type: http`，`tls: true`；证书校验开启，不自动 skip-cert-verify |
| socks5 | `type: socks5`，默认 `udp: false`（现有系统无已验证 UDP 能力） |
| socks5h | `type: socks5`，默认 `udp: false`；由代理侧解析目标域名，验收实测确认，不输出不存在的 socks5h 类型 |

server 保持域名/IP 原值，IPv6 使用 YAML 字符串而非拼接 URL。HTTP/HTTPS 的空用户名、非空密码保持原样；SOCKS5/SOCKS5h 的同类组合不符合 RFC 1929 的非空用户名要求，跳过并返回不含凭据的原因 `SOCKS_AUTH_USERNAME_REQUIRED`，不得自动补用户名。端口必须 1–65535。无效/不支持节点跳过，管理页只显示数量与不含凭据的原因；若全部跳过，已有订阅返回上述拒绝配置，创建/reset 拒绝。

完整配置：`mode: rule`、`allow-lan: false`、`mixed-port: 7890`；一个 `type: select` 的 `AoxToken` 组，成员为导出节点（无节点则 REJECT）；`rules: ["MATCH,AoxToken"]` 以合法 YAML 列表序列化。不附加 DIRECT 自动回退，不启用 TUN/DNS 劫持、不开放 external-controller、不加入远程规则和第三方测速 URL。界面提示该配置默认让匹配流量走所选代理；客户端自己的覆写设置仍由用户控制。

不擅自强制客户端自动刷新；导入说明建议 60 分钟更新一次，也可手动更新。可选返回 `profile-update-interval: 1` 响应头，客户端是否识别以固定版本 Clash Verge 实测为准，不作为准时更新保证。节点选择是否保留由 Clash Verge 行为决定，要实测，不承诺。

参考：Mihomo 官方 [HTTP/HTTPS](https://wiki.metacubex.one/config/proxies/http/) 与 [SOCKS5](https://wiki.metacubex.one/config/proxies/socks/) 配置文档，核对日期 2026-10-20。

## 9. 日志、限流与部署前置条件

token 放 query 是为固定路径日志脱敏提供便利，不代表天然安全。Nginx 的 `$request`/`$request_uri`、CDN、WAF、APM、错误追踪都可能记录完整链接。

生产订阅链接只走无 CDN 的 api.aoxtoken.com。上线日志前置检查范围为该域名 Nginx vhost（access/error 及轮转文件）、应用日志和审计；若这些路径附带 APM/错误采集，也要检查其实际输出。下载路由仅记录不带 query 的固定路径、状态码、耗时；应用 request logger 不打印 query/header/body；管理创建/重置接口也不记录响应体。使用随机测试标记扫描，不拿真实 token 做证据；不能证明脱敏则保持功能关闭。

Cloudflare 不在生成链接的下载路径上，不要求改动其日志策略，但必须用假 token 验证 aoxtoken.com 上该路径被拒绝且不重定向。Host 拒绝只能阻止源站返回配置，不能抹去请求到达 Cloudflare 前已产生的边缘日志；因此不宣称用户手工把真实 token 发到错误域名也能避免泄漏。UI 仅生成/复制专用 api 域名链接。

审计仅记录创建/重置/撤销的 actor、subscription_id、时间、结果；下载可记录内部 subscription_id、节点数、状态和耗时。不得记录 token/hash、完整 URL、导出正文、代理密码。下载最近时间第一版不增加高频数据库写入。

建议初始限流：入口 IP 60 次/分钟；有效订阅 10 次/分钟。IP 限流在数据库查询前，按 token 的限流键使用内部订阅 ID，不把随机 token 直接当 Redis key。多实例共享计数；限流依赖不可用则 503，不失效开放。配合连接/响应超时，防止猜测请求耗尽 DB。数值为可配置设计默认值，不做 IP 绑定。

`middleware/panel_rate_limit.go` 的面板限流在 Redis 异常时 fail-open，不能直接复用 `limiter.Global()` 作为下载路由保护。下载使用独立 fail-closed 限流，明确测试 Redis 故障返回 503 且不读取/导出代理。登录管理接口仍可保留原有面板限流行为。

第 6 节配置文件开关默认关闭；管理入口与下载均受控，关闭时不返回配置。开启前须证明 api 域名解析地址与批准源站一致、所有地址不在 Cloudflare 官方 IPv4/IPv6 段、DNS 控制台记录为仅 DNS/proxied=false，缺一则不开启；DNS/CDN 变更后重新核验。另须完成 Host 拒绝和日志审核。不是本轮修改生产 Nginx、Cloudflare 或部署的授权。

## 10. UI 流程

管理员/供应商代理页工具栏“Clash 订阅”打开弹窗：

1. 未创建：解释范围、凭据可见和不可撤回边界，选择有效期，有可导出节点才允许生成。
2. 创建成功：只在本次弹窗展示链接；复制前刷新节点数，零节点时禁止复制。提示关闭后不再显示，请保存或重新生成。
3. 已创建：显示状态、到期时间、节点数；有节点才允许重置，零节点也允许撤销。无“找回旧链接”。用户恢复 active 后显示旧订阅已撤销，不能提供旧链接恢复入口。
4. 重置/撤销确认只影响订阅下载，不修改代理；提示客户端需重新导入，以及已下载配置不会立刻失效。
5. 提供手动导入步骤：Clash Verge → 订阅 → 新建远程订阅 → 粘贴链接 → 更新/选择配置。

第一版不做一键协议唤起/二维码，避免增加 URL 泄漏面及客户端兼容范围；普通用户不显示入口，接口仍独立鉴权。

## 11. 验收与 Claude 复核重点

| 类别 | 必须通过的检查 |
|---|---|
| 权限 | 供应商 A 只能下载分配给 A 的节点；B、自建代理、管理员池不混入；普通用户及模型 API key 不能管理订阅 |
| 原功能 | 分配代理仍不能改/删；列表详情继续脱敏；账号绑定、调度、过期切换与现有测试不退化 |
| token | 创建只返回一次；DB 不存明文；撤销/到期/禁用/删用户/角色变化后新下载拒绝；恢复 active/原角色后旧链接仍失败；并发 reset/delete 的 version 冲突、首次创建唯一冲突 409 可复现 |
| 自动撤销 | 单个/批量/原生 SQL 用户更新均触发；无变化不增加 version，多条件一次只撤销一次；create/reset 与用户变更竞争不会生成恢复后可用的旧链接 |
| SQL | 逐项验证 users.deleted_at/status/role 与 proxies.deleted_at/status/expires_at，不能依赖原生 SQL 的隐式软删除过滤 |
| 归属 | 分配/收回/转分配后下次拉取立即反映；快照边界有并发测试，不承诺回收已发出响应 |
| YAML | HTTP、HTTPS、SOCKS5/SOCKS5h；代理侧解析实测；HTTP 空用户名保留，SOCKS 空用户名非空密码跳过；域名/IPv4/IPv6、特殊密码、重复名字、Unicode、控制字符不注入；Mihomo `-t` 校验 |
| 空订阅 | 零节点禁用前端生成/重置/复制，后端创建/reset 拒绝且不变更旧记录；已有链接返回有效 REJECT 配置；Clash Verge 对旧节点清除/保留仅记录实测行为 |
| TLS | HTTPS 不跳过证书；证书失败保留失败结果，不能自动改 HTTP |
| 日志/域名 | api.aoxtoken.com 的 Nginx vhost 含轮转、应用、审计及接入的采集器没有测试 token/密码；aoxtoken.com 路径拒绝且不跳转；错误 Host/X-Forwarded-Host 不绕过；缓存不得存配置 |
| 配置/方法 | enabled 默认 false；true 加无效/non-HTTPS public_base_url 启动失败；不复用 frontend_url；显式方法处理覆盖 HEAD 和其他非 GET 的 405，错误 Host 统一 404 |
| 错误 | 404 不泄漏主体信息；429/503 不返回旧配置；依赖故障 fail-closed |
| 客户端 | 固定测试版本的 Clash Verge/Mihomo，记录版本；导入、更新、节点选择、重置旧链接失败；只用隔离假代理/专用测试凭据 |
| 迁移 | 247 起新增表/索引/约束/撤销触发器；旧版本回滚可保留表和触发器，users 更新不依赖新应用代码；代码回滚或关开关后停止下载，已导出凭据仍有效；未来删除表时先删除依赖触发器 |

需要 Claude 明确给出：通过 / 修改后通过 / 阻塞；特别审核管理员默认范围、token 明文不可恢复的 UX、空订阅行为、日志脱敏的生产可实施性、数据库快照边界及是否遗漏其他凭据泄漏路径。

## 12. 通过评审后的工作顺序

评审通过后才编写具体实现计划并开始开发：新分支或隔离 worktree → 数据层及权限回归 → token API 与 YAML 输出 → 管理/供应商 UI → 日志与限流集成 → 隔离客户端验收 → PR。首次 PR 不自动合并、发布或启用生产。上线另行审核日志路径及窗口。

仓库 `.gitignore` 忽略 `docs/*`；文档随 PR 提交时使用 `git add -f` 精确加入本设计及复核提示词，不批量加入私有运维文件，也不为此放开整个 docs 目录。本轮仍仅修订文档。
