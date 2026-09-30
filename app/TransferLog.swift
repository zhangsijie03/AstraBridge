import AppKit

// 与 Go TraceEvent 对齐；仅接收脱敏元数据，不保存聊天内容或请求凭据。
enum TransferStage: String, Decodable { case prepare, upload, connect, headers, streaming, repair, completed, failed, cancelled }
struct TransferLimitHint: Decodable { let name: String; let value: String }
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
    let semanticStatus: Int?
    let limitHints: [TransferLimitHint]?
    enum CodingKeys: String, CodingKey {
        case time, stage, message, attempt
        case requestID = "request_id", elapsedMS = "elapsed_ms", quietMS = "quiet_ms"
        case upstreamBytes = "upstream_bytes", clientEvents = "client_events"
        case toolCalls = "tool_calls", httpStatus = "http_status"
        case semanticStatus = "semantic_status", limitHints = "limit_hints"
    }
    var terminal: Bool { [TransferStage.completed, .failed, .cancelled].contains(stage) }
    var summary: String { "\(requestID) · \(message) · \(elapsedMS / 1000)s" }
    var line: String {
        let wait = !terminal && attempt > 0 && quietMS >= 30_000 ? " · 上游暂无新数据（不代表已断开）" : ""
        let semantic = semanticStatus.map { " · 错误分类状态 \($0)" } ?? ""
        let hints = (limitHints ?? []).map { "\($0.name)=\($0.value)" }.joined(separator: " · ")
        let limits = hints.isEmpty ? "" : "  上游限额提示（该次响应报告值，不代表限流原因）：\(hints)\n"
        return "[\(time)] \(requestID)  \(message)\n  总耗时 \(elapsedMS / 1000)s · 距本轮上游数据 \(quietMS / 1000)s · 收到 \(upstreamBytes) B · 客户端事件 \(clientEvents) · 工具 \(toolCalls) · 请求次数 \(attempt)\(httpStatus.map { " · 上游 HTTP \($0)" } ?? "")\(semantic)\(wait)\n\(limits)"
    }
}

final class TransferLogView: NSView, NSTableViewDataSource, NSTableViewDelegate {
    private enum Column: String { case outcome, time, request, elapsed, message }
    private struct Entry {
        var trace: TransferTrace
        let time: String
        var interrupted = false
        var terminal: Bool { interrupted || trace.terminal }
        var glyph: String {
            if interrupted { return "—" }
            switch trace.stage { case .completed: return "✓"; case .failed: return "✕"; case .cancelled: return "—"; default: return "…" }
        }
        var state: String {
            if interrupted { return "中转已停止" }
            switch trace.stage {
            case .prepare: return "校验请求"
            case .upload: return "上传图片"
            case .connect: return "连接上游"
            case .headers: return "等待响应"
            case .repair: return "工具纠错"
            case .streaming: return trace.quietMS >= 30_000 ? "等待上游 · 静默 \(trace.quietMS / 1000)s" : "接收响应"
            case .completed: return trace.toolCalls > 0 ? "完成 · 已返回工具" : "完成"
            case .cancelled: return "已取消"
            case .failed:
                switch trace.semanticStatus ?? trace.httpStatus {
                case 404: return "上游返回 404"
                case 429: return "请求限流 · 429"
                case 401: return "登录验证失败 · 401"
                case 403: return "上游拒绝访问 · 403"
                case 504: return "等待上游超时 · 504"
                case let status? where status >= 400: return "转发失败 · \(status)"
                default: return "转发失败 · 悬停查看原因"
                }
            }
        }
        var elapsed: String {
            let seconds = trace.elapsedMS / 1000
            return seconds < 60 ? "\(seconds)s" : "\(seconds / 60)m\(seconds % 60)s"
        }
        var details: String { (interrupted ? "中转已停止；未确认完成。\n" : "") + trace.line }
    }
    private static let timestampParser = ISO8601DateFormatter()
    private static let clock: DateFormatter = { let value = DateFormatter(); value.dateFormat = "HH:mm:ss"; return value }()
    private let table = NSTableView()
    private let overview = NSTextField(labelWithString: "暂无转发请求")
    private let follow = NSButton(checkboxWithTitle: "自动滚动", target: nil, action: nil)
    private let scroll = NSScrollView()
    private let copy = NSButton(title: "复制日志", target: nil, action: nil)
    private let save = NSButton(title: "导出…", target: nil, action: nil)
    private let clear = NSButton(title: "清空显示", target: nil, action: nil)
    private var lines: [String] = []
    private var entries: [Entry] = []
    private var active: [String: String] = [:]
    private let capacity = 1000

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        translatesAutoresizingMaskIntoConstraints = false
        let root = NSStackView(); root.orientation = .vertical; root.alignment = .leading; root.spacing = 8; root.translatesAutoresizingMaskIntoConstraints = false
        addSubview(root)
        NSLayoutConstraint.activate([root.leadingAnchor.constraint(equalTo: leadingAnchor), root.trailingAnchor.constraint(equalTo: trailingAnchor), root.topAnchor.constraint(equalTo: topAnchor), root.bottomAnchor.constraint(equalTo: bottomAnchor)])
        let title = NSTextField(labelWithString: "转发日志"); title.font = .systemFont(ofSize: 13, weight: .semibold)
        let space = NSView(); space.setContentHuggingPriority(.defaultLow, for: .horizontal)
        follow.state = .on; follow.target = self; follow.action = #selector(followChanged)
        copy.target = self; copy.action = #selector(copyLog)
        save.target = self; save.action = #selector(saveLog)
        clear.target = self; clear.action = #selector(clearLog)
        [follow, copy, save, clear].forEach { $0.controlSize = .small; $0.font = .systemFont(ofSize: 11) }
        [copy, save, clear].forEach { $0.bezelStyle = .rounded }
        copy.toolTip = "复制最近 1000 条完整事件，包含错误原因与计数"
        let actions = NSStackView(views: [title, space, follow, copy, save, clear]); actions.spacing = 10
        root.addArrangedSubview(actions); actions.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        overview.lineBreakMode = .byTruncatingTail; overview.maximumNumberOfLines = 1
        overview.font = .systemFont(ofSize: 11, weight: .medium)
        overview.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        root.addArrangedSubview(overview); overview.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        let hint = NSTextField(labelWithString: "每个请求一行 · 悬停查看详情 · 复制／导出保留完整事件")
        hint.font = .systemFont(ofSize: 10); hint.textColor = .secondaryLabelColor
        root.addArrangedSubview(hint)
        scroll.hasVerticalScroller = true; scroll.hasHorizontalScroller = false; scroll.autohidesScrollers = true; scroll.borderType = .bezelBorder
        table.delegate = self; table.dataSource = self; table.rowHeight = 24
        table.usesAlternatingRowBackgroundColors = true
        table.allowsMultipleSelection = false; table.allowsColumnReordering = false
        table.autoresizingMask = [.width]
        table.columnAutoresizingStyle = .lastColumnOnlyAutoresizingStyle
        table.setAccessibilityLabel("精简转发日志")
        let columns: [(Column, String, CGFloat)] = [(.outcome, "结果", 36), (.time, "时间", 68), (.request, "请求", 82), (.elapsed, "耗时", 62), (.message, "状态", 360)]
        for (key, title, width) in columns {
            let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier(key.rawValue)); column.title = title
            column.width = width; column.minWidth = key == .message ? 140 : width
            column.maxWidth = key == .message ? 2000 : width
            column.resizingMask = key == .message ? .autoresizingMask : []
            table.addTableColumn(column)
        }
        scroll.documentView = table; root.addArrangedSubview(scroll)
        scroll.widthAnchor.constraint(equalTo: root.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 120).isActive = true
        scroll.setContentHuggingPriority(.init(1), for: .vertical)
        updateActions()
    }
    required init?(coder: NSCoder) { nil }
    override func layout() {
        super.layout()
        // 日志嵌入主窗口后，随可用宽度收缩最后一列，避免窄窗口出现被裁掉的状态。
        let width = scroll.contentSize.width
        if width > 0 && abs(table.frame.width - width) > 0.5 {
            table.setFrameSize(NSSize(width: width, height: table.frame.height))
        }
        table.sizeLastColumnToFit()
    }
    func numberOfRows(in tableView: NSTableView) -> Int { entries.count }
    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        guard entries.indices.contains(row), let identifier = tableColumn?.identifier, let column = Column(rawValue: identifier.rawValue) else { return nil }
        let field = table.makeView(withIdentifier: identifier, owner: self) as? NSTextField ?? NSTextField(labelWithString: "")
        let entry = entries[row]; field.identifier = identifier
        field.font = column == .outcome ? .systemFont(ofSize: 16, weight: .bold) : .monospacedSystemFont(ofSize: 11, weight: .regular)
        field.lineBreakMode = .byTruncatingTail; field.maximumNumberOfLines = 1
        field.alignment = column == .outcome ? .center : .left
        field.textColor = .labelColor; field.toolTip = entry.details
        switch column {
        case .outcome:
            field.stringValue = entry.glyph
            field.textColor = entry.interrupted || entry.trace.stage == .cancelled ? .secondaryLabelColor : entry.trace.stage == .completed ? .systemGreen : entry.trace.stage == .failed ? .systemRed : .systemOrange
            field.setAccessibilityLabel(entry.state)
        case .time: field.stringValue = entry.time
        case .request: field.stringValue = entry.trace.requestID
        case .elapsed: field.stringValue = entry.elapsed
        case .message: field.stringValue = entry.state
        }
        return field
    }
    func append(_ event: TransferTrace) {
        let selectedID = entries.indices.contains(table.selectedRow) ? entries[table.selectedRow].trace.requestID : nil
        let origin = scroll.contentView.bounds.origin
        let top = table.row(at: NSPoint(x: 0, y: origin.y))
        let topID = entries.indices.contains(top) ? entries[top].trace.requestID : nil
        let offset = top >= 0 ? origin.y - table.rect(ofRow: top).minY : 0
        if event.terminal { active.removeValue(forKey: event.requestID) } else { active[event.requestID] = event.summary }
        lines.append(event.line); if lines.count > capacity { lines.removeFirst(lines.count - capacity) }
        if let index = entries.firstIndex(where: { $0.trace.requestID == event.requestID }) {
            entries[index].trace = event; entries[index].interrupted = false
        } else {
            let clock = Self.timestampParser.date(from: event.time).map { Self.clock.string(from: $0) } ?? "--:--:--"
            entries.append(Entry(trace: event, time: clock))
            if entries.count > capacity { entries.remove(at: entries.firstIndex(where: { $0.terminal }) ?? 0) }
        }
        overview.stringValue = active.isEmpty ? "暂无进行中的转发 · 最近：\(event.requestID)" : "\(active.count) 个请求转发中"
        overview.toolTip = active.isEmpty ? event.summary : active.keys.sorted().compactMap { active[$0] }.joined(separator: "\n")
        table.reloadData()
        if let id = selectedID, let row = entries.firstIndex(where: { $0.trace.requestID == id }) { table.selectRowIndexes(IndexSet(integer: row), byExtendingSelection: false) }
        else { table.deselectAll(nil) }
        // 更新同一请求不会刷出重复行；暂停跟随时按请求编号保留选中行与阅读位置。
        if follow.state == .on { scrollToLatest() }
        else {
            let target = topID.flatMap { id in entries.firstIndex(where: { $0.trace.requestID == id }) }
            scroll.contentView.scroll(to: NSPoint(x: 0, y: target.map { table.rect(ofRow: $0).minY + offset } ?? origin.y))
            scroll.reflectScrolledClipView(scroll.contentView)
        }
        updateActions()
    }
    func stopped() {
        let selected = table.selectedRowIndexes
        let origin = scroll.contentView.bounds.origin
        active.removeAll(); overview.stringValue = "中转未运行 · 历史日志保留在本次会话"; overview.toolTip = nil
        for index in entries.indices where !entries[index].terminal { entries[index].interrupted = true }
        table.reloadData()
        table.selectRowIndexes(selected, byExtendingSelection: false)
        scroll.contentView.scroll(to: origin); scroll.reflectScrolledClipView(scroll.contentView)
    }
    private func updateActions() { [copy, save, clear].forEach { $0.isEnabled = !lines.isEmpty } }
    private func scrollToLatest() { if !entries.isEmpty { table.scrollRowToVisible(entries.count - 1) } }
    @objc private func followChanged() { if follow.state == .on { scrollToLatest() } }
    @objc private func copyLog() { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string) }
    @objc private func clearLog() {
        lines.removeAll(); entries.removeAll(); table.reloadData(); updateActions()
        // 清空的是显示历史，进行中的请求仍由后续快照继续更新。
        overview.stringValue = active.isEmpty ? "暂无转发请求" : "\(active.count) 个请求转发中"
        overview.toolTip = active.isEmpty ? nil : active.keys.sorted().compactMap { active[$0] }.joined(separator: "\n")
    }
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
