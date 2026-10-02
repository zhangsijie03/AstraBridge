# AstraBridge 星桥源码来源与抽离边界

## 当前固定来源

- 来源分支：https://github.com/ranxi2001/sub2api/tree/production
- 真实提交：`bf9405e4ab58c1be4fc8ec2101371753a016908e`
- 最近发布标签：`v2.9.6`；本版本锁定其后的 production 提交，不把标签名当作提交
- 原协议目录：`backend/internal/service/basispoints/`
- 本地协议目录：`internal/basispoints/`

60 个协议源码、测试与说明文件逐字节保持原样；4 个协议文件（含测试）登记为星桥本地适配；另有 2 个本地文件：明确标注的图片共享代码提取文件 `image_attachment_support.go` 和回归测试 `output_wire_test.go`。3 个 `backend/internal/util/transportdiag/` 文件原样抽离到 `internal/transportdiag/`。下载时核对固定提交目录树的 Git blob 哈希；`python3 scripts/check_upstream.py` 同时校验来源分类、上游锁定文件、本地适配哈希和本地文件哈希。任何未登记文件、分类冲突或哈希漂移都会阻止打包。

`upstream-manifest.json` 记录逐文件来源及哈希。图片辅助提取仍来自此前固定的提取提交，单独标注；`baseline_sha256` 留存 v2.8.11 历史记录。哈希清单不是上游签名。

## 原样沿用的协议实现

请求准备、工具目录提示、code/cmd/custom 原始文本传输、完整调用恢复、schema 校验、整批验证与暂存回放、历史重建、SSE 转换、结构化输出验证及两类原生工具纠错均由 production 原生模块实现。最新提交同时包含多智能体消息归属转换、历史图片处理、工具截图内联、重复工具声明校验、目录驱动工具示例和兼容消息 ID 清理。

格式纠错最多两次；首轮未知工具重生成最多一次，且流式客户端已经收到正文、可见推理摘要或拒绝内容后禁止重新生成整段回答。已知目标的局部工具纠错保持原有边界。工具需整批验证后才下发；普通文字继续流式传输。上游字节持续增长不代表已产生可下发的工具调用。

## 宿主接入对齐

`internal/gateway/` 根据同一提交的宿主源码移植，属于适配代码，不声称逐字节不变：

- `NativeImages.PrepareWithCatalog` 接入可信会话的工具目录继承；上传图片后调用 `Bridge.Reprepare`，保持本请求已校验的工具目录快照。
- 会话身份依次识别 thread-id、turn metadata、window ID、client_metadata，再回退到显式 session/conversation 头或 prompt_cache_key；memory 和无线程元数据的 subagent 请求按原生规则隔离。匿名请求不启用跨请求回放、目录或附件缓存。归一化身份同步到 prompt_cache_key。
- 星桥没有数据库 API key/account ID，作用域使用当前账号、本地 Key、身份和执行道的 JSON 元组 SHA-256，代替上游平台 ID 和 xxhash；这会改变键值，不改变身份优先级及隔离语义。
- StreamWithRepairs 回调沿用原生纠错上下文、同账号及累积历史；已发送请求不因网络失败重放。
- HTTP 400 明确报告 invalid_encrypted_content 时，使用原生恢复函数只移除不透明 reasoning，并在同账号、同路由重试一次。保留用户消息、工具结果和图片；有 compaction 或加密正文时不删历史、不恢复。恢复函数及错误提取器从上游抽离，仅调整包名和文件位置。
- production 的 UpstreamFailure 分类、错误脱敏、取消终态与工具纠错错误传递直接使用原生文件；非流式返回对应错误状态，已开始 SSE 的 HTTP 状态保持 200。response.incomplete 沿用原生不完整响应语义，不另行猜测限流。未知错误安全归类，不回显上游自由文本。
- production 的 openai_excel_bps_ratelimit.go 的账号级 BPS 冷却语义：Retry-After 秒数或 HTTP 日期优先，默认 5 秒，范围 1–7200 秒，并发更新不缩短已有截止时间。星桥不提供平台级冷却配置或账号池；以账号 ID 的 SHA-256 代替平台数据库 ID，停止/启动中转与连接测试共享当前引擎进程内状态，退出清空。
- 移除 0.5.3 自行添加的 SSE 限流观察器，在原生转换后调用 ParseUpstreamFailure 决定是否冷却。图片上传 HTTP 429、生成与纠错失败共用同一账号冷却；不重放已接受的生成、不切换账号。
- 冷却期间在读请求体、上传和生成之前本地拦截，返回 rate_limit_error / basispoints_rate_limited 与 Retry-After。合法上游提示采用原生 Handler 的七天转发上限；本地调度仍上限两小时，较长上游建议不会在拦截响应中被缩短成两小时。
- 星桥诊断扩展：保留本地冷却的来源，明确区分默认回避与上游 Retry-After；只记录固定响应头中通过数值/时长校验的剩余额度、重置窗口与用量百分比，不记录任意头、错误正文或凭据。提示仅为该次响应报告值，不据此判定 BPS 额度耗尽或修改账号状态。纠错请求使用自己的提示，不误用首次成功响应的等待时间。
- UI/导出日志分列实际上游 HTTP 状态和错误分类状态；本地拦截没有上游 HTTP 状态。截止时间表示本地暂停结束，不承诺上游额度已恢复。无提示时仍沿用原生五秒回避，不增加自动探测或重试。
- 按原生发送 `: keepalive` SSE 注释和 X-Accel-Buffering: no。星桥仍先发送一个注释立即建立本地流，随后每 15 秒发送一次。
- HTTP/2 空闲 10 秒主动 PING，5 秒无应答关闭失活连接；使用 Go 标准库 HTTP2Config 实现上游 x/net/http2 的相同参数。明确的 HTTP 代理 H2 EOF/reset/协议故障仅影响后续同代理请求，试用 H1 一分钟，不重放失败请求；取消、普通 EOF、应用错误不触发降级。保留独立 H1/H2 连接池；本地最多记录 256 个代理摘要。
- 等待响应头调整为原生默认 300 秒；附件上传仍为 60 秒。明确 Free 套餐按原生拒绝，未知套餐不会误判。

宿主参考文件路径见 manifest 的 host_adapter_sources。独立桌面仍使用本地监听、支持 gpt-6-astra 与 gpt-6.1-sol 按请求路由、手动启动、8 并发和请求体内存预算、60 分钟总时限及 15 秒写入时限。账号登录与刷新仍由 Codex 管理；不会写入登录文件。

## 2026-10-02 production 对齐

核对 Sub2API `production` 最新提交 `bf9405e4ab58c1be4fc8ec2101371753a016908e` 后，已将 BPS 请求、历史消息、图片校验、附件限制和工具示例等原生文件逐文件同步。此前在 0.5.14 由星桥临时移植的目录驱动工具示例与兼容消息 ID 清理现已成为上游原生实现，因此不再登记为本地补丁。

星桥保留两类本地网关适配，共涉及 4 个上游文件：`stream.go` 在 Responses `output_text` 边界补齐空的 `annotations` / `logprobs` 字段，避免严格客户端丢失文件引用，`stream_repair_boundary_test.go` 同步适配该行为；`request.go` 移植上游宿主的 6.1 Sol 推理强度校验，拒绝 `none` / `minimal`，并由 `basispoints_test.go` 补充回归覆盖。原生附件上传仍由星桥 `internal/gateway` 负责，使用同一 BPS `openai_file_id` 契约、账号头和 60 秒超时。

production 同时新增了服务端公网图片中继、route、普通 Codex 模型目录与 Prism 会话能力。星桥只暴露 `gpt-6-astra` 与 `gpt-6.1-sol` 两个本地路由，继续使用本机原生附件上传，因此这些平台能力不搬入本地网关。

0.5.17 发布后的再次核查确认，上游 production 与当前基线之间新增提交为 0；逐文件证据与平台差异见 [2026-10-02 上游差异核查](upstream-review-2026-10-02.md)。

## 未启用的平台能力

不搬入平台数据库、账号池切换、计费、OAuth 刷新、403 自动停用/恢复探测或 Mihomo 节点调度。星桥只有当前账号和本机代理；401/403/429 不自动切换账号。原生图片数量策略默认 off，保持手动 compact；不启用自动图片压缩、忽略图片或忽略加密正文等可选功能，避免静默丢失上下文。

production 可选的 gpt-image-2 生图通道不在本次两个 BPS 模型的工具范围内。图片输入/工具截图与图片生成是不同能力。不搬入 route.go / route_test.go 或公网 image_relay 服务。

## 验证边界

本地合成请求覆盖原生协议、宿主会话及目录、图片混合输入、有限恢复、错误保留、取消和传输降级。没有使用真实账号请求 BPS，不能保证用户原会话已复现、上游可用或完全不会等待。构建、竞态检测和历史核查见 [v2.9.4 对齐审查](native-v2.9.4-review.md)；本次升级说明见 [0.5.15 发布说明](releases/0.5.15.md)。

## 0.2.1 接入调整

不再给 Codex 桌面写 provider。AiMaMi 使用普通 Bearer API Key 访问本地 Responses 服务，由它管理客户端路由；本地模型列表仅列出用户配置项。账号凭据与客户端 Key 分离，地址与 Key 跨重启持久。原版协议目录仍是同一份 23 个未改动文件。

## 0.3.0 品牌与界面

展示名称更新为 AstraBridge · 星桥，重新整理原生窗口布局、图标和复制反馈。原版协议目录、上游请求处理均未改动。内部 Bundle ID、数据目录和引擎文件名沿用旧值以保证升级兼容。

## 0.4.0 原生图片抽离

新增来源：Sub2API `26b324b44c80929e5f86aeb36e09996423c4a5c0`，2026-09-26 锁定的 production 提交。

- `basispoints/attachments.go`、`attachments_test.go`、`images.go`、`images_test.go` 共 4 文件逐字节使用该提交版本；附件缓存、multipart、内容遍历及校验均来自上游。
- `basispoints/image_attachment_support.go` 仅从该提交的 `image_relay.go` 提取共享图片上限、解码器注册和 `relayImagePayload`。它是明确标注的提取文件，不宣称完整文件与上游相同。未搬入公网图片路由或临时图片落盘。
- `gateway/images.go` 是对该提交 `openai_excel_bps_attachments.go` 的宿主适配，保留附件端点、multipart 字段、账号头、超时、响应字段与无重定向行为，替换了平台账号/HTTP 客户端依赖。
- `gateway/gateway.go` 接入完整预校验、上传、二次协议转换；补充账号/凭据/Key/线程缓存作用域和并发请求体资源预算。没有升级无关工具提示词或结构化输出协议。当前请求体上限改为 64 MiB。
- 旧清单的哈希留在 `baseline_sha256`；当前 26 文件的有效哈希在 `sha256`，图片来源在 `native_images`。不能再将当前版本描述为「23 文件全部等同 v2.8.11」。

真实 OAuth 上传及视觉结果尚未验收。

## 0.5.0 双平台发行

新增 Windows WinForms 界面、平台进程锁和状态目录。macOS 与 Windows 共享 Go 网关；26 个锁定协议文件与 0.4.0 相同。平台构建及发布流程见 [building.md](building.md)。
