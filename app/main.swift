import AppKit
import CFNetwork
import Darwin

private enum EnginePhase: String, Decodable { case idle, testing, enabled, stopped, error }
private enum EngineAction: String, Encodable { case start, stop, probe, quit }
private let fixedModelID = "gpt-6-astra"
private enum GatewayFailureCode {
    static let rateLimited = "basispoints_rate_limited"
    static let modelUnavailable = "basispoints_model_unavailable"
}
private enum Product {
    static let name = "AstraBridge"
    static let chineseName = "星桥"
    static let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
    // 沿用旧数据位置与 Bundle ID，升级时保留地址、Key、偏好及单实例锁。
    static let stateDirectory = "Library/Application Support/BPS Local"
    private struct Source: Decodable { let tag: String; let commit: String }
    private static let source: Source? = {
        guard let url = Bundle.main.url(forResource: "upstream-manifest", withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(Source.self, from: data)
    }()
    // 与发行包内已校验的清单共用来源，避免协议升级后界面仍显示旧提交。
    static let sourceDescription = source.map { "Sub2API \($0.tag)，固定提交 \($0.commit.prefix(7))" } ?? "Sub2API（来源清单不可读取）"
    static let sourceURL = source.flatMap { URL(string: "https://github.com/ranxi2001/sub2api/blob/\($0.commit)/docs/excel-bps.md") }
}

private struct GatewayResult: Decodable {
    let success: Bool
    let cancelled: Bool?
    let message: String?
    let model: String?
    let effort: String?
    let code: String?
    let status: Int?
    let account: String?
    let warnings: [String]?
}
private struct EngineEvent: Decodable {
    private enum CodingKeys: String, CodingKey {
        case type, phase, message, account, model, port, requests, backup, result, trace
        case baseURL = "base_url"
        case apiKey = "api_key"
    }
    let type: String
    let phase: EnginePhase?
    let message: String?
    let account: String?
    let model: String?
    let port: Int?
    let baseURL: String?
    let apiKey: String?
    let requests: Int
    let backup: String?
    let result: GatewayResult?
    let trace: TransferTrace?
}
private struct Command: Encodable { let action: EngineAction }

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var window: NSWindow!
    private let transferLog = TransferLogView()
    private var process: Process?
    private let input = Pipe()
    private let output = Pipe()
    private var outputBuffer = Data()
    private var closing = false
    private var terminationFallback: DispatchWorkItem?
    private var busy = false
    private var active = false
    private let preview = CommandLine.arguments.contains("--preview") || Bundle.main.object(forInfoDictionaryKey: "AstraBridgePreview") as? Bool == true
    private let statusLabel = NSTextField(labelWithString: "正在准备")
    private let statusIcon = NSImageView()
    private let previewLabel = NSTextField(labelWithString: "离线预览 · 示例数据")
    private let detailLabel = NSTextField(wrappingLabelWithString: "正在读取本地配置…")
    private let accountLabel = NSTextField(labelWithString: "等待识别")
    private let routeLabel = NSTextField(labelWithString: "正在读取本地地址…")
    private var relayBaseURL = ""
    private var relayAPIKey = ""
    private var copyURLButton: CopyButton!
    private var copyKeyButton: CopyButton!
    private var copyModelButton: CopyButton!
    private let keyStateLabel = NSTextField(labelWithString: "等待服务启动")
    private let countLabel = NSTextField(labelWithString: "0")
    private var startButton: RelayPowerButton!
    private let startActionLabel = NSTextField(labelWithString: "启动中转")
    private var testButton: NSButton!
    private var updateButton: NSButton!
    private let spinner = NSProgressIndicator()
    private let dataDirectory = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(Product.stateDirectory)
    private let updateChecker = UpdateChecker()
    private var updateInProgress = false
    private var updateCheckInProgress = false
    private var updaterProcess: Process?

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildWindow()
        NSApp.activate(ignoringOtherApps: true)
        if preview {
            // 预览使用合成数据并绕过引擎，支持布局和复制操作验收。
            relayBaseURL = "http://127.0.0.1:17861/v1"
            relayAPIKey = "preview-local-key-not-a-credential"
            routeLabel.stringValue = relayBaseURL
            keyStateLabel.stringValue = "•••• •••• •••• ••••"
            accountLabel.stringValue = "demo•••@example.com"
            statusLabel.stringValue = "准备就绪"
            detailLabel.stringValue = "启动本地中转后，即可通过 AiMaMi 使用固定模型。"
            previewLabel.isHidden = false
            updateStatusIcon("pause.circle.fill", color: .secondaryLabelColor)
            setBusy(false)
            return
        }
        launchEngine()
        checkForUpdates(manual: false)
    }
    private func label(_ text: String, size: CGFloat, weight: NSFont.Weight = .regular, secondary: Bool = false) -> NSTextField {
        let field = NSTextField(wrappingLabelWithString: text)
        field.font = .systemFont(ofSize: size, weight: weight)
        field.textColor = secondary ? .secondaryLabelColor : .labelColor
        return field
    }
    private func stack(_ views: [NSView], vertical: Bool = false, spacing: CGFloat = 12) -> NSStackView {
        let view = NSStackView(views: views)
        view.orientation = vertical ? .vertical : .horizontal
        view.alignment = vertical ? .leading : .centerY
        view.spacing = spacing
        view.translatesAutoresizingMaskIntoConstraints = false
        return view
    }
    private func spacer() -> NSView {
        let view = NSView()
        view.setContentHuggingPriority(.init(1), for: .horizontal)
        return view
    }
    private func separator() -> NSBox {
        let line = NSBox(); line.boxType = .separator
        return line
    }
    private func symbol(_ name: String, size: CGFloat, color: NSColor) -> NSImageView {
        let image = NSImageView()
        image.image = NSImage(systemSymbolName: name, accessibilityDescription: nil)
        image.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: size, weight: .medium)
        image.contentTintColor = color
        return image
    }
    private func updateStatusIcon(_ name: String, color: NSColor) {
        statusIcon.image = NSImage(systemSymbolName: name, accessibilityDescription: nil)
        statusIcon.contentTintColor = color
    }
    private func connectionRow(_ title: String, value: NSTextField, note: String? = nil, copy: CopyButton) -> NSView {
        let heading = stack([label(title, size: 11, weight: .medium, secondary: true)])
        if let note = note { heading.addArrangedSubview(label(note, size: 10, secondary: true)) }
        value.font = .monospacedSystemFont(ofSize: 14, weight: .medium)
        value.lineBreakMode = .byTruncatingMiddle
        value.isSelectable = true
        value.setContentCompressionResistancePriority(.init(740), for: .horizontal)
        let text = stack([heading, value], vertical: true, spacing: 6)
        let row = stack([text, spacer(), copy], spacing: 16)
        row.edgeInsets = NSEdgeInsets(top: 0, left: 20, bottom: 0, right: 16)
        row.heightAnchor.constraint(equalToConstant: InterfaceStyle.rowHeight).isActive = true
        return row
    }
    private func buildWindow() {
        if preview, let mode = Bundle.main.object(forInfoDictionaryKey: "AstraBridgePreviewAppearance") as? String {
            NSApp.appearance = NSAppearance(named: mode == "light" ? .aqua : .darkAqua)
        }
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 700, height: 844),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.contentMinSize = NSSize(width: 700, height: 760)
        window.title = "\(Product.name) · \(Product.chineseName)"
        window.titlebarAppearsTransparent = true
        window.backgroundColor = .windowBackgroundColor
        window.center(); window.delegate = self
        window.isReleasedWhenClosed = false
        guard let content = window.contentView else { return }
        let root = stack([], vertical: true, spacing: 0)
        content.addSubview(root)
        NSLayoutConstraint.activate([
            root.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: InterfaceStyle.pageInset),
            root.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -InterfaceStyle.pageInset),
            root.topAnchor.constraint(equalTo: content.topAnchor, constant: 16),
            root.bottomAnchor.constraint(equalTo: content.bottomAnchor, constant: -20)
        ])
        func add(_ view: NSView, gap: CGFloat = 0) {
            root.addArrangedSubview(view)
            view.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
            root.setCustomSpacing(gap, after: view)
        }

        let mark = symbol("point.3.connected.trianglepath.dotted", size: 30, color: InterfaceStyle.accent)
        mark.widthAnchor.constraint(equalToConstant: 42).isActive = true
        let branding = stack([label(Product.name, size: 25, weight: .semibold),
                              label("星桥 · 为 AiMaMi 连接 BPS", size: 12, secondary: true)], vertical: true, spacing: 4)
        previewLabel.font = .systemFont(ofSize: 10)
        previewLabel.textColor = .secondaryLabelColor
        previewLabel.isHidden = true
        add(stack([mark, branding, spacer(), previewLabel]), gap: 22)

        statusIcon.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: 19, weight: .medium)
        statusIcon.widthAnchor.constraint(equalToConstant: 24).isActive = true
        updateStatusIcon("circle.dotted", color: .secondaryLabelColor)
        statusLabel.font = .systemFont(ofSize: 17, weight: .semibold)
        spinner.style = .spinning; spinner.controlSize = .small
        spinner.isDisplayedWhenStopped = false
        let statusHeading = stack([statusIcon, statusLabel, spinner], spacing: 8)
        detailLabel.font = .systemFont(ofSize: 12)
        detailLabel.textColor = .secondaryLabelColor
        detailLabel.maximumNumberOfLines = 3
        detailLabel.preferredMaxLayoutWidth = 405
        let statusText = stack([statusHeading, detailLabel], vertical: true, spacing: 7)
        statusText.widthAnchor.constraint(equalToConstant: 408).isActive = true
        startButton = RelayPowerButton(title: "启动中转", target: self, action: #selector(toggleRelay))
        startButton.bezelStyle = .circular
        startButton.isBordered = false
        startButton.focusRingType = .exterior
        startButton.imagePosition = .imageOnly
        startButton.bezelColor = InterfaceStyle.accent
        startButton.keyEquivalent = "\r"
        startButton.widthAnchor.constraint(equalToConstant: 56).isActive = true
        startButton.heightAnchor.constraint(equalToConstant: 56).isActive = true
        startActionLabel.font = .systemFont(ofSize: 11, weight: .medium)
        startActionLabel.textColor = .secondaryLabelColor
        let relayAction = stack([startButton, startActionLabel], vertical: true, spacing: 4)
        relayAction.alignment = .centerX
        relayAction.widthAnchor.constraint(equalToConstant: 112).isActive = true
        let statusArea = stack([statusText, spacer(), relayAction], spacing: 16)
        statusArea.heightAnchor.constraint(equalToConstant: 80).isActive = true
        add(statusArea, gap: 18)

        // 测试归属于连接配置；与服务启停分开，避免被误认作次级说明文字。
        testButton = NSButton(title: "测试连接", target: self, action: #selector(test))
        testButton.bezelStyle = .rounded
        testButton.controlSize = .regular
        testButton.font = .systemFont(ofSize: 12, weight: .medium)
        testButton.image = NSImage(systemSymbolName: "network", accessibilityDescription: nil)
        testButton.imagePosition = .imageLeading
        testButton.toolTip = "使用当前登录账号向 BPS 发送一次测试请求；每次测试后冷却 60 秒"
        testButton.setAccessibilityLabel("测试 BPS 连接")
        testButton.widthAnchor.constraint(equalToConstant: 112).isActive = true
        testButton.heightAnchor.constraint(equalToConstant: 30).isActive = true
        add(stack([label("连接配置", size: 13, weight: .semibold),
                   label("Responses · 仅限本机", size: 11, secondary: true),
                   spacer(), testButton]), gap: 10)
        copyURLButton = CopyButton(label: "复制 Base URL", target: self, action: #selector(copyURL))
        copyKeyButton = CopyButton(label: "复制 API Key", target: self, action: #selector(copyKey))
        copyModelButton = CopyButton(label: "复制模型 ID", target: self, action: #selector(copyModel))
        let modelValue = NSTextField(labelWithString: fixedModelID)
        let rows = stack([], vertical: true, spacing: 0)
        let surface = SurfaceView()
        surface.translatesAutoresizingMaskIntoConstraints = false
        surface.addSubview(rows)
        NSLayoutConstraint.activate([
            rows.leadingAnchor.constraint(equalTo: surface.leadingAnchor),
            rows.trailingAnchor.constraint(equalTo: surface.trailingAnchor),
            rows.topAnchor.constraint(equalTo: surface.topAnchor),
            rows.bottomAnchor.constraint(equalTo: surface.bottomAnchor)
        ])
        let fields = [
            connectionRow("Base URL", value: routeLabel, copy: copyURLButton),
            connectionRow("API Key", value: keyStateLabel, note: "本地中转密钥", copy: copyKeyButton),
            connectionRow("模型 ID", value: modelValue, note: "固定模型 · 原生图片上传", copy: copyModelButton)
        ]
        for (index, row) in fields.enumerated() {
            if index > 0 {
                let line = separator()
                rows.addArrangedSubview(line)
                line.widthAnchor.constraint(equalTo: rows.widthAnchor, constant: -40).isActive = true
                line.leadingAnchor.constraint(equalTo: rows.leadingAnchor, constant: 20).isActive = true
            }
            rows.addArrangedSubview(row)
            row.widthAnchor.constraint(equalTo: rows.widthAnchor).isActive = true
        }
        add(surface, gap: 17)

        accountLabel.font = .systemFont(ofSize: 12, weight: .medium)
        accountLabel.lineBreakMode = .byTruncatingMiddle
        accountLabel.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        countLabel.font = .monospacedDigitSystemFont(ofSize: 13, weight: .semibold)
        add(stack([symbol("person.crop.circle", size: 16, color: .secondaryLabelColor),
                   accountLabel, spacer(), label("成功请求", size: 11, secondary: true), countLabel], spacing: 8), gap: 22)

        let help = NSButton(title: "接入指南", target: self, action: #selector(openHelp))
        let directory = NSButton(title: "配置目录", target: self, action: #selector(openBackup))
        updateButton = NSButton(title: "检查更新", target: self, action: #selector(checkUpdates))
        [help, directory, updateButton].forEach { $0.bezelStyle = .inline; $0.font = .systemFont(ofSize: 11) }
        let guide = stack([
            stack([label("接入 AiMaMi", size: 12, weight: .semibold), spacer(), updateButton, directory, help], spacing: 12),
            label("中转注入 → 自定义中转模型，填入以上三项，协议选择 Responses。", size: 11, secondary: true)
        ], vertical: true, spacing: 6)
        add(guide, gap: 14)
        add(separator(), gap: 14)
        // 日志直接参与主窗口布局，新增高度优先留给日志的独立滚动区域。
        add(transferLog)
        window.makeKeyAndOrderFront(nil)
        buildMenu()
        setBusy(true)
    }
    private func buildMenu() {
        let mainMenu = NSMenu()
        let appItem = NSMenuItem(); let appMenu = NSMenu()
        appMenu.addItem(withTitle: "关于 \(Product.name)", action: #selector(openAbout), keyEquivalent: "")
        appMenu.addItem(withTitle: "检查更新", action: #selector(checkUpdates), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "退出 \(Product.name)", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu; mainMenu.addItem(appItem)
        let editItem = NSMenuItem(); let edit = NSMenu(title: "编辑")
        edit.addItem(withTitle: "拷贝", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "粘贴", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "全选", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = edit; mainMenu.addItem(editItem)
        let helpItem = NSMenuItem(); let help = NSMenu(title: "帮助")
        help.addItem(withTitle: "接入指南", action: #selector(openHelp), keyEquivalent: "?")
        help.addItem(withTitle: "协议来源", action: #selector(openSource), keyEquivalent: "")
        helpItem.submenu = help; mainMenu.addItem(helpItem)
        NSApp.mainMenu = mainMenu
    }
    private func launchEngine() {
        guard let resources = Bundle.main.resourceURL else { showError("应用资源缺失"); return }
        let task = Process(); task.executableURL = resources.appendingPathComponent("bps-local")
        task.standardInput = input; task.standardOutput = output
        // 启动错误仅在窗口显示。引擎输出采用明确的 JSON 事件，不记录请求或令牌。
        task.standardError = FileHandle.nullDevice
        task.environment = ProxyEnvironment.forEngine()
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let chunk = handle.availableData
            if chunk.isEmpty { handle.readabilityHandler = nil; return }
            DispatchQueue.main.async { self?.receive(chunk) }
        }
        task.terminationHandler = { [weak self] _ in
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.terminationFallback?.cancel()
                self.terminationFallback = nil
                self.process = nil
                if self.closing { NSApp.terminate(nil) }
                else {
                    let wasActive = self.active
                    self.active = false
                    self.transferLog.stopped()
                    self.setBusy(self.updateInProgress)
                    if wasActive && !self.updateInProgress { self.showError("后台进程已停止。请重新打开工具恢复中转服务。") }
                }
            }
        }
        do { try task.run(); process = task } catch { showError("无法启动本地服务：\(error.localizedDescription)") }
    }
    private func receive(_ chunk: Data) {
        outputBuffer.append(chunk)
        while let newline = outputBuffer.firstIndex(of: 10) {
            let line = outputBuffer.prefix(upTo: newline)
            outputBuffer.removeSubrange(...newline)
            do { apply(try JSONDecoder().decode(EngineEvent.self, from: line)) }
            catch { showError("后台返回了无法识别的状态，请重新打开工具") }
        }
    }
    private func apply(_ event: EngineEvent) {
        // 退出由进程终止回调完成；迟到的状态不得撤销关闭或覆盖更新提示。
        guard !closing else { return }
        if let trace = event.trace {
            transferLog.append(trace)
            if !active { transferLog.stopped() }
            return
        }
        defer { detailLabel.toolTip = detailLabel.stringValue }
        if let baseURL = event.baseURL { relayBaseURL = baseURL; routeLabel.stringValue = baseURL }
        if let apiKey = event.apiKey { relayAPIKey = apiKey; keyStateLabel.stringValue = "•••• •••• •••• ••••" }
        if let account = event.account { accountLabel.stringValue = account }
        countLabel.stringValue = "\(event.requests)"
        if let result = event.result {
            guard active && !busy else { return }
            if let account = result.account { accountLabel.stringValue = account }
            if result.success {
                statusLabel.stringValue = "中转运行中"
                updateStatusIcon("checkmark.circle.fill", color: InterfaceStyle.accent)
                detailLabel.stringValue = "最近请求成功 · \(result.model ?? "") · 实际推理档位 \(result.effort ?? "")"
            } else if result.cancelled == true {
                statusLabel.stringValue = "中转运行中"
                updateStatusIcon("checkmark.circle.fill", color: InterfaceStyle.accent)
                detailLabel.stringValue = result.message ?? "客户端已取消本次请求，可以继续使用。"
            } else if result.code == GatewayFailureCode.rateLimited {
                statusLabel.stringValue = "最近请求被限流"
                updateStatusIcon("exclamationmark.circle.fill", color: .systemOrange)
                // 流内限流可能已使用 HTTP 200，展示冷却提示而非误标响应状态。
                detailLabel.stringValue = result.message ?? "BPS 上游限流，请稍后手动重试。"
            } else if result.code == GatewayFailureCode.modelUnavailable {
                statusLabel.stringValue = "上游模型暂不可用"
                updateStatusIcon("exclamationmark.circle.fill", color: .systemOrange)
                detailLabel.stringValue = result.message ?? "BPS 上游暂未提供当前模型，请稍后重试。"
            } else {
                statusLabel.stringValue = "最近请求未完成"
                updateStatusIcon("exclamationmark.circle.fill", color: .systemOrange)
                detailLabel.stringValue = "\(result.message ?? "请求未完成，请稍后重试。")\n\(result.code ?? "unknown")\(result.status.map { "（错误状态 \($0)）" } ?? "")"
            }
            return
        }
        guard let phase = event.phase else { return }
        guard !updateInProgress else { return }
        if let message = event.message { detailLabel.stringValue = message }
        switch phase {
        case .idle:
            // 每次打开均保持未启动，旧版自动启动偏好不再参与决策。
            active = false
            statusLabel.stringValue = "准备就绪"
            updateStatusIcon("pause.circle.fill", color: .secondaryLabelColor); setBusy(false)
        case .testing:
            statusLabel.stringValue = "正在测试连接"
            updateStatusIcon("arrow.triangle.2.circlepath", color: InterfaceStyle.accent); setBusy(true)
            testButton.title = "测试中…"
        case .enabled:
            active = true; statusLabel.stringValue = "中转运行中"
            updateStatusIcon("checkmark.circle.fill", color: InterfaceStyle.accent); setBusy(false)

        case .stopped:
            transferLog.stopped()
            active = false; statusLabel.stringValue = "中转已停止"
            updateStatusIcon("pause.circle.fill", color: .secondaryLabelColor); setBusy(false)
        case .error:
            showError(event.message ?? "操作失败")
        }
    }
    private func setBusy(_ value: Bool) {
        busy = value
        if value { spinner.startAnimation(nil) } else { spinner.stopAnimation(nil) }
        let available = process?.isRunning == true
        startButton.title = active ? "停止中转" : "启动中转"
        startButton.isRunning = active
        startActionLabel.stringValue = startButton.title
        // 图形主按钮保留可见文案、悬浮提示和读屏标签，避免仅凭图标猜测操作。
        startButton.image = NSImage(systemSymbolName: active ? "stop.fill" : "play.fill", accessibilityDescription: nil)?
            .withSymbolConfiguration(.init(pointSize: 22, weight: .medium))
        startButton.setAccessibilityLabel(startButton.title)
        startButton.toolTip = startButton.title
        startButton.bezelColor = active ? nil : InterfaceStyle.accent
        startButton.isEnabled = !value && available && !preview && !closing && !updateInProgress
        testButton.isEnabled = !value && available && !preview && !closing && !updateInProgress
        updateButton?.isEnabled = !value && !updateInProgress && !updateCheckInProgress && !closing && !preview
        if !value { testButton.title = "测试连接" }
        copyURLButton.isEnabled = !relayBaseURL.isEmpty
        copyKeyButton.isEnabled = !relayAPIKey.isEmpty
        // 模型是固定的本地常量，与服务是否运行、是否正在测试无关。
        copyModelButton.isEnabled = true
    }

    private func showError(_ message: String) {
        statusLabel.stringValue = "需要检查"
        updateStatusIcon("exclamationmark.circle.fill", color: .systemOrange)
        detailLabel.stringValue = message
        detailLabel.toolTip = message
        setBusy(false)
    }
    private func send(_ action: EngineAction) {
        guard process?.isRunning == true else { showError("后台服务未运行，请重新打开应用"); return }
        do {
            var data = try JSONEncoder().encode(Command(action: action)); data.append(10)
            try input.fileHandleForWriting.write(contentsOf: data)
            setBusy(true)
            if action == .probe { testButton.title = "测试中…" }
        } catch { showError("无法与后台通信，请重新打开应用") }
    }
    // 退出应用时优先走引擎自己的 quit 命令，确保 HTTP 服务和请求上下文先收敛。
    // 远端请求或管道异常时再使用信号兜底，避免更新器无限等待旧 PID。
    private func requestEngineQuit() {
        guard let process = process, process.isRunning else { return }
        do {
            var data = try JSONEncoder().encode(Command(action: .quit)); data.append(10)
            try input.fileHandleForWriting.write(contentsOf: data)
            if busy {
                // 测试请求在引擎主循环内同步执行，额外发 SIGINT 让其立刻取消上下文。
                process.interrupt()
            }
        } catch {
            process.terminate()
        }
        let fallback = DispatchWorkItem { [weak self, weak process] in
            guard let self = self, self.closing, let process = process, process.isRunning else { return }
            process.terminate()
            let force = DispatchWorkItem { [weak self, weak process] in
                guard let self = self, self.closing, let process = process, process.isRunning else { return }
                kill(process.processIdentifier, SIGKILL)
            }
            self.terminationFallback = force
            DispatchQueue.main.asyncAfter(deadline: .now() + 2, execute: force)
        }
        terminationFallback = fallback
        DispatchQueue.main.asyncAfter(deadline: .now() + 8, execute: fallback)
    }
    @objc private func start() { send(.start) }
    @objc private func test() { send(.probe) }
    @objc private func stop() { send(.stop) }
    @objc private func toggleRelay() { if active { stop() } else { start() } }
    private func copy(_ value: String, using button: CopyButton) {
        guard !value.isEmpty else { return }
        NSPasteboard.general.clearContents()
        if NSPasteboard.general.setString(value, forType: .string) { button.showCopied() }
        else {
            let alert = NSAlert()
            alert.messageText = "暂时无法复制"
            alert.informativeText = "剪贴板不可用，请稍后重试。"
            alert.beginSheetModal(for: window)
        }
    }
    @objc private func copyURL() { copy(relayBaseURL, using: copyURLButton) }
    @objc private func copyKey() {
        // 仅复制本地中转 Key，绝不把账号 access token 放入剪贴板。
        copy(relayAPIKey, using: copyKeyButton)
    }
    @objc private func copyModel() { copy(fixedModelID, using: copyModelButton) }
    @objc private func checkUpdates() { checkForUpdates(manual: true) }
    private func checkForUpdates(manual: Bool) {
        guard !preview, !closing, !updateInProgress, !updateCheckInProgress else { return }
        updateCheckInProgress = true
        updateButton?.isEnabled = false
        updateChecker.check(currentVersion: Product.version) { [weak self] result in
            guard let self = self else { return }
            self.updateCheckInProgress = false
            self.setBusy(self.busy)
            guard !self.closing, !self.updateInProgress else { return }
            switch result {
            case .success(nil):
                if manual {
                    let alert = NSAlert(); alert.messageText = "已是最新版本"; alert.informativeText = "当前版本为 \(Product.version)。"; alert.addButton(withTitle: "完成"); alert.beginSheetModal(for: self.window)
                }
            case .success(let update?):
                let alert = NSAlert()
                alert.messageText = "发现新版本 \(update.version)"
                alert.informativeText = "当前版本：\(Product.version)\n\n\(self.releaseNotes(update.notes))\n\n将从 GitHub 下载并校验后自动重启。"
                alert.addButton(withTitle: "下载并安装")
                alert.addButton(withTitle: "稍后")
                alert.beginSheetModal(for: self.window) { [weak self] response in
                    guard response == .alertFirstButtonReturn else { return }
                    self?.downloadAndInstall(update)
                }
            case .failure(let error):
                if manual {
                    let alert = NSAlert(); alert.messageText = "检查更新失败"; alert.informativeText = error.localizedDescription; alert.addButton(withTitle: "完成"); alert.beginSheetModal(for: self.window)
                }
            }
        }
    }
    private func releaseNotes(_ notes: String) -> String {
        let trimmed = notes.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.count > 900 else { return trimmed.isEmpty ? "该版本未提供发行说明。" : trimmed }
        return String(trimmed.prefix(900)) + "…"
    }
    private func downloadAndInstall(_ update: AppUpdate) {
        guard !closing, !updateInProgress else { return }
        updateInProgress = true
        setBusy(true)
        statusLabel.stringValue = "正在准备更新"
        detailLabel.stringValue = "正在下载并校验版本 \(update.version)，请不要退出应用。"
        updateChecker.stage(update) { [weak self] result in
            guard let self = self else { return }
            switch result {
            case .failure(let error):
                self.updateInProgress = false; self.showError(error.localizedDescription)
            case .success(let staged):
                guard !self.closing else { self.updateChecker.discard(staged); return }
                do {
                    let updater = try self.updateChecker.launchUpdater(staged: staged, replacing: Bundle.main.bundleURL, waitingFor: ProcessInfo.processInfo.processIdentifier)
                    self.updaterProcess = updater
                    updater.terminationHandler = { [weak self] task in
                        guard task.terminationStatus != 0 else { return }
                        DispatchQueue.main.async {
                            guard let self = self else { return }
                            self.closing = false
                            self.terminationFallback?.cancel()
                            self.updateInProgress = false
                            self.updaterProcess = nil
                            self.showError("更新未完成，旧版本已保留。日志：\(staged.stagingDirectory.appendingPathComponent("update.log").path)")
                        }
                    }
                    self.statusLabel.stringValue = "更新已准备"
                    self.detailLabel.stringValue = "应用将关闭并自动重启到版本 \(staged.version)。"
                    NSApp.terminate(nil)
                } catch {
                    self.updateChecker.discard(staged)
                    self.updateInProgress = false; self.showError(error.localizedDescription)
                }
            }
        }
    }
    @objc private func openHelp() {
        let alert = NSAlert()
        alert.messageText = "将星桥接入 AiMaMi"
        alert.informativeText = "1. 点击「启动中转」。\n2. 在 AiMaMi 打开「中转注入 → 自定义中转模型」。\n3. 复制本窗口的 Base URL、API Key 和模型 ID，协议选择 Responses。\n4. 保存并启用中转，保持 AiMaMi 真实账号模式。\n\n启动仅监听本机。点击「测试连接」或发送聊天时，才会使用当前账号连接 BPS。\n\n只支持 gpt-6-astra。图片自动上传为 BPS 原生附件，无需图床或公网域名。支持 PNG、JPEG、GIF、WebP；单张最多 20 MiB，每次最多 20 张、合计 32 MiB。\n\n客户端需发送图片内容；不读取请求里的本地文件路径。支持 JSON / JSON Schema 输出，通过提示约束并在本地校验；不提供上游原生约束解码。"
        alert.informativeText += "\n\n测试连接是真实上游请求，每次测试后有 60 秒冷却，请勿连续点击。"
        alert.addButton(withTitle: "知道了")
        alert.beginSheetModal(for: window)
    }
    @objc private func openAbout() {
        let alert = NSAlert()
        alert.messageText = "\(Product.name) · \(Product.chineseName)"
        alert.informativeText = "版本 \(Product.version)\nAiMaMi 的本地 BPS 连接工具。\n\n文本、工具与图片协议采用 \(Product.sourceDescription)。原生源码逐文件校验。\n原名 BPS Local；升级继续沿用已有连接配置。"
        alert.addButton(withTitle: "完成")
        alert.beginSheetModal(for: window)
    }
    @objc private func openSource() {
        guard let url = Product.sourceURL else { showError("无法读取协议来源清单，请重新安装完整发行包。"); return }
        NSWorkspace.shared.open(url)
    }
    @objc private func openBackup() {
        if FileManager.default.fileExists(atPath: dataDirectory.path) { NSWorkspace.shared.open(dataDirectory) }
        else if let readme = Bundle.main.url(forResource: "README", withExtension: "md") { NSWorkspace.shared.open(readme) }
    }
    func windowShouldClose(_ sender: NSWindow) -> Bool { NSApp.terminate(nil); return false }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard process?.isRunning == true else { return .terminateNow }
        if closing { return .terminateCancel }
        closing = true
        requestEngineQuit()
        // terminateLater 的 AppKit 等待循环会阻塞这里依赖的主队列回调和超时任务。
        // 先取消本次系统退出，待子进程清理完成后再次 terminate，届时直接 terminateNow。
        return .terminateCancel
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
