# Aox .24 部署与回退方案

状态：**待 review，未授权生产执行**。编制：2026-10-15（+08）。镜像已发布，不代表已上线。

> 执行时使用 executing-plans 按关口逐项记录证据。本文合并只批准方案；现场准备、入口变更、停服迁移、真实付费验收、回退与清理均以当次明确授权为准。此前阶段 D 的授权不延伸到本次升级。

**目标：** 在阶段 D 的 24 小时复查通过后，从 .23 升级到 .24，保持 subproxy 诊断启用，将应用配置整合为单一基础 Compose，并提供可核验的迁移及缓存回退路径。

**架构：** 只重建 sub2api；PostgreSQL、Redis、subproxy 保持原容器与配置。首次上线建议暂不开放 Gemini 显式缓存管理接口，入口限制单独审批。迁移由 .24 启动执行，保留新增 schema 回退旧二进制，不自动降级 schema 或覆盖数据库。

**依据：** [生产记录](PRODUCTION_DEPLOYMENT.md)、[上游复核](upstream/v0.2.16-aox-review.md)、[原联合发布方案](SUBPROXY_RELEASE_PLAN.md)。技术栈：Docker Compose、PostgreSQL 18、Redis、Nginx、Go 服务。

## 1. 冻结版本与授权关口

| 项目 | 当前 / 目标 |
|---|---|
| 当前生产 | .23，`41d5706d34ad87057129e5596e8a43ab4eda3163`；诊断已启用，执行前重新核实 |
| 回退镜像 | `ghcr.io/ihuhoo/sub2api@sha256:bd969c74944c4b0d061de580af0f1f5e5bfd3fc9496412f137ab7875daa47d92` |
| 目标 tag | `v0.2.16-aox.0.0.24` |
| 目标提交 | `2d334453f0082caf09d5fedf1fa6c3291ebdbe56` |
| 目标镜像 | `ghcr.io/ihuhoo/sub2api@sha256:13f13ed198a6d67e92e917fe308c6d33bf1b021e666324366d28ae32774d4672`，linux/amd64 |
| 发布证据 | [Aox run 38682837539](https://github.com/iHuHoo/sub2api/actions/runs/38682837539)；本地 `runtime/sub2api-release-0.0.24-20261015/`（位于 `/Users/dalu/aiworkspace/codex/`） |
| 目标 main CI | [run 38682638474](https://github.com/iHuHoo/sub2api/actions/runs/38682638474)，2026-10-15 已完成并成功；安全扫描也已通过，执行前复核冻结提交与结果 |
| 生产位置 | `aoxtoken@20.195.40.145:/home/aoxtoken/sub2api`，项目 `sub2api` |

- [ ] **G0：** 2026-10-16 02:06 +08 之后完成阶段 D 的 24 小时复查，Claude 复核通过：诊断缓存、错误分类、网关错误/延迟、资源与重启。未通过则不部署 .24；原备份和 .22 镜像继续保留。
- [ ] **G1：** 本方案 review 通过；最终 main CI 成功；隔离预演通过并补齐以下证据。不能以“文件树与 PR 一致”替代这道执行关口。
- [ ] **G2：** 用户授权现场准备和明确的 Nginx 缓存入口限制范围，批准外部验收使用的 key、模型、费用上限；准备动作本身不授权停服。
- [ ] **G3：** 用户明确停服窗口、最长中断、观察时长和回退执行权限。建议先按数据库实测大小估算，不能沿用上次 90 秒；建议 15 分钟停服上限，30 分钟观察，最终值由授权确定。
- [ ] **G4：** 验收和观察通过后更新实际生产记录；显式缓存开放单独申请，首次升级不顺带开放。

执行人、复核人、窗口、回退负责人、最大费用、停止阈值及例外处理均填入执行记录。没有授权/工具/证据的步骤保持未完成，不能推断通过。

## 2. 四个迁移与数据库验证

| 完整文件名 | 变更 | 检查重点 |
|---|---|---|
| `242_group_i18n.sql` | `groups.i18n JSONB NOT NULL DEFAULT '{}'` | 现有分组、名称、定价不丢失，默认值正确 |
| `243_gemini_cached_contents.sql` | 新缓存绑定表及 3 个索引 | API key/分组/账号归属、唯一 public_id |
| `244_gemini_cached_contents_channel_usage.sql` | `channel_usage JSONB NOT NULL DEFAULT '{}'` | 创建与续期沿用渠道计费口径 |
| `245_gemini_cached_contents_mutation_lease.sql` | mutation_token、mutation_lease_until、pending 索引 | 续期异常恢复、租约处理 |

四个文件都是增量 schema，不删除既有数据。数字前缀与其他迁移重复不等于冲突：迁移器按**完整文件名**排序、记录和校验。不要重命名旧迁移、删除 schema_migrations 记录或改校验和。相对 .23 的所有旧迁移应字节一致；现场若还有其他待执行迁移，停止并重新评审。

### 隔离预演（生产窗口前）

- [ ] 使用独立 Compose 项目、独立数据库/Redis/secret、无生产网络、无公网端口；脱敏记录项目、卷、网络、镜像和 Compose 版本。敏感备份仅保存在 0700 目录，文件 0600。
- [ ] 从授权的近期备份恢复，运行 .24 的四个迁移；对比旧表行数、分组数据、关键约束和 schema_migrations 的完整文件名/校验和。新缓存表初始为空；所有索引有效。
- [ ] 保持新 schema，换回冻结 .23 验证启动、管理员、分组/套餐/计费读取及诊断能力，再切回 .24。此步骤验证旧二进制兼容新增 schema，不能只依据“ALTER ADD”猜测兼容。
- [ ] 用合成上游验证缓存创建、续期、删除、上游成功后本地失败的孤儿清理、续期失败的租约恢复、上游 404/403/超时、账号不可用等回退分支。不得调用真实模型或真实缓存存储来代替合成测试，除非另获费用授权。
- [ ] 完成第 3 节入口限制与第 4 节 Compose 等价性验证，记录原始敏感产物的受限路径和脱敏结论。

### 停服后的新备份（每次正式部署都重做）

1. 先记录所有容器 ID/启动时间/重启数，阻止新业务进入并排空现有流式请求及异步计费，再只停 sub2api。确认无其他数据库写入者。超过获批排空时间则停止，不强杀长请求后声称无损。
2. 建 `backups/pre-v0.2.16-aox.0.0.24-<UTC时间>/`（0700），保存**当前有效的两份 Compose**、`.env`、config、Nginx 配置、secret 的受限备份/校验信息、.23 digest、容器身份与迁移清单。此备份必须反映诊断=true；不要复用诊断尚未启用时的旧配置。
3. 用当前 PostgreSQL 18 客户端做 `pg_dump -Fc`，核验非空、`pg_restore --list` 和 SHA-256；另存受限的 roles/grants 恢复资料。默认不把凭据、完整 config、SQL 数据或转储写入普通日志。
4. 恢复到一次性隔离 PostgreSQL 容器/独立卷，无生产网络/无发布端口；检查完整恢复成功、旧表逐表行数与关键对象一致。若用 `--no-owner --no-privileges`，明确只验证 schema/data，角色权限另验，不宣称完整权限恢复。
5. 备份和恢复验证失败或超时：尚未启动 .24 时用原两份 Compose 恢复 .23，重新核验健康；不得跳过备份赶窗口。

## 3. Gemini 显式缓存：首次关闭与回退清理

### 代码已确认的约束

- `.24` 自动装配缓存服务及 `/v1beta/cachedContents` 路由，没有已实现的全局 enable/disable 配置。`gateway.gemini_cached_content_max_ttl_seconds` 是上限，默认 86400；0/负值回落默认值，**不是关闭开关**。
- 关闭第三方账号的 `explicit_cache_upstream` 只影响该账号；官方 AI Studio/Vertex 仍可能支持缓存，且改账号能力会妨碍清理。不得通过删账号、改 key、取消分组等方式“关闭缓存”。
- 管理 API 的 list/get 只反映当前 API key、当前分组下的本地有效记录，不能盘点全租户、已过期/软删除记录或上游孤儿。
- 创建及延长时预收存储费用；**DELETE 不退已收费用**，业务清理不等于退款。usage 写入是异步任务；需对账完成后才能停服。
- PATCH 使用 2 分钟租约；恢复 worker 每分钟扫描，批次 50。恢复到原有效期或删除过期资源；失败可继续重试，没有“等两分钟必定清理完”的保证。
- API DELETE 在账号不存在/不再支持时可能仅清本地记录；本地 200/404 或 active_count=0 **不能单独证明上游删除**。恢复算法的过期兜底也不能替代实际上游证明，尤其历史 TTL 曾提高时。

依据：[路由](../backend/internal/server/routes/gateway.go)、[handler](../backend/internal/handler/gemini_cached_contents_handler.go)、[service](../backend/internal/service/gemini_cached_content.go)、[repository](../backend/internal/repository/gemini_cached_content_repo.go)。

### 首次上线的默认建议：入口暂封管理接口

在 .24 启动前，经 G2 授权，在**所有实际 API 入口**对规范化路径 `/v1beta/cachedContents` 及其子路径拒绝所有方法，返回明确的 503 维护响应；普通 `/v1beta/models/...`、OpenAI/Claude 路由不受影响。Nginx 规则放在现有 server 层、现有 location 分流之前，保留原超时和转发设置，不替换整份站点配置。先按现场配置渲染独立 include 并 review，再 `nginx -t` 与 reload；失败立即恢复入口备份。

必须在预演和生产入口验证：POST/PATCH 被拦，GET/DELETE 同样被拦；尾斜杠、大小写、URL 编码/重复斜杠、现有路径重写和所有域名/直达 IP 入口不能绕过。盘点 Cloudflare 外的入口；应用端口仍只绑定 127.0.0.1，内部直接调用方也须纳入禁用约束。任何未知绕过路径未排除则不启动 .24。**Nginx 改动是本方案提出的新授权项，不沿用阶段 D 的授权。**

这是入口管理限制，不是宣称应用具有全局缓存开关。它不解析模型请求正文中的 cachedContent 引用；首次启用前必须确认没有既有网关缓存绑定、缓存调用方或未登记资源。初次窗口不做真实缓存创建测试，使用预演合成上游证明功能。若无法可靠覆盖入口，应推迟部署或先单独开发/评审应用侧开关，不临时修改已发布镜像。

### 已产生缓存时的回退顺序

即便按上述建议首次关闭，回退仍需检查以下路径，不能预先勾选“无缓存”。

1. **封住新写入并排空：** 保持公共缓存管理入口关闭；与调用方停止缓存引用/续期，回退维护窗口关闭公共网关新流量并排空在途请求。保留 .24 在受限本地入口运行以处理恢复和清理，暂不换 .23。不依赖只封 POST 而放过 PATCH。
2. **盘点全量：** 受限查询新表所有记录（包括过期/软删除行），记录 id/public_id、account_id、api_key_id、group_id、upstream_name、expire_time、租约状态和渠道计费口径，内容不贴公共日志。检查创建/续期请求记录、`orphan_cleanup_failed`、`record_usage_failed` 与恢复失败日志；对照每个相关上游账号的资源列表和账单。上游列表可能含其他系统资源，只处理已确认属于本实例的资源。
3. **先解决续期：** 保持原账号能力、凭据、原 TTL 配置及 .24 恢复 worker。等在途 PATCH 完成，对过期租约观察实际恢复；所有涉及资源的 mutation 都有明确结果后才删除。不能人为清空 mutation_token，也不能因为租约过期就假定上游没生效。
4. **删除已确认资源：** 正常有效绑定使用原拥有者 API key、原分组，通过 SSH 限定的本机 `.24` DELETE 接口删除；key 从受限文件/内存读入，不放命令行、浏览器截图或日志。不能用管理员 token 冒充缓存拥有者。逐项核验上游 GET/控制台确为不存在，再检查本地记录和费用。网关 Resolve 对本地不存在、过期或归属不匹配返回 403；该 403、路由级 404 和超时都不是上游不存在的证明。
5. **异常资源另处理：** 原 key 不可用、归属已变、账号已删除/不支持、过期记录无法经网关 Resolve、或上游成功却本地无记录时，经专门授权用正确上游账号凭据直接检查/删除，记录限定资源清单；无凭据则停止回切，保留 .24 管理能力和证据。不能全账号批量删除，不能凭数据库空表清账。
6. **核对计费和引用：** 对创建/续期 operation 的 usage、预留释放和上游费用逐项对账，确认无待处理计费任务或未解释的记录失败。删除不自动退款；任何调整费用另行批准。通知并确认调用方不再引用将失效的网关缓存 ID，不把这些 ID 留给 .23 当普通上游缓存处理。
7. **回切放行条件：** 在新写入持续封锁后，所有本实例资源都有已删除/已到期的上游证明，所有 mutation、孤儿清理和计费差异已解释解决，客户端引用已清除，且复核人签字。随后停止 .24、按第 6 节回切 .23。上游不可达时可以等待**经核实的实际最晚到期时间**，包括最后一次可能成功的续期和时钟偏差；不能只等本地 expire_time 或固定 24 小时。无法证明则不声称清理完成。

以下只读汇总用于现场盘点（迁移完成后执行），不输出 mutation token 内容，也不代替上游核验：

```sql
SELECT count(*) AS all_records,
       count(*) FILTER (WHERE deleted_at IS NULL AND expire_time > now()) AS active_records,
       count(*) FILTER (WHERE deleted_at IS NULL AND mutation_token IS NOT NULL) AS pending_mutations,
       count(*) FILTER (WHERE deleted_at IS NOT NULL AND mutation_token IS NOT NULL) AS deleted_with_mutation,
       max(expire_time) AS latest_recorded_expiry
FROM gemini_cached_contents;
```

不提供自动 DELETE/退款 SQL；本地软删除、删表或恢复旧库不能停止上游存储收费。重大安全事件可先封入口、隔离应用以止损，但是否停止恢复 worker、如何保存费用与资源状态须记录为异常回退，不能算完成清理。

## 4. Compose 并回：不仅替换镜像

当前基础文件仍是 .22，override 是 .23 加诊断设置；只改基础 image 而保留 override，会在两文件启动时再次覆盖为 .23。

1. 准备动作先保存两文件合并后的**受限**渲染快照；完整 `docker compose config` 可能含密码，只落 0600 文件，不输出到终端、日志或 Git。普通验证使用 `config --quiet`。
2. 以现场基础文件复制候选基础 Compose，保留所有现有数据库/Redis配置和原 `sub2api-network`，只给 sub2api 整合：.24 digest、`SUBPROXY_ENABLED=true`、`http://subproxy:8081`、原有 token/HMAC file 环境引用和只读挂载、追加 `subproxy-caller` 外部网络声明。禁止把 runtime secret 内容写进 YAML；检查 config.yaml/env 无 env/file 双引用。
3. 比较“旧基础+override”的渲染结果与“候选基础单文件”，除应用镜像 .23→.24 外，服务配置、端口、挂载、环境值、healthcheck、重启策略、网络及资源限制应等价。输出仅差异键名和通过/失败，不打印 secret 值。容器依赖与卷名称、项目 `sub2api` 不变；不添加 default 网络。
4. 将原 override 移到本次受限备份中，原路径保留**空 override** `services: {}` 作为旧两文件命令的兼容垫片。确认它不再包含 image、诊断、挂载或网络覆盖。检查单文件与两文件的渲染结果完全一致。不保留会暗中覆盖镜像的旧 override，也不删除所有备份。
5. 扫描现有运维脚本、定时任务和文档，改为单文件的正式命令；兼容垫片保留期和后续移除另记待办。本次不得运行不限定服务的 `up` 或 `down`。

候选文件替换后，在 `/home/aoxtoken/sub2api` 使用：

```sh
docker compose -p sub2api -f docker-compose.yml config --quiet
docker compose -p sub2api -f docker-compose.yml -f compose.subproxy.yml config --quiet
# 仅 G3 停服/备份/恢复验证通过后执行：
docker compose -p sub2api -f docker-compose.yml up -d --no-deps --no-build --force-recreate sub2api
```

Compose 替换在 sub2api 停止、备份验证完成后进行；临时文件先校验，原子替换，失败恢复同一批备份。**只合并 image、不合并 secret/network/diagnostics 的方案不通过 review。**

## 5. 正式窗口与验收

- [ ] 现场确认 .23 digest、四服务健康/重启、磁盘、入口超时、token 权限、迁移范围；采集新的 15 分钟基线并冻结网关错误率/延迟比较口径。不要读取凭据到日志。
- [ ] 提前按 digest 拉 .24，核对 -version 和提交；G2 的缓存入口限制生效。准备 .23 的两文件回退包，以及“已整合基础+空 override”的 .23 回退候选并比较等价性，选定一种写进执行记录。
- [ ] G3 后按第 2 节停服、新备份与隔离恢复；按第 4 节整合 Compose，再只重建 sub2api。记录中断起止时间；超获批上限则进入第 6 节，不能等待无限重试。
- [ ] 核对 .24 镜像、版本、健康与启动日志，schema_migrations 恰好新增上述四个完整文件名，旧校验和不变；核对新 schema、索引、旧表及关键业务数据无非预期变化。
- [ ] PostgreSQL、Redis、subproxy 的容器 ID/启动时间/重启数必须与前一致。public health/login/models（匿名）=200/200/401；匿名 capabilities/诊断保持 401。
- [ ] 授权管理员会话可用；capabilities **直接捕获脱敏响应** enabled=true/schema_version=1，诊断控件保留。仅使用再次获批的代理做 quick/最近报告/取消检查，不修改代理记录；调用方取消现有 service_unavailable 告警单独计数，不当成持续服务故障。
- [ ] 检查分组、套餐、付款/促销配置可读且无异常；真实下单/退款不在默认验收范围。
- [ ] 仅按 G2 批准的 key/模型/费用发最小非流式网关请求，核对 usage 与响应；没有费用授权则该验收保持待办，不宣称部署验收完成。首次默认不创建真实 Gemini 缓存。
- [ ] 公网缓存管理接口按预期返回维护响应；所有已知入口均验证，新表无缓存资源/孤儿事件。维护 503 从预期维护流量单列，不能用它掩盖原网关 5xx。
- [ ] 至少 30 分钟观察，45–60 秒采样：四服务健康/重启/CPU/内存、磁盘、网关 5xx/延迟、诊断分类、迁移和缓存恢复/计费错误。延迟按平台、流式/非流式及相近请求比较，不能以总耗时冒充 TTFT；流量不足明确标注。

停止条件：认证绕过/秘密泄露、迁移错误或范围不符、非预期数据变化、容器不健康或新增重启、持续网关 5xx/超时或资源饱和、诊断持续 503/schema 错误、中断超过授权上限、入口缓存限制可绕过。窗口前由复核人确认流量足够时的量化阈值；建议连续 3 个一分钟窗口网关 5xx >1%（每窗至少 20 请求）或同类请求 p95 >基线 2 倍，低流量用连续失败及逐请求复核，不能除以极小样本宣布性能恢复。

## 6. 回退矩阵

| 情形 | 动作与边界 |
|---|---|
| .24 未启动，备份/准备失败 | 恢复本窗口备份的 .23 **基础+override**，只重建应用。基础本身是 .22，不能单独拿它回退 |
| 四迁移已完成，确认从未创建/续期缓存且无孤儿 | 保持入口限制、排空/停止 .24；使用预演验证过的 .23 配置包回切。保留新增 schema 和迁移记录，不 DROP、不恢复旧库；核对 .23、诊断=true、网络/secret 和公共端点 |
| 缓存存在或状态不明 | 先执行第 3 节清理和计费核对，保留受限 .24 管理能力。未满足清理放行条件不直接回切 |
| 迁移部分完成/失败 | 保存错误、现有 schema 与迁移记录；依据预演和现场实际判断 .23 是否兼容。不得手工标记迁移成功、盲目删字段或重试改变数据；不满足兼容证明则保持维护，升级为故障处置 |
| 数据损坏，确需恢复备份 | 另获明确恢复授权：封所有写入，保存故障现场的新备份，核对备份后新增业务、付款、usage 与上游缓存资源，明确 RPO/数据损失和对账。先恢复到独立实例验证，再批准切换；不自动覆盖生产数据库 |

若用预先渲染的“整合 .23 基础+空 override”回退，必须确认其仅把 .24 digest 换回 .23，其他设置等价。若用原两文件包则必须带两个 `-f`；两种方式不可混用。整个回退不重建数据库/Redis/subproxy，不关闭诊断来掩盖升级问题。紧急止损与“完整恢复服务且缓存/账单已处理”分别报告。

## 7. 证据与完成条件

建议证据目录：本地 `/Users/dalu/aiworkspace/codex/runtime/sub2api-aox24-deploy-<日期>/`，远端 `/home/aoxtoken/sub2api/backups/pre-v0.2.16-aox.0.0.24-<UTC时间>/`。日志脱敏，敏感渲染/备份仅在受限位置，不入 Git。

| 记录 | 执行时补齐 |
|---|---|
| 授权 / G0 | 24h 复查及复核链接、窗口、停机上限、付费上限、回退授权 |
| 预演 | 环境清单、迁移结果、.23 兼容验证、合成缓存异常清理与计费证据 |
| 入口 / Compose | 各入口封禁矩阵、Nginx 校验/回退文件、渲染等价性、空 override、单/双文件一致性 |
| 新备份 | 目录、SHA-256、dump 大小/时间、恢复结果、表计数、角色权限验证范围 |
| 上线 | 前后容器身份、实际 digest/版本、四迁移校验、健康、管理员/诊断/网关验收、中断秒数 |
| 缓存 / 回退 | 全量资源盘点、mutation/孤儿/usage 核对、上游删除或到期证明、放行签字 |
| 观察 / 收尾 | 30 分钟采样、异常及处置、最终状态、备份保留期限、升级后 24 小时复查待办 |

成功后另提文档 PR 更新 PRODUCTION_DEPLOYMENT.md 的**实际**版本、基础 Compose 单文件用法、空 override 兼容状态、新备份和回退包；不要提前把本计划写成已上线事实。保留 .23 镜像及本次备份至少到升级后 24 小时复查通过，再按既有保留策略处理，不自动删 .22 或历史恢复材料。计划阶段不设定时任务、不部署、不清缓存、不改生产 Compose。
