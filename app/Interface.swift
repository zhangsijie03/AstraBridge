import AppKit

// 所有界面共用系统语义色，随 macOS 外观和辅助功能设置变化。
enum InterfaceStyle {
    static let accent = NSColor.systemTeal
    static let pageInset: CGFloat = 28
    static let cornerRadius: CGFloat = 14
    static let rowHeight: CGFloat = 68
}

// 系统 circular 按钮会把可见圆面限制得很小；显式绘制圆面，仍保留
// NSButton 的键盘、点击、禁用和辅助功能行为。
final class RelayPowerButton: NSButton {
    var isRunning = false { didSet { needsDisplay = true } }
    override var isEnabled: Bool { didSet { needsDisplay = true } }

    private var circleRect: NSRect {
        let diameter = min(bounds.width, bounds.height) - 6
        return NSRect(x: bounds.midX - diameter / 2, y: bounds.midY - diameter / 2,
                      width: diameter, height: diameter)
    }

    override func draw(_ dirtyRect: NSRect) {
        let circle = NSBezierPath(ovalIn: circleRect)
        var fill = isRunning ? NSColor.controlBackgroundColor :
            (InterfaceStyle.accent.blended(withFraction: 0.28, of: .black) ?? InterfaceStyle.accent)
        if !isEnabled { fill = NSColor.quaternaryLabelColor.withAlphaComponent(0.12) }
        else if isHighlighted { fill = fill.blended(withFraction: 0.15, of: .black) ?? fill }
        fill.setFill(); circle.fill()
        (isEnabled ? InterfaceStyle.accent.withAlphaComponent(0.6) : NSColor.separatorColor).setStroke()
        circle.lineWidth = 1; circle.stroke()
        let foreground: NSColor = !isEnabled ? .tertiaryLabelColor : (isRunning ? .labelColor : .white)
        let glyphRect = NSRect(x: bounds.midX - 11, y: bounds.midY - 11, width: 22, height: 22)
        image?.withSymbolConfiguration(.init(paletteColors: [foreground]))?.draw(in: glyphRect)
    }

    override var focusRingMaskBounds: NSRect { circleRect }
    override func drawFocusRingMask() { NSBezierPath(ovalIn: circleRect).fill() }
    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        needsDisplay = true
    }
}

final class SurfaceView: NSView {
    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)
        let shape = NSBezierPath(roundedRect: bounds.insetBy(dx: 0.5, dy: 0.5),
                                 xRadius: InterfaceStyle.cornerRadius, yRadius: InterfaceStyle.cornerRadius)
        NSColor.controlBackgroundColor.setFill()
        shape.fill()
        NSColor.separatorColor.withAlphaComponent(0.18).setStroke()
        shape.lineWidth = 1
        shape.stroke()
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        needsDisplay = true
    }
}

final class CopyButton: NSButton {
    private var resetFeedback: DispatchWorkItem?
    private let copyLabel: String

    init(label: String, target: AnyObject?, action: Selector) {
        copyLabel = label
        super.init(frame: .zero)
        title = "复制"
        self.target = target
        self.action = action
        bezelStyle = .rounded
        controlSize = .regular
        font = .systemFont(ofSize: 12, weight: .medium)
        imagePosition = .imageLeading
        image = NSImage(systemSymbolName: "doc.on.doc", accessibilityDescription: nil)
        image?.isTemplate = true
        setAccessibilityLabel(label)
        toolTip = label
        translatesAutoresizingMaskIntoConstraints = false
        widthAnchor.constraint(equalToConstant: 88).isActive = true
        heightAnchor.constraint(equalToConstant: 30).isActive = true
    }

    required init?(coder: NSCoder) { nil }

    func showCopied() {
        resetFeedback?.cancel()
        title = "已复制"
        image = NSImage(systemSymbolName: "checkmark", accessibilityDescription: nil)
        setAccessibilityLabel("\(copyLabel)，已复制")
        // 连续点击时重新计时；反馈只属于被点击的按钮，不覆盖连接状态。
        let reset = DispatchWorkItem { [weak self] in
            guard let self = self else { return }
            self.title = "复制"
            self.image = NSImage(systemSymbolName: "doc.on.doc", accessibilityDescription: nil)
            self.setAccessibilityLabel(self.copyLabel)
        }
        resetFeedback = reset
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.8, execute: reset)
    }

    deinit { resetFeedback?.cancel() }
}
