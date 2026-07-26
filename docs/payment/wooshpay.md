# WooshPay 托管收银台配置

WooshPay 集成用于一次性 CNY 充值。Sub2API 创建托管 Checkout Session，由 WooshPay 收银台提供支付宝和银联两种支付方式。

## 固定参数

| 项目 | 值 |
| --- | --- |
| Provider key | `wooshpay` |
| 测试 API Base | `https://apitest.wooshpay.com` |
| 生产 API Base | `https://api.wooshpay.com` |
| Webhook | `https://<host>/api/v1/payment/webhook/wooshpay` |
| Webhook 事件 | `payment_intent.succeeded` |
| 收银台支付方式 | `alipay`, `unionpay` |
| 币种 | `CNY` |

## 配置步骤

1. 在管理后台的支付设置中启用 `wooshpay`，再创建 WooshPay 服务商实例。
2. 测试密钥选择测试 API Base；生产密钥选择生产 API Base。系统只接受上表中的两个 HTTPS 地址。
3. 填写 Secret Key 和 Webhook Secret。两者只能保存在服务端配置中，禁止写入前端代码、日志、工单或截图。
4. 在 WooshPay 后台创建 Webhook 端点，地址使用上表路径，并只需订阅 `payment_intent.succeeded`。
5. Success URL 和 Cancel URL 可以留空；留空时系统使用当前订单的支付结果页。

浏览器跳转到 Success URL 只表示用户返回站点，不是支付成功凭据。余额仅在服务端验证 Webhook 签名，并完成订单号、PaymentIntent、金额和币种对账后入账。

## 上线检查

- 先使用测试 API Base 和测试密钥完成全流程。
- 使用平台允许的最小安全金额创建一笔 CNY 充值。
- 分别验证支付宝和银联能够打开托管收银台。
- 确认成功事件只入账一次；重复投递同一事件不会重复充值。
- 验证错误签名、错误金额、非 CNY 币种和冲突订单号均不会入账。
- 切换生产环境时同时替换 API Base、Secret Key 和 Webhook Secret，并重新检查 Webhook 地址。

当前集成不提供 WooshPay 退款 API、周期性订阅或 Direct PaymentIntent。退款需要在 WooshPay 后台人工处理，并同步按现有运营流程处理 Sub2API 订单。
