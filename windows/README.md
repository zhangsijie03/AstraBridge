# AstraBridge Windows

Windows 10/11 x64 原生桌面版，.NET 8 WinForms，自包含发布；解压后双击 `AstraBridge.exe`，无需安装 .NET，不弹出终端。`engine/bps-local.exe` 必须与主程序一起保留。

每次打开需手动启动。固定使用 `gpt-6-astra`，在 AiMaMi 中填入 Base URL、本地 API Key 和模型 ID，协议选择 Responses。只有点击测试连接或发起聊天才访问上游。API Key 是本机中转密钥，窗口不展示账号令牌。

数据默认保存到 `%LOCALAPPDATA%\AstraBridge`。账号默认读取 `%USERPROFILE%\.codex`，支持启动进程继承 `CODEX_HOME`。代理继承 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`；暂不读取 Windows PAC 或系统代理。环境变量变更后重新打开程序。

## 构建与验证

需要 Go 和 .NET 8 SDK（构建机需要联网还原 Windows Desktop 引用与运行时包）。在仓库根目录执行：

```powershell
./scripts/build-windows.ps1
```

版本读取根目录 `VERSION`，输出 `dist/AstraBridge-<版本>-Windows-x64.zip`。脚本在 Windows 构建机默认运行离线冒烟验证，报告为 `.build/windows-smoke.json`；只有显式交叉构建时才使用 `-SkipSmokeTest`。可通过参数 `-GoBin`、`-DotnetBin` 或环境变量 `GO_BIN`、`DOTNET_BIN` 指定工具。

```powershell
./AstraBridge.exe --preview
Start-Process ./AstraBridge.exe -ArgumentList '--smoke-test', '--smoke-report', "$PWD/smoke.json" -Wait -PassThru
```

`--preview` 仅使用合成地址、密钥和脱敏账号，不启动引擎。`--smoke-test` 验证 UI 的手动启动状态、JSON 事件契约，并在临时隔离目录启动引擎验证 idle/关闭输入后的退出；不发送 start/probe、不读取真实账号、不访问上游。应同时检查进程退出码与 JSON 报告中的 `success`。

退出窗口先关闭引擎 stdin 取消请求，最多等待 5 秒，再终止子进程树并等待 3 秒。异常退出和通信失败在窗口显示。测试连接失败保留原本运行的服务状态，方便直接停止。

## 验收范围

CI 离线检查不代表真实账号连通性，也不代替 Windows 实机在 100% / 150% / 200% DPI 下的视觉、剪贴板及屏幕阅读器验收。发布包未签署 Authenticode，Windows 可能显示未知发布者提示。
