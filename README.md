<div align="center">

# AstraBridge · 星桥

**让 AiMaMi 通过本地 Responses 接口连接 BPS。**

macOS / Windows · 手动启停 · 固定 `gpt-6-astra` · 原生图片附件

[下载最新版](https://github.com/zhangsijie03/AstraBridge/releases/latest) · [接入指南](#快速开始) · [图片说明](docs/native-images.md) · [报告问题](https://github.com/zhangsijie03/AstraBridge/issues)

</div>

AstraBridge 是轻量桌面中转工具。它读取本机当前 Codex 的 ChatGPT 登录账号，提供独立的本地地址和 API Key，交给 AiMaMi 管理客户端路由。打开应用后由你手动启动，关闭窗口即停止中转。

```text
Codex / Responses 客户端
          │
          ▼
       AiMaMi
          │  本地 API Key
          ▼
  AstraBridge · 星桥       ← 读取本机 Codex 当前登录
  127.0.0.1:17861
          │  当前账号的访问凭据
          ▼
       BPS 上游
```

> 这是社区协议适配工具，不是 OpenAI 官方产品或“降智修复开关”，不保证提高模型能力。账号能否访问 BPS 取决于上游权限和服务状态。

## 下载与安装

在 [Releases](https://github.com/zhangsijie03/AstraBridge/releases) 下载对应系统的 ZIP，**完整解压后运行**。

| 系统 | 下载文件 | 启动方式 |
| --- | --- | --- |
| macOS 13+，Apple Silicon（M 系列） | `AstraBridge-版本-macOS-arm64.zip` | 将 `AstraBridge.app` 放入“应用程序”后打开 |
| Windows 10/11，x64 | `AstraBridge-版本-Windows-x64.zip` | 打开解压目录中的 `AstraBridge.exe` |

Windows 包含 .NET 运行时，无需另外安装。请保留 EXE 旁边的 `engine` 和运行时文件。当前不提供 Intel Mac、Windows ARM64 或 Linux 桌面发行包。

发行包尚无商业代码签名：macOS 使用本地 ad-hoc 签名，未公证；Windows 未 Authenticode 签名。系统可能提示发布者未知，请先核对仓库来源和 `SHA256SUMS.txt`，按系统提供的方式允许该应用运行，无需关闭系统安全防护。

## 转发日志

**转发日志常驻主窗口下方，每个请求一行**，无需打开另一个窗口。第一列显示结果：绿色 **✓** 为完整完成，红色 **✕** 为失败，**…** 为进行中，**—** 为取消或停止后未确认完成。同一请求的快照更新原行，不重复刷屏；HTTP 200 中携带的流内错误仍显示红叉。

主窗口支持调整大小，日志区域独立滚动。表格显示时间、请求编号、耗时及精简状态；悬停可查看最新完整详情，复制／导出保留完整事件记录。每 5 秒更新本轮上游静默时间、已接收字节、客户端事件和工具调用次数；连接、响应头、图片上传、工具纠错与结束都会记录。

- 上游数据增加但客户端事件不增加：可能在接收推理或等待原生工具校验，不等于断线。
- 长时间没有上游新数据：查看当前阶段及 HTTP 状态，无法仅凭静默时长判断死锁。
- 显示工具已返回客户端：星桥这次转发已经完成，工具执行和下一轮请求由客户端负责。

表格最多保留 1000 个请求，优先淘汰已结束的请求；复制／导出最多保留最近 1000 条完整事件，并非每个历史请求的全部事件。退出后不自动落盘。支持暂停自动滚动并保留选中请求与阅读位置；不记录 Token、API Key、账号、聊天正文、图片内容或工具代码。高负载时日志队列允许丢弃快照，避免诊断阻塞实际转发。

## 快速开始

1. **准备账号。** 在本机 Codex 登录 ChatGPT 账号，确保 `~/.codex/auth.json` 存在；Windows 对应 `%USERPROFILE%\.codex\auth.json`。自定义目录可通过 `CODEX_HOME` 指定。
2. **启动星桥。** 打开应用，点击圆形 **▶** 按钮。每次打开都需要手动开启。此步骤只启动本地服务，不访问 BPS。
3. **复制连接信息。** 界面提供 Base URL、本地 API Key 和固定模型 ID 三个复制按钮。
4. **配置 AiMaMi。** 进入“中转注入 → 添加中转模型 → 自定义中转模型”，填写下表，获取模型列表并选择 `gpt-6-astra`。
5. **启用路由。** 在 AiMaMi 保存并启用该中转模型。保持真实账号登录模式，以便星桥读取当前登录。

| 配置项 | 值 |
| --- | --- |
| 名称 | `AstraBridge`（可自定） |
| Base URL | 复制界面地址，默认 `http://127.0.0.1:17861/v1` |
| API Key | 复制星桥生成的本地 Key；不是 ChatGPT token |
| 协议 | **Responses** |
| 模型 ID | **`gpt-6-astra`**，不可更改 |

上述菜单以 AiMaMi 1.2.6 为参考，其它版本以实际界面为准。星桥不会自动配置 AiMaMi 或给 Codex 写入新 provider。

“测试连接”会向真实 BPS 发送随机校验文案和当前账号访问凭据；聊天和图片请求同样会访问上游。仅打开或启动本地服务不会执行测试。测试完成后本地有 60 秒冷却，请勿连续点击，以免触发上游风控。**显示“运行中”表示本地服务已启动，不等于上游连通性已验证。** 点击 **■** 停止中转，或关闭窗口退出。

## 图片与工具

- 支持 PNG、JPEG、GIF、WebP 的 Base64 图片输入；用户消息中的图片自动上传到 BPS 原生附件接口，无需图床或公网域名。
- function/custom 工具返回的截图按 Sub2API v2.9.4 原生规则校验并保留内联格式；支持上游接受的 HTTPS 图片引用和有效附件 ID。
- 每张最多 20 MiB、单请求最多 20 张、解码后合计最多 32 MiB；单图最多 64 Mi 像素。
- 工具继续由客户端执行；星桥负责协议转换，不在本机执行模型返回的代码。
- 不读取请求里指定的本地文件路径，也不提供图片生成。

详见 [图片支持与限制](docs/native-images.md)。模型能否正确理解图片仍取决于上游账号和模型。

## 账号、隐私与网络

- 仅监听 `127.0.0.1`，使用随机本地 Key 校验请求，拒绝外部 Host 和浏览器 Origin。
- 每个请求重新读取当前账号；登录、切换和刷新由 Codex / AiMaMi 负责。星桥不保存或刷新 refresh token，不修改 `auth.json`。
- 当前账号 access token 仅用于固定的 BPS 上游；本地 Key 不发送给 BPS，账号 token 不返回给客户端。
- 不保存聊天正文、原图文件、登录 token 或上游原始错误正文。附件和回放缓存仅存在内存中。
- BPS 返回 HTTP 429 或原生分类器识别到流内限流时，按当前账号进入本地冷却：优先采用有效的 `Retry-After`，否则暂缓 5 秒（沿用原生默认值），范围 1–7200 秒。冷却期间直接返回明确的 429，日志显示“本地冷却拦截”；结束后可手动重试，不代表上游额度已恢复。该状态跨同一引擎的停止/启动和测试连接共享，退出应用后清空，不修改账号文件。
- v2.9.4 原生区分流内限流、鉴权、权限、请求错误和上游取消；日志区分实际 HTTP 与错误分类状态，并记录上游有效的等待、额度及重置提示。没有提示时明确标注未知，不把默认 5 秒当作额度恢复时间。
- 不对普通网络失败、鉴权拒绝或限流自动重放，不自动切换账号、不跟随上游重定向。仅在原生规则允许时进行有限工具纠错，或对明确的加密推理拒绝进行一次同账号恢复。
- 可通过 `HTTPS_PROXY` 配置代理。macOS 界面还会读取系统静态 HTTPS 代理；Windows 的代理行为见 [构建与运行](docs/building.md)。

## 配置位置

| 内容 | macOS | Windows |
| --- | --- | --- |
| 本地地址和 Key | `~/Library/Application Support/BPS Local/relay.json` | `%LOCALAPPDATA%\AstraBridge\relay.json` |
| Codex 当前账号 | `~/.codex/auth.json` | `%USERPROFILE%\.codex\auth.json` |

macOS 沿用旧版目录，以保留已有地址和 Key。macOS 配置文件权限为 `0600`；Windows 使用当前用户目录继承的 ACL，不能用 POSIX 权限位描述其访问控制。不要共享 `relay.json` 或账号文件。

端口冲突时先退出旧工具；需要修改端口，可退出星桥后编辑 `relay.json` 的 `port`，随后同步更新 AiMaMi。损坏的配置会报错，不会悄悄重建 Key。旧版 BPS Local 遗留的受管 provider 备份仅在安全校验通过后恢复，用户修改过的配置不会被强行覆盖。

## 能力边界与排查

| 现象 | 说明 / 处理 |
| --- | --- |
| 未找到账号、账号过期 | 重新在 Codex 登录，确认 `CODEX_HOME` 与账号文件位置 |
| 端口无法监听 | 退出旧版或重复窗口，检查 `relay.json` |
| 本地启动成功，上游返回错误 | 本地监听不代表 BPS 权限已通过；检查账号、代理和上游状态 |
| 其它模型不能使用 | 仅支持 `gpt-6-astra`，网关会拒绝其它模型 |
| 上游 404 / model_not_found | 本机已校验固定模型，上游当前无法提供该模型；不自动重放或切换模型。稍后重试，持续出现需核实 BPS 模型权限；本地不能恢复上游权限 |
| idle timeout waiting for SSE | 已在原生注释心跳外补充完整保活事件，适配客户端的 SSE 事件空闲计时。若中间路由丢弃未知事件或在收到响应头前超时，仍需进一步检查该段链路 |
| 自动标题或结构化输出失败 | 采用 Sub2API 原生提示与本地 JSON/Schema 校验；校验失败会报错，不提供上游约束解码 |
| 重启后继续旧聊天异常 | 回放缓存已清空，建议新建聊天 |

提供 `/v1/models`、`/v1/responses` 和 `/v1/responses/compact`；不提供 Chat Completions 转换。仅带 `previous_response_id` 的增量历史不受支持。推理档位沿用上游转换：`max/ultra → xhigh`，`none/minimal → low`。收到上游成功响应头后，流式连接沿用原生 15 秒心跳间隔，并补充 `keepalive` 数据事件；它不包含模型文本，不计作上游进度，属于星桥兼容调整。HTTP/2 连接空闲 10 秒后发送健康探测，5 秒无应答关闭失活连接；20 分钟总超时仍生效。针对 Sub2API v2.9.6 的核查依据、调整边界见 [连接与日志审查](docs/connection-and-compact-log-review.md)。

报告问题时请附系统版本、星桥版本、操作步骤和脱敏错误提示。**不要上传 auth.json、relay.json、完整聊天、token 或 Key。**

## 源码与构建

桌面层：macOS Swift / AppKit，Windows C# / WinForms。两者共用 Go 引擎及同一套 Responses、图片和账号处理代码。

```text
app/                 macOS 桌面界面
windows/             Windows 桌面界面
cmd/bps-local/       引擎入口与进程控制
internal/gateway/    本地 HTTP 网关与原生图片上传
internal/basispoints/锁定的上游协议实现
scripts/             构建、来源校验与离线检查
```

详见 [本地构建与自动发布](docs/building.md)。Go 依赖已固定并纳入 `vendor`；构建会校验上游文件哈希。Release 同时提供两个平台 ZIP 和 SHA-256 校验文件，源代码使用对应版本 tag。

## 上游来源与许可

文本、工具及图片协议同步 [Sub2API v2.9.4](https://github.com/ranxi2001/sub2api/releases/tag/v2.9.4)，固定提交 `7dd10bfe4b635f226f0ddfa52cc65797697272d8`。沿用原生工具传输、批次校验及有限纠错，星桥网关接入原生工具目录继承、会话隔离、图片预校验及单次加密推理恢复。

当前锁定 **62 个协议文件：61 个逐字节一致的 v2.9.4 文件、1 个明确标注的图片共享代码提取文件**。另原样沿用并校验 3 个传输诊断文件。桌面 UI、账号读取和独立网关是本项目适配代码，并非整个 Sub2API 平台的原封不动复制。完整边界见 [来源说明](docs/source-provenance.md) 和 [哈希清单](upstream-manifest.json)。

AstraBridge 原创代码按 [GPL-3.0-only](LICENSE) 开源；第三方代码保留各自许可证及归属，见 [NOTICE](NOTICE) 与 [licenses](licenses)。感谢 Sub2API 与原协议作者 [hloolx/codex2api](https://github.com/hloolx/codex2api)。

验证以离线协议测试、本机模拟上游和打包检查为主；没有使用真实账号完成 BPS 连通或视觉结果验收。
