# AstraBridge 星桥源码来源与抽离边界

## 固定来源

- 项目：https://github.com/ranxi2001/sub2api
- 基线发布：https://github.com/ranxi2001/sub2api/releases/tag/v2.8.11
- 完整提交：`3bf31dedc335318238fbf29e376e10f9329d3eb5`
- 原目录：`backend/internal/service/basispoints/`
- 本地目录：`internal/basispoints/`

2026-09-26 从 GitHub 固定提交重新获取该目录全部 23 个文件，包括协议源码、原测试和 NOTICE。该版本在 0.3.x 中逐字节保持原样。0.4.0 为用户授权的原生图片功能更新图片相关文件，其余 21 个文件继续保持该版本原样；当前有效来源与 SHA-256 均记录在根目录 `upstream-manifest.json`。

执行 `python3 scripts/check_upstream.py` 可离线验证当前源码与这份锁定清单一致。构建脚本也强制运行此校验，任一文件改动或新增都将阻止打包。需要核验清单来源时，可从上述固定提交下载文件独立计算 SHA-256；清单不是上游签名。

## 使用原版的部分

原样使用 `basispoints.Prepare`、`Bridge.Stream` 和 `ReplayCache`，包括上游地址、请求体白名单、模型/推理档位转换、原有 developer 工具目录提示词、run_officejs 工具转换、历史回放、工具 item ID 兼容、SSE 协议转换等。工具目录中的提示词来自上游文件，不是本地工具自行设计。

原版明确拒绝结构化输出。0.1.1 本地增加的 schema 提示词、JSON 校验及延迟消息事件已经撤掉。不能用“修好了标题 400”描述 0.1.2；这里只准确解释原版限制，保留其拒绝行为。

## 为独立运行编写的外壳

以下是本地工具代码，不冒称为 Sub2API 原版源码：

- `app/` 与 `windows/`：Swift/AppKit 和 C#/WinForms 窗口、启停按钮、脱敏状态，以及供 AiMaMi 使用的地址/Key/模型 ID 复制按钮。
- `internal/identity/`：读取 Codex 当前账号；原平台使用数据库账号及自己的 OAuth 生命周期。
- `internal/relayconfig/`：持久化本地端口和中转 API Key。`internal/localconfig/` 只保留旧版备份的迁移恢复能力，新版不调用配置接管。
- `internal/gateway/` 和 `cmd/`：127.0.0.1 HTTP 服务、随机访问密钥、启动/退出、网络连接和状态展示。

转发外壳参照原项目 `backend/internal/service/openai_excel_bps.go`，使用相同 BPS 地址、上游请求头和协议模块。原平台的 Gin 路由、数据库、账号池、计费、调度、Ops 和共享 HTTP 客户端没有搬入。它不是整个 Sub2API 服务的原封不动独立二进制。

需明确保留的宿主差异：客户端使用独立中转 API Key，由服务端每请求读取当前账号；不做模型映射和自动账号切换；按本地账号与明确线程 ID 隔离回放缓存，无明确线程时隔离每次请求；并发上限 8、请求体上限 64 MiB、请求头等待 45 秒、总时限 20 分钟；本地转发按完整 SSE 帧写出，15 秒心跳，发送失败/客户端取消单独归类，错误正文采用无敏感内容的提示。本地 shell 不向原版协议追加兼容提示词或伪造模型结果。

原平台已经有 15 秒心跳及客户端断开处理。早期本地外壳遗漏这些处理导致误报，不能归咎于原版协议。当前修复保留在宿主层；文本与工具核心保持原样，图片扩展来源见下文。

## 验证范围

源码逐文件哈希校验、原版协议测试、本地模拟上游及配置恢复测试、原生应用构建。未进行真实账号凭据联调，不能保证上游账号可用性或模型质量提高。

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
