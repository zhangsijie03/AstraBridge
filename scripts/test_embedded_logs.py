#!/usr/bin/env python3
"""Exercise the real AppKit layout and embedded log using offline fixtures only."""
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1]
output = root / '.build/embedded-log-check'
output.mkdir(parents=True, exist_ok=True)
source = (root / 'app/main.swift').read_text().split('\nlet app = NSApplication.shared\n')[0]
checks = r'''
func check(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() { print("FAIL: " + message); exit(1) }
}
func descendants(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(descendants) }
func fixture(_ index: Int, stage: TransferStage = .completed, semantic: Int? = nil) -> TransferTrace {
    TransferTrace(requestID: String(format: "R%06d", index), time: "2026-09-30T12:00:00Z", stage: stage,
        message: stage == .completed ? "转发已完成（离线测试）" : "正在接收上游数据（离线测试）",
        elapsedMS: 35000, quietMS: 5000, upstreamBytes: 4096, clientEvents: 6, toolCalls: 0,
        attempt: 1, httpStatus: 200, semanticStatus: semantic, limitHints: nil)
}
extension AppDelegate {
    func verifyEmbeddedLog() throws {
        check(preview && process == nil, "Checks must not launch an engine")
        let content = window.contentView!
        let views = descendants(transferLog)
        let table = views.compactMap { $0 as? NSTableView }.first!
        let scroll = table.enclosingScrollView!
        func cell(_ row: Int, _ column: Int) -> NSTextField {
            transferLog.tableView(table, viewFor: table.tableColumns[column], row: row) as! NSTextField
        }
        func rowID(_ row: Int) -> String { cell(row, 2).stringValue }
        let buttons = views.compactMap { $0 as? NSButton }
        let copy = buttons.first { $0.title == "复制日志" }!
        let clear = buttons.first { $0.title == "清空显示" }!
        let follow = buttons.first { $0.title == "自动滚动" }!
        check(transferLog.window === window, "Log must belong to the main window")
        check(!descendants(content).contains { ($0 as? NSButton)?.title == "转发日志" }, "Obsolete log navigation remains")
        check(!copy.isEnabled && !clear.isEnabled && table.numberOfRows == 0, "Empty state is missing")
        check(table.tableColumns.count == 5 && !scroll.hasHorizontalScroller, "Compact columns are missing")
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            window.appearance = NSAppearance(named: appearance)
            for size in [NSSize(width: 700, height: 844), NSSize(width: 700, height: 760), NSSize(width: 900, height: 1000)] {
                window.setContentSize(size); content.layoutSubtreeIfNeeded()
                print("Layout: content=\(content.bounds.size), log=\(scroll.frame.size)")
                check(scroll.frame.height >= 120, "Log viewport is too small")
                let columnWidth = table.rect(ofColumn: table.tableColumns.count - 1).maxX
                check(columnWidth <= scroll.contentSize.width + 2, "Columns overflow the log viewport")
                for view in descendants(content) where (view is NSButton || view is NSTextField) && !view.isHidden {
                    let rect = view.convert(view.bounds, to: content)
                    check(rect.minX >= -1 && rect.maxX <= content.bounds.width + 1 && rect.minY >= -1 && rect.maxY <= content.bounds.height + 1, "Control clipped: \(view)")
                }
            }
        }
        window.setContentSize(NSSize(width: 700, height: 844)); content.layoutSubtreeIfNeeded()
        transferLog.append(fixture(1, stage: .streaming))
        check(table.numberOfRows == 1 && cell(0, 0).stringValue == "…", "In-progress icon is incorrect")
        transferLog.append(fixture(1, stage: .streaming))
        transferLog.append(fixture(1))
        check(table.numberOfRows == 1 && cell(0, 0).stringValue == "✓", "Repeated events must update one successful row")
        transferLog.append(fixture(2, stage: .failed, semantic: 429))
        check(cell(1, 0).stringValue == "✕" && cell(1, 4).stringValue.contains("429"), "HTTP 200 failure must show a cross")
        check(cell(1, 4).toolTip?.contains("错误分类状态 429") == true && cell(1, 4).toolTip?.contains("上游 HTTP 200") == true, "Full details were lost")
        transferLog.append(fixture(3, stage: .cancelled))
        check(cell(2, 0).stringValue == "—", "Cancellation must remain neutral")
        clear.performClick(nil)
        follow.performClick(nil)
        for index in 1...1001 { transferLog.append(fixture(index)) }
        check(table.numberOfRows == 1000, "History must be bounded at 1000 requests")
        check(rowID(0) == "R000002" && rowID(999) == "R001001", "History eviction order is wrong")
        table.selectRowIndexes(IndexSet(integer: 498), byExtendingSelection: false)
        scroll.contentView.scroll(to: NSPoint(x: 0, y: 300))
        let topID = rowID(table.row(at: NSPoint(x: 0, y: scroll.contentView.bounds.origin.y)))
        transferLog.append(fixture(1002))
        check(table.selectedRow >= 0 && rowID(table.selectedRow) == "R000500", "History eviction lost the selection")
        check(rowID(table.row(at: NSPoint(x: 0, y: scroll.contentView.bounds.origin.y))) == topID, "Paused follow lost the top request")
        follow.performClick(nil); content.layoutSubtreeIfNeeded()
        check(scroll.contentView.bounds.origin.y > 300, "Enabling follow did not reach recent entries")
        clear.performClick(nil)
        check(!copy.isEnabled && !clear.isEnabled && table.numberOfRows == 0, "Clearing did not reset the view")
        transferLog.append(fixture(2000, stage: .streaming))
        check(views.compactMap { $0 as? NSTextField }.contains { $0.stringValue.hasPrefix("1 个请求转发中") }, "Active summary is missing")
        table.selectRowIndexes(IndexSet(integer: 0), byExtendingSelection: false)
        transferLog.stopped()
        check(table.numberOfRows == 1 && rowID(0) == "R002000" && cell(0, 0).stringValue == "—", "Stopping must preserve an interrupted row")
        check(table.selectedRow == 0, "Stopping lost the selection")
        check(views.compactMap { $0 as? NSTextField }.contains { $0.stringValue.hasPrefix("中转未运行") }, "Stopping left a stale active summary")
        clear.performClick(nil)
        check(process == nil, "UI checks launched an engine")
        print("PASS: compact layout, resize, request aggregation, outcome icons, HTTP 200 error, tooltips, history cap, selection, follow, clear and stop; no engine or upstream requests")
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
delegate.applicationDidFinishLaunching(Notification(name: NSApplication.didFinishLaunchingNotification))
try delegate.verifyEmbeddedLog()
'''
(output / 'main.swift').write_text(source + '\n' + checks)
subprocess.run(['swiftc', '-swift-version', '5', '-module-cache-path', str(root / '.build/swift-cache'),
                str(root / 'app/Interface.swift'), str(root / 'app/TransferLog.swift'), str(output / 'main.swift'),
                '-o', str(output / 'verify'), '-framework', 'AppKit', '-framework', 'CFNetwork'], check=True)
subprocess.run([str(output / 'verify'), '--preview'], check=True)
