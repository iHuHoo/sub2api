# subproxy / sub2api 首次联合发布与生产启用方案

状态：**方案已评审（PR #53）；subproxy 1.0.0 已发布并复核；sub2api 发布及后续阶段待单独授权，未部署、未启用生产**。编制及本次记录日期：2026-10-13。

PR #53 合并只代表方案通过评审。PR 合并不等于发布授权；后续每一步镜像发布、生产部署、启用及生产回滚操作，均须单独取得明确授权，记录执行人、复核人及批准范围；不得把前一步授权延伸至下一步。下文命令是执行手册，不由合并触发。

## 1. 发布范围与版本冻结

| 组件 | 已合入的候选源码 | 发布标识（含待发布项） | 部署标识 |
| --- | --- | --- | --- |
| subproxy | `9ea62d26ad969374b4dabcd07ff7f8c44f140e40`（#17，含 S1–S5 与清口） | `ghcr.io/ihuhoo/subproxy:1.0.0` | `ghcr.io/ihuhoo/subproxy@sha256:a62d87142145c8534094a29a6a9be70bb004ef6625cc206d3a5782f8394debe6` |
| sub2api | `41d5706d34ad87057129e5596e8a43ab4eda3163`（#52，含 I1/I2、#51 race 修复与清口） | 拟用 `v0.2.15-aox.0.0.23` | 待发布后填入 `ghcr.io/ihuhoo/sub2api@sha256:…` |

编制时两边远端 main 与上表一致；sub2api 最新 release 是 `v0.2.15-aox.0.0.22`，拟用 .23 tag 尚不存在。**执行前重新检查 tag 和镜像是否已占用，绝不覆盖已有版本。** 若需改源码、版本、架构或规则，更新记录并重新复核，不能跟随浮动 main/latest 构建。

sub2api 的 .22 tag 指向 `2f6ab29bd1d58dc5decc8e081a94d2b30cbdd35b`，与本候选的 `backend/migrations/` 无差异。此结论仅适用于这两个提交；[生产记录](PRODUCTION_DEPLOYMENT.md) 最后核实于 2026-09-24，**不是当前生产版本证明**。

范围包括固定源码的镜像、同版规则、必要的网络/secret/超时配置、诊断启用及回滚。不升级 PostgreSQL、Redis、Docker 或宿主机；不改变 Redis 持久化；不启用目前禁用的目标规则，不改变账号绑定或线上代理凭据。

依据：

- [subproxy 运维手册（冻结版本）](https://github.com/iHuHoo/subproxy/blob/9ea62d26ad969374b4dabcd07ff7f8c44f140e40/docs/operations.md)
- [管理员诊断契约与启用前验证](SUBPROXY_INTEGRATION.md)
- [现有生产部署与数据库回滚约束](PRODUCTION_DEPLOYMENT.md)
- [Aox 镜像工作流](../.github/workflows/build-aox.yml)

subproxy 运维手册中的“sub2api 业务集成尚未实施”是历史描述；此次配套版本已包含 I1/I2，以本方案及接入文档为准。

## 2. 执行记录与放行条件

执行人逐阶段填写证据，复核人确认后进入下一阶段。空白或失败均不可作为通过；秘密、完整环境文件及未脱敏报告只保存在受限运行目录，PR 仅放脱敏结论和日志位置。

| 记录项 | 执行时填写 |
| --- | --- |
| 发布授权 / 生产授权、窗口、执行人 / 复核人 | 用户明确指定 1.0.0 并授权第一步；Codex 执行，用户独立复核通过。第二步及各生产操作仍待单独授权 |
| 第一步版本与构建环境偏差 | 用户将原 sha-tag 改为 1.0.0；实际在 ARM64 Mac / Docker Desktop 使用 AMD64 模拟构建与运行验收，并非原计划的原生 AMD64 构建机；目标仍为 linux/amd64。两项偏差已由用户复核接受 |
| 第一步发布证据 | 本地 `$CODEX_RUNTIME_DIR/subproxy-1.0.0-release/`：release-record.json、image-before-test.txt、build.log、container-tests.log、push-authorized.log、remote-manifest.txt、pull-by-digest.log；race/vet/契约及两项容器验收通过，验收 image ID 与发布 digest 相同，按 digest 拉取复核成功。用户独立复验通过，日志无 token 泄漏 |
| 阶段 C 前置：生产 GHCR 拉取权限 | **未落实，阻止进入阶段 C**。包当前 private，匿名拉取 403；必须在生产主机使用有该包读取权限的账号及只读 read:packages token 登录，并按上表 digest 成功预拉；或在另行明确授权后将包公开并验证匿名拉取。不能使用开发机的成功拉取代替生产主机验证 |
| 生产当前版本、源码、镜像 digest、CPU 架构、Compose 版本 | 待现场核实 |
| 当前 Compose 项目/文件、数据挂载、入口代理/负载均衡及超时 | 待现场核实 |
| 历史 .15 → 候选的迁移范围（实际范围仍以现场源码为准） | 5 个 SQL：`241_add_payment_order_bonus_amount.sql`、`241_add_typesafe_platform.sql`、`242_drop_platform_check_constraints.sql`、`243_supplier_ownership.sql`、`244_live_proxy_references.sql`；逐项审阅、校验和及恢复演练待填 |
| 同范围新增的迁移回归测试（不执行为 SQL） | `platform_check_constraints_drop_migration_test.go`、`typesafe_platform_migration_test.go`；迁移目录合计新增 7 个文件 |
| 两边目标源码 CI 链接与结果、发布 run / 构建日志 | 待填；PR 的绿灯不代替实际构建源码的验证 |
| 两个新镜像 digest、架构、sub2api 内嵌版本/commit | subproxy digest 见版本表，linux/amd64，revision=9ea62d26a，运行用户 65532；sub2api 待发布后核实 |
| 同版 rules/deploy 归档校验和、实际 ruleset_version、覆盖项清单 | 待填 |
| 隔离验收记录、入口测试、权限矩阵、脱敏检查 | 待填 |
| 旧镜像/配置/secret/规则备份位置、数据库 dump 校验及恢复演练 | 待填 |
| 生产部署时间、关闭态检查、启用时间、观察结果、回滚决定 | 待填 |

第一步镜像发布已完成，不代表两个 digest 的联合阶段 B 已通过。当前已合入的 CI 和历史隔离验收只是准备依据，不预先勾选本次待发布镜像的验收。计划 review 可先完成；实际发布/上线必须补齐对应阶段的记录。

## 3. 阶段 A：候选镜像发布（需发布授权）

1. 在干净的隔离 checkout 检出上表完整 SHA，确认无本地修改。复核两边该 SHA 的 CI；sub2api 单测必须包含 `make test-unit` 的 `-race`，subproxy 必须有 race/vet/build/契约及隔离容器验证。
2. 核实生产架构。以下首发方案面向 `linux/amd64`，与 Aox 工作流当前默认 Linux runner 构建对应；若现场不是该架构，停止并补充架构构建/验收方案。
3. subproxy 当前 CI 只构建不推送，没有发布工作流。首次原计划使用已认证、受控的 amd64 构建机人工发布；实际 ARM64 主机模拟构建的偏差已记录并获复核接受。不另加自动发布机制。以下保留执行方式供追溯，1.0.0 已存在，禁止重跑覆盖：

   ```sh
   set -eu
   # 在 subproxy 仓库的冻结提交上执行
   SP_IMAGE=ghcr.io/ihuhoo/subproxy:1.0.0
   test "$(git rev-parse HEAD)" = 9ea62d26ad969374b4dabcd07ff7f8c44f140e40
   test -z "$(git status --porcelain)"
   docker build --platform linux/amd64 -f deploy/Dockerfile \
     --label org.opencontainers.image.revision=9ea62d26ad969374b4dabcd07ff7f8c44f140e40 \
     -t "$SP_IMAGE" .
   # 先按 operations.md 建立 schema 校验 Python 环境，并把其 bin 加入 PATH。
   DOCKER_DEFAULT_PLATFORM=linux/amd64 SUBPROXY_IMAGE="$SP_IMAGE" go test -tags=container -count=1 -v ./tests/container
   docker push "$SP_IMAGE"
   docker buildx imagetools inspect "$SP_IMAGE"
   ```

   测试后直接 push 同一 image，不重新构建。记录本地 image ID、推送 digest、revision 标签与架构；按 digest 拉取后核对身份。构建机沿用受控 TMPDIR，不并行运行占用固定子网的容器测试。GHCR 认证经凭据存储/标准输入，不把 token 放入命令或日志；生产主机的包读取权限仍待落实，见记录表和阶段 C 放行条件。ARM64 主机重验时必须设置 `DOCKER_DEFAULT_PLATFORM=linux/amd64`，否则构建上下文测试可能按宿主平台解析 FROM 并报 no match for platform。OCI index 中另一个 unknown/unknown 条目是 buildx attestation，不将其作为已验证的密码学签名。
4. sub2api 使用 `build-aox.yml`，不调用通用 `release.yml`。审核 tag 未占用后，在冻结提交上创建并推送上述 Aox tag；**推 tag 即触发镜像发布**，仅在授权后执行：

   ```sh
   set -eu
   git tag -a v0.2.15-aox.0.0.23 41d5706d34ad87057129e5596e8a43ab4eda3163 \
     -m "subproxy diagnostics integration and race fixes"
   git push origin refs/tags/v0.2.15-aox.0.0.23
   ```

   核对 Build Aox Image run 的 head SHA、成功状态和 summary 中的镜像 digest。该工作流仅推 GHCR 镜像，不自动创建 GitHub Release 或部署生产，不需要额外 dispatch。容器内 `/app/sub2api -version` 应对应 `0.2.15-aox.0.0.23` 与目标 commit。
5. 两个镜像按 digest 拉取进入阶段 B。tag 仅便于发现，生产配置只使用 digest。任何重新构建都产生新的候选验收记录，不沿用旧镜像的测试结论。

## 4. 阶段 B：同一镜像的隔离预演

使用独立 Compose 项目、数据库、Redis、secret 和网络，不接生产调用网络。主机本地证据使用 `$CODEX_RUNTIME_DIR`；生产端证据进入受限备份目录。不得将生产数据或凭据提交 Git。

### 网络、配置与权限

- 拟采用同机双网络：subproxy 加入 internal 的专用 caller 网络与独立 egress 网络；sub2api 保留原数据库网络，再加入 caller 网络。subproxy 不发布主机端口，不增加公网反代。验证 egress 能访问获准公网代理/情报源。
- 复制冻结版本的整个 `deploy/` 和 `rules/` 到同一个版本目录，保留相对路径及空的 `deploy/custom-rules/`。部署使用 `deploy/compose.example.yml`，镜像由 `SUBPROXY_IMAGE` 指定 digest；使用固定 Compose 项目名避免升级时重复建栈。
- 对比所有内置规则、禁用项、域名白名单及已有 custom override，复核启用源的许可/配额。override 的 `replaces_version` 必须与该版内置规则一致。不可只换镜像而保留未经审核的规则挂载。
- subproxy 示例使用 Compose `gw_priority`；现场需支持它，按运维手册已验证的 Compose 5.5.1 核对，不能忽略解析错误。若需升级运行时，另行安排，不夹带在本窗口。
- 两个独立随机 secret：双方共享 service token，HMAC secret 只供 sub2api 使用且至少 32 字节。使用文件引用，目录 0700；按实际容器 UID 验证文件可读，参考运维手册的文件 bind mount 权限说明。配置文件只包含路径。
- 下方 override 是待按现场路径渲染的模板；需与实际 Compose 合并检查。先保持 enabled=false。原 Compose 有额外服务网络时全部保留；从既有 config.yaml/环境中移除冲突的 token.env/hmac_secret.env 引用，确保每个 secret 只选 env 或 file。

```yaml
# compose.subproxy.yml：与现场 docker-compose.yml 合并，不单独运行。
services:
  sub2api:
    image: ${SUB2API_IMAGE:?set reviewed digest}
    environment:
      SUBPROXY_ENABLED: ${SUBPROXY_ENABLED:-false}
      SUBPROXY_BASE_URL: http://subproxy:8081
      SUBPROXY_ALLOW_INTERNAL_HTTP: "true"
      SUBPROXY_TOKEN_FILE: /run/secrets/subproxy_service_token
      SUBPROXY_HMAC_SECRET_FILE: /run/secrets/subproxy_hmac
    volumes:
      - ${SUBPROXY_TOKEN_HOST_FILE:?set absolute host path}:/run/secrets/subproxy_service_token:ro
      - ${SUBPROXY_HMAC_HOST_FILE:?set absolute host path}:/run/secrets/subproxy_hmac:ro
    networks: [subproxy-caller]
networks:
  subproxy-caller:
    external: true
    name: ${SUBPROXY_CALLER_NETWORK:?set caller network}
```

宿主路径变量使用 `SUBPROXY_TOKEN_HOST_FILE` / `SUBPROXY_HMAC_HOST_FILE`，仅用于 Compose 挂载插值；容器环境变量 `SUBPROXY_TOKEN_FILE` / `SUBPROXY_HMAC_SECRET_FILE` 固定为容器路径。subproxy 自身 Compose 的 `SUBPROXY_TOKEN_FILE` 仍按其运维手册设为同一 service token 的宿主路径，不把该栈的环境文件注入 sub2api。网络按并集合并，模板只追加 caller 网络，保留基础 Compose 的 `sub2api-network`，不额外创建 default 网络。先启动 subproxy 创建 caller 网络，再启动 sub2api；不能只在宿主 .env 设置开关却遗漏容器 environment。启用/关闭后使用 `up --force-recreate` 应用环境，单纯 `restart` 不更新容器环境。

仅同机私有网络可使用上述 HTTP 配置。若实际跨主机，改为受信任 HTTPS、`SUBPROXY_ALLOW_INTERNAL_HTTP=false`，重新评审网络方案；不关闭 TLS 校验或开放公共诊断端口。

### 验收矩阵（两份最终 digest 都必须覆盖）

| 检查 | 通过条件 |
| --- | --- |
| 基础启动与隔离 | 两镜像身份/架构正确；subproxy 非 root、只读根、cap_drop/no-new-privileges、无端口发布；健康检查成功；DB/Redis 与生产隔离 |
| 关闭态 | capabilities 为 enabled=false；旧 quality-check 入口可用；sub2api 不因未配置 secret 而失败 |
| 真实入口超时 | 浏览器 70 秒、后端 65 秒；所有反代/LB 对诊断路由读取/响应头等待至少 75 秒；受控 sample 超过 30 秒仍正常返回，无 504 |
| 管理员单条与缓存 | quick/sample 报告及 ruleset_version 正确；quick stability 为 skipped/mode_not_sample；最近报告时间、保存状态正确；出口与同来源对照一致 |
| 批量与取消 | 并发不超过 2；关闭弹窗/离页停止排队并取消在途；429 含正整数 Retry-After 并停止后续派发，不自动重试 |
| 失败语义 | 临时错误密码代理返回报告 unusable/proxy_auth_failed；服务停止为 503/service_unavailable；错误服务 token 为 503/service_auth_failed；不回退旧检测 |
| 权限与页面 | 匿名诊断 401；普通用户/供应商三个管理接口均拒绝；管理员正常；桌面/手机无横向溢出 |
| 契约与异常缓存 | 404 无报告、409 配置改变、未知 schema、未知项/挑战/部分结果、saved=false 均正确显示；Redis 不可用不伪装为已保存 |
| 脱敏与回滚演练 | 报告/服务日志不含代理用户名、密码、token；关闭诊断并重建/刷新后旧入口恢复；恢复原配置后原业务正常 |

异常注入仅在隔离环境执行：使用临时代理和测试服务/fixture，不修改线上代理、停止线上 Redis 或替换生产 token。历史 I2 的 10 条代理验收不能代替此次最终 digest 的验收；真实探测仅使用明确授权的代理，凭据从私有运行文件导入，不通过 shell 参数传递。

## 5. 阶段 C：生产部署，诊断保持关闭（需生产授权）

**进入本阶段前先解决私有包拉取权限，并单独取得相应生产操作授权。** 首选保持包私有，配置只读 `read:packages` token；所属账号须有该包读取权。经标准输入执行 `docker login ghcr.io --username <读取账号> --password-stdin`，不把 token 写进命令、Git 或日志，不复制开发机的写入/删除权限凭据。保护生产 Docker 凭据存储，记录凭据引用、权限、失效/轮换安排及按 digest 预拉的脱敏结果。若改公开，须另行批准并验证匿名拉取；本次未改变包可见性、未登录生产。未成功预拉前不部署 subproxy、不停 sub2api。

1. 先按 [生产部署文档](PRODUCTION_DEPLOYMENT.md) 核实主机、目录与实际 Compose 项目。记录现有 sub2api 版本/digest、PostgreSQL/Redis 镜像、健康/重启数、磁盘余量及入口配置；记录至少 15 分钟原有网关错误率和延迟基线。若现场与假设不符，暂停修改并修订执行记录。
2. 比较实际生产源码到冻结候选的迁移清单、校验和及行为变更。若生产确为 .22，复核迁移无差异；若更旧，逐项审阅并在隔离恢复副本上演练。特别不能仅凭“诊断功能无 SQL”跳过整包升级检查，也不能忽略旧生产文档中的迁移 239 回滚约束。
3. 预拉两个审核 digest；建立受限的时间戳备份目录，保存实际 Compose/override/.env/config、入口配置、旧镜像 digest、secret 和规则归档。secret 不打印，完整 `docker compose config` 不写公开日志，仅用 `config --quiet` 校验。
4. 在 sub2api 仍运行时，先在版本目录部署 subproxy，检查无端口发布、健康、网络与挂载规则；它不依赖 sub2api，也不访问数据库，失败时暂停，不进入停服步骤。保留配置中的未核实目标禁用状态。subproxy 健康只证明进程存活，不作为业务验收。停服前还须检查实际 config.yaml 与容器环境：`subproxy.token.env` / `subproxy.hmac_secret.env` 及对应 `SUBPROXY_TOKEN_ENV` / `SUBPROXY_HMAC_SECRET_ENV` 无非空引用，file 引用指向预演验证过的容器路径。若 config.yaml 从新示例复制，必须移除其中默认的 `token.env: SUBPROXY_SERVICE_TOKEN`，避免与 file 同时存在导致启用失败；不要输出实际 secret。
5. 按现有部署程序停止 **sub2api 服务** 后做 PostgreSQL custom-format dump，校验非空、`pg_restore --list`、SHA-256，并在隔离数据库完成恢复检查。确认没有其他数据库写入者；有则先协调窗口。保留旧镜像，DB/Redis 服务及数据卷不删除、不重建。
6. 给生产 sub2api 加载已验证 override，digest 固定，显式 `SUBPROXY_ENABLED=false`。每次操作使用同一现场项目名、基础文件和 override 文件组合：

   ```sh
   set -eu
   docker compose -p "$APP_PROJECT" -f docker-compose.yml -f compose.subproxy.yml config --quiet
   docker compose -p "$APP_PROJECT" -f docker-compose.yml -f compose.subproxy.yml \
     up -d --no-deps --no-build --force-recreate sub2api
   ```

   subproxy 栈同样使用已核实的固定项目名、版本路径与 digest，启动加 `--no-build`，避免示例中的 build 段意外现场重建。不执行全栈 `down` 或 `down -v`。
7. 核对 sub2api 实际 image/digest、内嵌版本、启动/迁移日志、健康与重启数；公共 health/login 应 200，匿名 /v1/models 应 401。管理员登录、原 quality-check 和经批准的一条网关业务请求通过，再观察至少 15 分钟。失败按阶段 E 处理，不进入启用步骤。

## 6. 阶段 D：小规模启用与观察

1. 确认阶段 B/C 证据齐全、入口超时已生效、secret/权限正确且回滚负责人在线。在已批准窗口将 override 的有效开关改为 true，使用相同 Compose 命令强制重建 sub2api；不要通过重启整个栈实施。
2. 刷新管理员页面，检查 capabilities enabled=true/schema_version=1。该能力接口不探测 subproxy，必须再做真实 quick/sample。
3. 先一条已授权代理 quick，再 sample，再 2–5 条小批量；核对报告、保存/最近报告、版本及取消行为。只探测代理端点，不调用模型来证明诊断结果；原网关业务健康另行验证。
4. 此开关对所有管理员生效，当前没有按用户/百分比分流功能。“小规模”通过维护窗口约定仅由指定管理员操作，并限制所选代理数实现；不能声称存在自动灰度隔离。
5. 观察至少 30 分钟，记录 sub2api/subproxy 健康、重启数、CPU/内存、网关错误率/延迟、诊断延时和 `proxy_diagnostics_failed` 固定分类。全部通过后结束维护窗口；24 小时内复查诊断缓存和错误分类趋势。

以下任一情况立即停止派发并关闭诊断：认证/权限绕过、凭据泄漏、持续 503 或 schema 错误、sample 被入口超时截断、容器不健康/新增重启、原有网关业务回归。零容忍权限/泄漏问题；资源持续饱和或连续两轮小批量出现 429 时停止扩量，核对负载和配额后再安排窗口，不自动提高并发重试。

单个代理 unusable、挑战页、unknown/skipped、未保存报告不是服务故障的同义词；按报告语义排查，不能把 HTTP 200 一律算通过。

## 7. 阶段 E：回滚与收尾

优先做功能回滚：停止管理员派发，将有效 `SUBPROXY_ENABLED=false` 写回部署配置并强制重建 sub2api；刷新页面，确认 enabled=false 与旧入口恢复、原业务正常。诊断开关启动时读取，不能只改文件。必要时停 subproxy；不要修改代理记录、账号绑定或清空共享 Redis。诊断缓存按 24 小时 TTL 过期。

若问题属于整个 sub2api 镜像，先判断新迁移及写入与旧二进制是否兼容，再恢复备份中的镜像 digest、配置与网络定义，重建 **仅 sub2api**。没有已验证的兼容结论不得直接回切；数据库恢复另行决策，明确停写、备份当前状态及接受的数据损失窗口，禁止自动把旧 dump 覆盖在线库。

subproxy 回滚须成套恢复旧 digest、配置、内置 rules、自定义 override，必要时恢复对应 secret/CA，强制重建以更新 bind mount。首次启用没有旧 subproxy 时，关闭调用方后停服务即可。暂停调用后按 15 秒 Compose stop grace period 停止，正在检测的请求可能失败；不要承诺无损切换。token/HMAC 回退或轮换可能使旧诊断报告不可读，双方服务 token 必须一致。

成功后更新 [PRODUCTION_DEPLOYMENT.md](PRODUCTION_DEPLOYMENT.md)：实际版本/digest、旧镜像、备份位置、迁移结果、关闭态与启用验收、观察时间及执行记录链接。保留旧镜像、完整规则/配置和恢复材料至少到 24 小时复查通过，再按既有备份保留策略处理；失败则记录已回滚及原因，不把拟发布值写成线上事实。
