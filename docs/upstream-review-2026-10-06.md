# Sub2API BPS 对齐核查（2026-10-06）

## 对齐结果

- 上游最新 Release：[v2.9.10](https://github.com/ranxi2001/sub2api/releases/tag/v2.9.10)，北京时间 2026-10-06 02:46 发布。
- 最新 production：[`23eed543bbf6de4164b55862c2a0f3d84f662b75`](https://github.com/ranxi2001/sub2api/commit/23eed543bbf6de4164b55862c2a0f3d84f662b75)，北京时间 2026-10-06 12:59 提交。
- 原基线：`bf9405e4ab58c1be4fc8ec2101371753a016908e`；[两提交间差异](https://github.com/ranxi2001/sub2api/compare/bf9405e4ab58c1be4fc8ec2101371753a016908e...23eed543bbf6de4164b55862c2a0f3d84f662b75)为 156 个提交（含合并提交），完整目录树显示 628 个文件变更。

已将星桥协议、原生图片与传输诊断的来源引用推进到最新 production。BPS 核心源码没有变化，现有 0.5.17 已具有相同协议实现；本次不调整应用版本、不发新安装包。

## 核查方法

GitHub compare API 本次仅返回前 300 个文件，不能据此认定服务层无改动。核查使用两个提交的完整递归 Git tree（均未截断），比较所有文件路径和 Git blob 哈希，并读取发生变化的相关宿主文件原文。

- `backend/internal/service/basispoints/` 的全部 71 个文件未变；64 个已引入、7 个明确排除，没有新增或遗漏文件。
- 60 个原样协议文件和 3 个原样传输诊断文件逐个验证本地 SHA-256 与最新上游 Git blob。
- 4 个本地补丁对应的原始上游文件逐个验证 SHA-256 与最新 Git blob，保留 Responses 文本字段兼容及 Sol 推理强度校验。
- `openai_excel_bps_attachments.go`、加密历史恢复、BPS 限流、会话作用域、模型目录与对应 BPS 测试均无上游变更。
- 新旧宿主文件通过固定提交下载，并校验返回原文的 Git blob 后进行完整差异比较。

## 宿主变化与迁移判断

| 文件或能力 | 实际变化 | 星桥处理 |
| --- | --- | --- |
| `openai_excel_bps.go`、`openai_excel_bps_transport.go` | 在生成、纠错、加密恢复响应及失败终态增加 `observeQualityResponse` / `observeQualityStatus` | 回调接入平台质量检测，不更改 BPS 协议；不移植数据库账号隔离、Redis 队列和后台测试 |
| `quality_5xx_trigger.go` | OAuth 账号 5xx 触发即时保护及质量检测计划，依赖持久化仓库、Redis 和测试 Worker | 星桥使用当前本机账号并记录请求错误，不自动消费额度做额外质量测试 |
| `http_upstream.go` | 区域出口、Astra 平台路由包装、代理/连接池日志哈希脱敏和 Grok 请求头调整 | 区域出口明确排除 `HTTPUpstreamProfileExcelBPS`；星桥已有代理摘要及错误脱敏，继续沿用独立本机传输 |
| `openai_gateway_scheduling.go` | 平台组合别名路由检查账号是否明确拥有模型映射 | 星桥按请求直接接受两个明确模型 ID，没有组合别名或账号池 |
| `ratelimit_service.go` | Anthropic safeguard 会话拒绝、Grok 模型冷却恢复、平台质量检测字段 | 与 BPS 冷却无关，不引入其他供应商的状态管理 |

上游同期的 Prism Sol 6.1 工具桥接、并行浏览器页面、图片解析、渠道健康统计和采票调度属于其他通道或平台功能，不是新的 BPS 能力。它们不能解除 BPS 返回的 `basispoints_model_access_changed` 权限拒绝。

## 保留的行为与验证范围

保留按每次请求的 `model` 在 Astra / 6.1 Sol 间路由、原生图片附件、工具目录继承、会话隔离、文件引用兼容、有限纠错与错误保留。客户端无需配合星桥切换全局模型；本机代理地址不写入应用默认配置。

验证包括来源清单校验、全量 Go 竞态测试、`go vet` 和 `git diff --check`。本次没有运行时改动，不重复打包，也未使用真实账号向 BPS 发起测试请求；不将离线验证描述为上游账号权限或回答质量已通过验收。
