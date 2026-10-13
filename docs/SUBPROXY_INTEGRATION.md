# subproxy 管理员诊断接入（I1 / I2）

管理员代理页通过能力查询切换诊断入口，默认关闭。兼容版本：

| sub2api | subproxy | 报告契约 |
| --- | --- | --- |
| I1（PR #48）+ I2（issue #45） | S1–S5，`1f6c39d`；文档更新 `0b8e373` | schema v1 |

关闭时保留旧 `quality-check`；启用时显示逐项报告，隐藏旧分数/等级。能力查询失败会提示重试，不会静默回退旧检测。未知 schema 主版本明确报错。

## 配置

```yaml
subproxy:
  enabled: false
  base_url: https://subproxy.internal.example
  allow_internal_http: false
  token:
    env: SUBPROXY_SERVICE_TOKEN
  hmac_secret:
    file: /run/secrets/subproxy_hmac
```

`token` 和 `hmac_secret` 各自只设置 `env` 或 `file`。HMAC 密钥至少 32 字节，独立生成，不得复用服务 token。配置仅包含引用；实际值由运行环境或 secret 文件提供。启用时读取一次；轮换后重启 sub2api。token/HMAC 轮换会使旧报告不可读。关闭时不读取 secret、不创建 HTTP 客户端或探测远端。

Viper 环境变量也支持配置，例如 `SUBPROXY_ENABLED`、`SUBPROXY_BASE_URL`、`SUBPROXY_TOKEN_ENV`、`SUBPROXY_HMAC_SECRET_FILE`。不要把实际 token 放进 `SUBPROXY_TOKEN_ENV`，它存储的是另一个环境变量的名字。

跨主机必须使用 HTTPS。同一主机的私有容器网络可显式设置 `allow_internal_http: true` 和 `http://subproxy:8081`；该设置是运维对同机网络隔离的声明，代码无法证明一个容器名是否位于同机，禁止用来降级跨主机链路。客户端不使用环境 HTTP 代理、不跟随重定向；服务证书必须受系统信任。

## 管理员接口

所有接口均经过现有 AdminAuth；供应商、普通用户不可调用。

| 接口 | 行为 |
| --- | --- |
| `GET /api/v1/admin/proxies/diagnostics-capabilities` | 返回 `enabled`、`schema_version: 1`、`modes: [quick, sample]`；关闭时仍 200；不探测远端健康 |
| `POST /api/v1/admin/proxies/:id/diagnostics` | body 严格为 `{"mode":"quick"}` 或 `{"mode":"sample"}`；代理配置由后端读取 |
| `GET /api/v1/admin/proxies/:id/diagnostics` | 读取当前配置对应的最近报告 |

正常结果使用项目响应封装，`data` 为 `{report, saved}`。HTTP 200 中的报告可以描述代理不可用；不要把代理故障与检测服务故障混为一谈。

裸 HTTP 401 表示管理员会话失效，页面提示重新登录；内部检测服务认证失败使用 HTTP 503 / `service_auth_failed`，两者不混用。

| HTTP / reason | 含义 |
| --- | --- |
| 503 / `DIAGNOSTICS_DISABLED` | 功能关闭 |
| 404 / `REPORT_NOT_FOUND` | 没有有效缓存或 Redis 不可读 |
| 404 / `PROXY_NOT_FOUND` | 代理已删除 |
| 409 / `PROXY_CHANGED` | 检测期间代理配置改变 |
| 503 / `service_unavailable` | 网络、超时、无效报告或远端服务错误 |
| 503 / `service_auth_failed` | subproxy 拒绝服务 token，不是用户登录失效 |
| 503 / `schema_unsupported` | subproxy 或缓存报告使用不兼容的 schema 主版本，需核对版本对应表 |
| 429 / `busy` | 检测服务忙；`Retry-After` 统一返回正整数秒；上游 HTTP 日期转换为剩余秒数并向上取整，缺失/非法/过期值及零值回退为 1 秒 |
| 400 / `proxy_address_unsupported` | subproxy 地址策略不支持该代理 |
| 400 / `request_invalid` | subproxy 拒绝代理配置格式，例如 host 包含 scheme 或端口 |

后端调用上限 65 秒，并透传请求取消。浏览器检测 POST 显式设置 70 秒超时，覆盖公共客户端的 30 秒默认值；反向代理该路由读超时至少 75 秒，启用前核对真实入口层。Nginx 可对相应路由设置 `proxy_read_timeout 75s`（已有更长配置可保留）；Caddy/其他入口需核对 response-header/read timeout 和外层负载均衡配置。

诊断失败输出 `proxy_diagnostics_failed` warn 日志，仅附加 `proxy_id` 和固定的 `classification`；包含服务请求、报告校验、代理变更及缓存预约/提交/读取失败的分类，不记录代理地址、凭据、token、远端正文或原始错误。功能关闭记录 debug；本地非法模式为 `invalid_mode`，远端配置拒绝为 `request_invalid`。

## 管理员页面

选择快速检测（quick）或短时采样（sample），逐条查看出口、来源、检查时间、规则/插件版本、状态和证据；这些检查不调用模型，不代表账号或模型调用一定可用。最近报告读取当前代理配置的缓存，使用独立标题、缓存状态和原检查时间，不显示批量并发提示。能力接口返回 404 时显示“检测能力接口不可用”，不与代理/报告不存在混用。

批量检测所选代理；未选择时检测当前筛选条件下的全部代理。最大并发 2，每份报告完成立即显示。关闭弹窗或离开页面会停止排队任务并取消在途请求。遇到 429 后停止派发剩余任务，保留另一条在途任务及已完成结果，不自动重试；稍后由管理员重新发起。

普通结果按稳定 ID 通用渲染。新增项和未知 `reason_code` 显示原码并标记未识别；证据显示后端已校验的标量，不在前端维护插件专属白名单；所有内容按纯文本渲染。报告未缓存时显示“未保存”，不伪装为已保存。404/409/429/503 与代理报告中的 unusable 分开显示。

## 启用前验证

1. 先在隔离容器中部署对应版本，数据库/Redis/服务 token 与生产隔离；subproxy 只连同机私有容器网络，不发布公网端口。
2. 确认 capabilities 的 enabled/schema_version、70 秒浏览器超时及所有入口 ≥75 秒；真实 sample 应能超过 30 秒后正常完成。
3. 管理员检查单条、最近报告、批量并发 2、取消、429 停止派发；供应商和普通用户访问三个管理接口均应被拒绝。
4. 验证服务关闭保留旧入口；服务不可用不会回退；未知项、挑战、部分结果和未保存报告均可显示。桌面与手机查看无横向溢出。
5. 已授权代理凭据仅通过私有运行时文件导入测试实例；错误密码使用独立临时代理，不修改线上记录。报告与日志不应包含代理密码、用户名或服务 token。

## 报告和缓存边界

quick 报告也保留 stability 项，状态为 `skipped/mode_not_sample`；该项计入 `completion.planned` 和 `completion.skipped`，不计入 `executed`。

接收最多 256 KiB、32 项，校验 v1 必填字段、状态、类型和数值边界。允许同版本新增可选字段，但不转发未知字段。证据只接受简单标量，单值字符串上限 2048 字节，每项最多 32 个键、16 KiB；样本最多 32 个。脱敏报告的上游生产者为受信任 subproxy，其核心按插件 Descriptor.EvidenceKeys 过滤证据；sub2api 只校验键名和标量类型，不维护每插件白名单。接入非自建报告源前必须补充生产者信任与证据过滤校验。

代理指纹对固定字段顺序的 scheme/host/port/username/password 及服务 token 做 HMAC-SHA256；不返回指纹。Redis 只存指纹、预约标识和报告，不保存代理凭据。每次预约使用随机 nonce，Lua 同时比较最新预约与指纹并写报告，预约和报告 TTL 均为 24 小时。保留原预约字段和报告格式，可读取升级前的缓存；旧计数键不再使用，按原 TTL 自行过期。

迟到报告可返回 `saved=false`，不能覆盖新预约；较新预约自身失败时也保留这一语义，不让旧请求重新取得提交资格。Redis 故障时仍可返回本次结果，但不会声称已保存。调用返回前重读代理并核对指纹；缓存读取时再次核对指纹，作为数据库修改与 Redis 写入间竞态的最终防线。

## 发布边界

I2 隔离容器实测（2026-10-12）：10 条授权代理 quick 均为 reachable，保存/读取报告一致；同来源 curl 出口 10/10 一致（一条走 api64 备用 IPv6 源）。sample 实测 pass。独立错误密码代理返回 unusable/proxy_auth_failed；服务停止返回 503/service_unavailable；关闭功能返回 enabled=false；供应商/普通用户三个管理接口均 403，匿名诊断 401。报告和服务日志未包含代理凭据或服务 token。浏览器桌面/手机验收另见 PR 记录。

I1/I2 不启用生产、不发布生产镜像。合并、镜像发布、生产启用分别安排。回滚时关闭 `subproxy.enabled` 并重启 sub2api，刷新页面即可恢复旧检查入口；不删除代理、不修改账号绑定、不恢复数据库。Redis 诊断缓存按 24 小时 TTL 自行过期。
