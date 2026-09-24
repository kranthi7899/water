import AppKit

/// Borderless floating panel that can still take keyboard focus.
final class AskPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
    var onEscape: (() -> Void)?
    override func cancelOperation(_ sender: Any?) { onEscape?() }
}

/// The Spotlight-style pop-up: one text field, and the streamed reply below.
/// Pure view: it knows nothing about the daemon; AppDelegate wires it up.
final class AskPanelController: NSObject, NSTextFieldDelegate {
    private let panel: AskPanel
    private let input = NSTextField()
    private let output = NSTextView()
    private let scroll = NSScrollView()
    private let status = NSTextField(labelWithString: "")

    private let width: CGFloat = 680
    private let compactHeight: CGFloat = 64
    private let expandedHeight: CGFloat = 360

    var onSubmit: ((String) -> Void)?
    var onClose: (() -> Void)?

    var isVisible: Bool { panel.isVisible }

    override init() {
        panel = AskPanel(contentRect: NSRect(x: 0, y: 0, width: 680, height: 64),
                         styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        super.init()
        panel.isFloatingPanel = true
        panel.level = .floating
        panel.hidesOnDeactivate = false
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.isMovableByWindowBackground = true
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        panel.onEscape = { [weak self] in self?.close() }

        let bg = NSVisualEffectView()
        bg.material = .popover
        bg.blendingMode = .behindWindow
        bg.state = .active
        bg.wantsLayer = true
        bg.layer?.cornerRadius = 14
        bg.layer?.masksToBounds = true
        panel.contentView = bg

        input.placeholderString = "Ask Water…"
        input.font = .systemFont(ofSize: 22, weight: .regular)
        input.isBordered = false
        input.drawsBackground = false
        input.focusRingType = .none
        input.delegate = self
        input.cell?.usesSingleLineMode = true
        input.cell?.wraps = false
        input.cell?.isScrollable = true

        status.font = .systemFont(ofSize: 11)
        status.textColor = .secondaryLabelColor
        status.alignment = .right

        output.isEditable = false
        output.isSelectable = true
        output.drawsBackground = false
        output.font = .systemFont(ofSize: 14)
        output.textContainerInset = NSSize(width: 2, height: 4)
        output.isVerticallyResizable = true
        output.autoresizingMask = [.width]
        scroll.documentView = output
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        scroll.borderType = .noBorder
        scroll.isHidden = true

        for v in [input, status, scroll] as [NSView] {
            v.translatesAutoresizingMaskIntoConstraints = false
            bg.addSubview(v)
        }
        NSLayoutConstraint.activate([
            input.leadingAnchor.constraint(equalTo: bg.leadingAnchor, constant: 18),
            input.trailingAnchor.constraint(equalTo: status.leadingAnchor, constant: -8),
            input.topAnchor.constraint(equalTo: bg.topAnchor, constant: 16),
            input.heightAnchor.constraint(equalToConstant: 32),
            status.trailingAnchor.constraint(equalTo: bg.trailingAnchor, constant: -16),
            status.centerYAnchor.constraint(equalTo: input.centerYAnchor),
            status.widthAnchor.constraint(equalToConstant: 170),
            scroll.leadingAnchor.constraint(equalTo: bg.leadingAnchor, constant: 16),
            scroll.trailingAnchor.constraint(equalTo: bg.trailingAnchor, constant: -12),
            scroll.topAnchor.constraint(equalTo: input.bottomAnchor, constant: 10),
            scroll.bottomAnchor.constraint(equalTo: bg.bottomAnchor, constant: -12),
        ])
    }

    // MARK: showing and hiding

    func show(placeholder: String = "Ask Water…") {
        input.placeholderString = placeholder
        position(height: scroll.isHidden ? compactHeight : expandedHeight)
        NSApp.activate(ignoringOtherApps: true)
        panel.makeKeyAndOrderFront(nil)
        panel.makeFirstResponder(input)
        input.selectText(nil)
    }

    func close() {
        panel.orderOut(nil)
        onClose?()
    }

    private func position(height: CGFloat) {
        let screen = NSScreen.main ?? NSScreen.screens.first
        let vf = screen?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let x = vf.midX - width / 2
        let top = vf.maxY - vf.height * 0.18
        panel.setFrame(NSRect(x: x, y: top - height, width: width, height: height), display: true)
    }

    private func expand() {
        guard scroll.isHidden else { return }
        scroll.isHidden = false
        position(height: expandedHeight)
    }

    // MARK: content

    func setStatus(_ s: String) { status.stringValue = s }

    func setInput(_ s: String) { input.stringValue = s }

    /// Starts a fresh reply area for a new turn.
    func beginReply() {
        output.textStorage?.setAttributedString(NSAttributedString())
        expand()
    }

    func appendReply(_ s: String) { append(s, color: .labelColor) }

    func appendNote(_ s: String) { append("\n" + s + "\n", color: .secondaryLabelColor, size: 12) }

    func appendError(_ s: String) { append("\n⚠︎ " + s + "\n", color: .systemRed) }

    /// An action the model wants to take is waiting in the approval queue.
    func appendApproval(id: String?) {
        let what = id.map { "Approval needed (\($0))." } ?? "Approval needed."
        append("\n⏸ " + what + " Review it with `water approve`.\n", color: .systemOrange, weight: .semibold)
    }

    private func append(_ s: String, color: NSColor, size: CGFloat = 14, weight: NSFont.Weight = .regular) {
        expand()
        let a = NSAttributedString(string: s, attributes: [.foregroundColor: color, .font: NSFont.systemFont(ofSize: size, weight: weight)])
        output.textStorage?.append(a)
        output.scrollToEndOfDocument(nil)
    }

    // MARK: NSTextFieldDelegate

    func control(_ control: NSControl, textView: NSTextView, doCommandBy sel: Selector) -> Bool {
        if sel == #selector(NSResponder.insertNewline(_:)) {
            let text = input.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
            if !text.isEmpty { onSubmit?(text) }
            return true
        }
        if sel == #selector(NSResponder.cancelOperation(_:)) {
            close()
            return true
        }
        return false
    }
}
