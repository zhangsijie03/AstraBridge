# 构建与发布

根目录 `VERSION` 是发布版本号。应用默认不启动中转、不发送探测；下面的构建和检查不需要真实账号。

## macOS

需要 macOS 13+、Go 1.27+、Python 3、Xcode Command Line Tools。

```sh
python3 scripts/check_upstream.py
go test -mod=vendor -race -count=1 ./...
go vet -mod=vendor ./...
python3 scripts/test_ui_contract.py
python3 scripts/test_embedded_logs.py
bash scripts/build.sh
python3 scripts/smoke.py
codesign --verify --deep --strict dist/AstraBridge.app
VERSION=$(cat VERSION)
ditto -c -k --sequesterRsrc --keepParent dist/AstraBridge.app "dist/AstraBridge-${VERSION}-macOS-arm64.zip"
```

在 Apple Silicon 构建 arm64 包。可用 `GO_BIN` 指定 Go 工具，`BPS_APP_PATH` 指定应用输出目录。构建只做 ad-hoc 签名，不含 Apple 公证。

## Windows

需要 Windows 10/11 x64、Go 1.27+、Python 3、.NET 8 SDK；发布后的应用自带运行时。

```powershell
python scripts/check_upstream.py
go test -mod=vendor -race -count=1 ./...
go vet -mod=vendor ./...
pwsh -File scripts/build-windows.ps1
```

Go race detector 需要受支持的 C 编译器；GitHub Windows runner 已提供。如本地没有，可先执行 `go test -mod=vendor -count=1 ./...`，以 CI race 结果作补充。

Windows 原生界面使用 WinForms，通过 UTF-8 与 Go JSON 行协议通信。环境变量 `HTTPS_PROXY` / `HTTP_PROXY` 由子进程继承。Windows 版不自动解析系统代理或 PAC；需要代理时请在启动应用前设置 `HTTPS_PROXY`。

Windows UI 的 `--preview` 使用合成数据。`--smoke-test --smoke-report <文件>` 在独立临时目录启动引擎，验证初始停止状态、契约和退出，不读取真实账号或发送上游请求。

应用内更新依赖发行包名称与版本严格匹配：macOS 使用 AstraBridge-版本-macOS-arm64.zip，Windows 使用 AstraBridge-版本-Windows-x64.zip，并且必须同时发布 SHA256SUMS.txt。若只上传单个平台包，另一平台会安全地提示发行包不完整并继续使用当前版本。

## 引擎选项

- `--codex-home <目录>`：指定 `auth.json` / `config.toml` 所在目录，默认使用 `CODEX_HOME` 或用户目录下 `.codex`。
- `--state-dir <目录>`：覆盖当前平台的状态目录。
- `--restore`：仅尝试安全恢复旧版 BPS Local 的备份。
- `--probe`：**会向真实 BPS 发送当前账号凭据和随机校验文案**，不属于离线构建验证。

客户端通过 stdin JSON 行发送 `start`、`stop`、`probe`、`quit` 操作；stdout 返回状态和请求结果事件。stdin EOF 会取消等待中的请求并关闭本地监听。进程锁防止同一状态目录被多个引擎并行使用。

## 自动构建与发布

GitHub Actions 对 `main` 的 push 和 pull request 构建两个平台，并执行协议来源校验、Go 测试、打包和离线 smoke 检查。Windows UI 必须在 Windows runner 运行检查，不能把 macOS 上的交叉编译当成 Windows 实机运行。

发布步骤：

1. 更新 `VERSION` 和 `docs/releases/<版本>.md`，完成 review。
2. 推送与版本匹配的 tag，例如 `v0.5.0`。
3. Release workflow 必须等两个平台检查成功后，再发布 ZIP 和 `SHA256SUMS.txt`。

不要提交 `.build`、`dist`、`bin`、`obj`、账号文件或运行时配置。产物内包括版本文件、README、来源清单及许可证，对应 tag 保留完整可构建源码。
