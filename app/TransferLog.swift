import AppKit

// 与 Go TraceEvent 对齐；仅接收脱敏元数据，不保存聊天内容或请求凭据。
enum TransferStage: String, Decodable { case prepare, upload, connect, headers, streaming, repair, completed, failed, cancelled }
struct TransferTrace: Decodable {
    let requestID: String
    let time: String
    let stage: TransferStage
    let message: String
    let elapsedMS: Int64
    let quietMS: Int64
    let upstreamBytes: Int64
    let clientEvents: Int64
    let toolCalls: Int64
    let attempt: Int
    let httpStatus: Int?
    enum CodingKeys: String, CodingKey {
        case time, stage, message, attempt
        case requestID = "request_id", elapsedMS = "elapsed_ms", quietMS = "quiet_ms"
        case upstreamBytes = "upstream_bytes", clientEvents = "client_events"
        case toolCalls = "tool_calls", httpStatus = "http_status"
    }
    var terminal: Bool { [TransferStage.completed, .failed, .cancelled].contains(stage) }
    var summary: String { "\(requestID) · \(message) · \(elapsedMS / 1000)s" }
    var line: String {
        let wait = !terminal && attempt > 0 && quietMS >= 30_000 ? " · 上游暂无新数据（不代表已断开）" : ""
        return "[\(time)] \(requestID)  \(message)\n  总耗时 \(elapsedMS / 1000)s · 距本轮上游数据 \(quietMS / 1000)s · 收到 \(upstreamBytes) B · 客户端事件 \(clientEvents) · 工具 \(toolCalls) · 请求次数 \(attempt)\(httpStatus.map { " · HTTP \($0)" } ?? "")\(wait)\n"
    }
}

final class TransferLogWindow: NSObject {
    private var window: NSWindow?
    private let text = NSTextView()
    private let overview = NSTextField(labelWithString: "暂无转发请求")
    private let follow = NSButton(checkboxWithTitle: "自动滚动", target: nil, action: nil)
    private var lines: [String] = []
    private var active: [String: String] = [:]
    private let capacity = 1000
    var summary: String { active.isEmpty ? "暂无进行中的转发 · 查看日志" : "\(active.count) 个请求转发中 · 查看日志" }

    func append(_ event: TransferTrace) {
        if event.terminal { active.removeValue(forKey: event.requestID) }
        else { active[event.requestID] = event.summary }
        lines.append(event.line)
        if lines.count > capacity { lines.removeFirst(lines.count - capacity) }
        overview.stringValue = active.isEmpty ? "暂无进行中的转发 · 最近：\(event.summary)" : active.keys.sorted().compactMap { active[$0] }.joined(separator: "\n")
        refresh()
    }
    // 停止或引擎退出后清除活跃列表，避免误显示残留中的请求。
    func stopped() { active.removeAll(); overview.stringValue = "中转未运行 · 历史日志保留在本次窗口会话" }
    private func refresh() {
        guard window != nil else { return }
        let origin = text.enclosingScrollView?.contentView.bounds.origin
        text.string = lines.joined(separator: "\n")
        if follow.state == .on { text.scrollToEndOfDocument(nil) }
        else if let origin = origin { text.enclosingScrollView?.contentView.scroll(to: origin) }
    }
    func show() {
        if let window = window { window.makeKeyAndOrderFront(nil); return }
        let panel = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 940, height: 560), styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: false)
        panel.title = "星桥 · 转发日志"
        panel.minSize = NSSize(width: 760, height: 420)
        panel.isReleasedWhenClosed = false
        let root = NSStackView(); root.orientation = .vertical; root.alignment = .leading; root.spacing = 12; root.translatesAutoresizingMaskIntoConstraints = false
        panel.contentView!.addSubview(root)
        NSLayoutConstraint.activate([root.leadingAnchor.constraint(equalTo: panel.contentView!.leadingAnchor, constant: 20), root.trailingAnchor.constraint(equalTo: panel.contentView!.trailingAnchor, constant: -20), root.topAnchor.constraint(equalTo: panel.contentView!.topAnchor, constant: 20), root.bottomAnchor.constraint(equalTo: panel.contentView!.bottomAnchor, constant: -20)])
        let hint = NSTextField(wrappingLabelWithString: "每 5 秒更新 · 仅保留最近 1000 条 · 不含密钥、正文或工具代码。上游数据可能只是保活或推理；已返回工具不代表客户端执行完毕。")
        hint.font = .systemFont(ofSize: 12); hint.textColor = .secondaryLabelColor
        root.addArrangedSubview(hint); hint.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        overview.lineBreakMode = .byTruncatingTail; overview.maximumNumberOfLines = 8
        overview.font = .systemFont(ofSize: 12, weight: .medium)
        root.addArrangedSubview(overview); overview.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        let scroll = NSScrollView(); scroll.hasVerticalScroller = true; scroll.hasHorizontalScroller = true; scroll.borderType = .bezelBorder
        text.isEditable = false; text.isSelectable = true; text.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        text.isVerticallyResizable = true; text.isHorizontallyResizable = true
        text.autoresizingMask = [.width]; text.textContainer?.widthTracksTextView = false
        text.textContainer?.containerSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        scroll.documentView = text; root.addArrangedSubview(scroll)
        scroll.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 220).isActive = true
        let copy = NSButton(title: "复制日志", target: self, action: #selector(copyLog))
        let save = NSButton(title: "导出…", target: self, action: #selector(saveLog))
        let clear = NSButton(title: "清空显示", target: self, action: #selector(clearLog))
        follow.state = .on
        let actions = NSStackView(views: [follow, copy, save, clear]); actions.spacing = 12
        root.addArrangedSubview(actions)
        window = panel; refresh(); panel.center(); panel.makeKeyAndOrderFront(nil)
    }
    @objc private func copyLog() { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string) }
    @objc private func clearLog() { lines.removeAll(); refresh() }
    @objc private func saveLog() {
        guard let window = window else { return }
        let panel = NSSavePanel(); panel.nameFieldStringValue = "AstraBridge-transfer.log"
        panel.beginSheetModal(for: window) { [weak self] response in
            guard response == .OK, let url = panel.url, let self = self else { return }
            do { try self.lines.joined(separator: "\n").write(to: url, atomically: true, encoding: .utf8) }
            catch { let alert = NSAlert(error: error); alert.beginSheetModal(for: window) }
        }
    }
}
