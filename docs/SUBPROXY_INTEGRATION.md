# subproxy 管理员诊断接入（I1）

本阶段仅提供后端 API，兼容 subproxy schema v1（S1–S5，main `1f6c39d`）。默认关闭；前端入口和批量操作由 I2 实现。旧 `quality-check` 保持原行为。

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

| HTTP / reason | 含义 |
| --- | --- |
| 503 / `DIAGNOSTICS_DISABLED` | 功能关闭 |
| 404 / `REPORT_NOT_FOUND` | 没有有效缓存或 Redis 不可读 |
| 404 / `PROXY_NOT_FOUND` | 代理已删除 |
| 409 / `PROXY_CHANGED` | 检测期间代理配置改变 |
| 503 / `service_unavailable` | 网络、超时、无效报告或远端服务错误 |
| 503 / `service_auth_failed` | subproxy 拒绝服务 token，不是用户登录失效 |
| 429 / `busy` | 检测服务忙 |
| 400 / `proxy_address_unsupported` | subproxy 地址策略不支持该代理 |
| 400 / `request_invalid` | subproxy 拒绝代理配置格式，例如 host 包含 scheme 或端口 |

客户端调用上限 65 秒，并透传请求取消。I2 浏览器调用应设置至少 70 秒超时；反向代理该路由读超时至少 75 秒，启用前核对真实入口层。

诊断失败输出 `proxy_diagnostics_failed` warn 日志，仅附加 `proxy_id` 和固定的 `classification`；包含服务请求、报告校验、代理变更及缓存预约/提交/读取失败的分类，不记录代理地址、凭据、token、远端正文或原始错误。

## 报告和缓存边界

接收最多 256 KiB、32 项，校验 v1 必填字段、状态、类型和数值边界。允许同版本新增可选字段，但不转发未知字段。证据只接受简单标量，单值字符串上限 2048 字节，每项最多 32 个键、16 KiB；样本最多 32 个。脱敏报告的上游生产者为受信任 subproxy，不接入任意第三方报告服务。

代理指纹对固定字段顺序的 scheme/host/port/username/password 及服务 token 做 HMAC-SHA256；不返回指纹。Redis 只存指纹、预约标识和报告，不保存代理凭据。每代理 `INCR` 加随机 nonce 形成预约标识，Lua 同时比较最新预约与指纹并写报告，所有键 TTL 24 小时。nonce 避免 Redis 清空、逐出或恢复后计数复用。

迟到报告可返回 `saved=false`，不能覆盖新预约。Redis 故障时仍可返回本次结果，但不会声称已保存。调用返回前重读代理并核对指纹；缓存读取时再次核对指纹，作为数据库修改与 Redis 写入间竞态的最终防线。

## 发布边界

I1 不启用生产、不发布镜像。I2 完成前端、测试容器联调和授权代理验收后再单独安排启用。关闭 `subproxy.enabled` 即停止新诊断调用；不删除代理、不修改账号绑定。
