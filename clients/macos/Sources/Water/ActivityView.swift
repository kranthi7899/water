import AppKit
import QuartzCore
import WaterClientCore

/// The Activity HUD's one view (V-hud): a blob, and — expanded — the
/// current turn's steps and any open approval cards with Approve, Edit and
/// Reject. It draws an `ActivityModel` and reports clicks; it knows nothing
/// about windows, the daemon or where it's hosted, so the workspace can
/// later embed the same view as a "what's running now" strip.
///
/// Every string it shows (step labels, actions, read-backs) is set as a
/// plain `NSTextField.stringValue`: never attributed, never HTML. Read-backs
/// carry text from outside (an email subject, an attendee's name), and
/// plain text is the whole defence.
final class ActivityView: NSView {
    var onApprove: ((ApprovalCard) -> Void)?
    var onReject: ((ApprovalCard) -> Void)?
    var onEdit: ((ApprovalCard) -> Void)?

    static let compactSize = NSSize(width: 64, height: 64)
    static let expandedWidth: CGFloat = 340
    private static let inset: CGFloat = 12
    private static let blobSide: CGFloat = 40

    private let blob = BlobView()
    private let title = NSTextField(labelWithString: "")
    private let header = NSStackView()
    private let stepsStack = NSStackView()
    private let cardsStack = NSStackView()
    private let root = NSStackView()
    private var widthConstraint: NSLayoutConstraint!

    private var shownSteps: [ActivityStep]?
    private var shownCards: [ApprovalCard]?
    private(set) var isExpanded = false

    override init(frame: NSRect) {
        super.init(frame: frame)
        build()
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    private func build() {
        blob.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            blob.widthAnchor.constraint(equalToConstant: Self.blobSide),
            blob.heightAnchor.constraint(equalToConstant: Self.blobSide),
        ])
        title.font = .systemFont(ofSize: 13, weight: .semibold)
        title.textColor = .labelColor
        title.lineBreakMode = .byTruncatingTail

        header.orientation = .horizontal
        header.alignment = .centerY
        header.spacing = 10
        header.addArrangedSubview(blob)
        header.addArrangedSubview(title)

        for s in [stepsStack, cardsStack] {
            s.orientation = .vertical
            s.alignment = .leading
            s.spacing = 6
        }
        cardsStack.spacing = 10

        root.orientation = .vertical
        root.alignment = .leading
        root.spacing = 10
        root.edgeInsets = NSEdgeInsets(top: Self.inset, left: Self.inset, bottom: Self.inset, right: Self.inset)
        root.addArrangedSubview(header)
        root.addArrangedSubview(stepsStack)
        root.addArrangedSubview(cardsStack)
        root.translatesAutoresizingMaskIntoConstraints = false
        addSubview(root)
        widthConstraint = root.widthAnchor.constraint(equalToConstant: Self.compactSize.width)
        NSLayoutConstraint.activate([
            root.leadingAnchor.constraint(equalTo: leadingAnchor),
            root.topAnchor.constraint(equalTo: topAnchor),
            widthConstraint,
        ])
        applyExpanded(false)
    }

    /// The size this view wants for what it last rendered.
    var preferredSize: NSSize {
        guard isExpanded else { return Self.compactSize }
        layoutSubtreeIfNeeded()
        return NSSize(width: Self.expandedWidth, height: ceil(root.fittingSize.height))
    }

    /// Draws the model's structure: title, step rows, approval cards.
    /// Rows are rebuilt only when they changed. Call `animate` for motion.
    func render(_ model: ActivityModel) {
        title.stringValue = Self.title(for: model.phase)
        applyExpanded(model.isExpanded)
        if shownSteps != model.steps {
            shownSteps = model.steps
            replace(stepsStack, with: model.steps.map(stepRow))
        }
        if shownCards != model.approvals {
            shownCards = model.approvals
            replace(cardsStack, with: model.approvals.map(cardView))
        }
        stepsStack.isHidden = model.steps.isEmpty || !isExpanded
        cardsStack.isHidden = model.approvals.isEmpty || !isExpanded
    }

    /// One animation frame for the blob.
    func animate(level: Float, phase: ActivityModel.Phase, time: TimeInterval) {
        blob.update(level: level, phase: phase, time: time)
    }

    static func title(for phase: ActivityModel.Phase) -> String {
        switch phase {
        case .idle: return "Water"
        case .listening: return "Listening"
        case .thinking: return "Thinking"
        case .responding: return "Answering"
        case .needsYou: return "Needs your OK"
        }
    }

    // MARK: -

    private func applyExpanded(_ expanded: Bool) {
        isExpanded = expanded
        title.isHidden = !expanded
        widthConstraint.constant = expanded ? Self.expandedWidth : Self.compactSize.width
    }

    private var contentWidth: CGFloat { Self.expandedWidth - 2 * Self.inset }

    private func replace(_ stack: NSStackView, with views: [NSView]) {
        for v in stack.arrangedSubviews {
            stack.removeArrangedSubview(v)
            v.removeFromSuperview()
        }
        for v in views { stack.addArrangedSubview(v) }
    }

    private static func glyph(_ s: ActivityStep.State) -> String {
        switch s {
        case .running: return "…"
        case .ok: return "✓"
        case .queued: return "⏸"
        case .denied: return "✕"
        case .error: return "!"
        case .other: return "·"
        }
    }

    private func stepRow(_ step: ActivityStep) -> NSView {
        let g = NSTextField(labelWithString: Self.glyph(step.state))
        g.font = .systemFont(ofSize: 12, weight: .semibold)
        g.textColor = step.state == .error || step.state == .denied ? .systemRed : .secondaryLabelColor
        g.alignment = .center
        g.translatesAutoresizingMaskIntoConstraints = false
        g.widthAnchor.constraint(equalToConstant: 16).isActive = true

        let l = plainLabel(step.label, size: 12, color: step.state == .running ? .labelColor : .secondaryLabelColor)
        l.maximumNumberOfLines = 1
        l.lineBreakMode = .byTruncatingTail
        l.preferredMaxLayoutWidth = contentWidth - 24

        let row = NSStackView(views: [g, l])
        row.orientation = .horizontal
        row.spacing = 6
        if step.state == .running {
            let spin = NSProgressIndicator()
            spin.style = .spinning
            spin.controlSize = .small
            spin.isIndeterminate = true
            spin.startAnimation(nil)
            row.insertArrangedSubview(spin, at: 0)
            g.isHidden = true
        }
        return row
    }

    private func cardView(_ card: ApprovalCard) -> NSView {
        var subtitleParts: [String] = []
        if let a = card.action { subtitleParts.append(a) }
        if let r = card.risk { subtitleParts.append(r + " risk") }
        let head = plainLabel("Waiting for your OK", size: 12, color: .systemOrange, weight: .semibold)
        let sub = plainLabel(subtitleParts.joined(separator: " · "), size: 11, color: .secondaryLabelColor)
        sub.isHidden = subtitleParts.isEmpty

        // The daemon's code-built read-back, exactly. Selectable so it can
        // be copied; still plain text.
        let readBack = plainLabel(card.readBack ?? "Open it in the workspace to see exactly what it will do.",
                                  size: 13, color: card.readBack == nil ? .secondaryLabelColor : .labelColor)
        readBack.isSelectable = card.readBack != nil
        readBack.maximumNumberOfLines = 0
        readBack.lineBreakMode = .byWordWrapping
        readBack.preferredMaxLayoutWidth = contentWidth - 20

        let note = plainLabel(card.note ?? "", size: 11, color: .systemRed)
        note.maximumNumberOfLines = 0
        note.preferredMaxLayoutWidth = contentWidth - 20
        note.isHidden = card.note == nil

        let canDecide = !card.submitting && card.payloadHash != nil
        let approve = ActionButton(title: "Approve") { [weak self] in self?.onApprove?(card) }
        approve.keyEquivalent = ""
        approve.bezelColor = .controlAccentColor
        // Approving needs the read-back on screen: never approve unseen text.
        approve.isEnabled = canDecide && card.readBack != nil
        let edit = ActionButton(title: "Edit…") { [weak self] in self?.onEdit?(card) }
        edit.isEnabled = !card.submitting
        let reject = ActionButton(title: "Reject") { [weak self] in self?.onReject?(card) }
        reject.isEnabled = canDecide
        let buttons = NSStackView(views: [approve, edit, reject])
        buttons.orientation = .horizontal
        buttons.spacing = 8
        if card.submitting {
            let spin = NSProgressIndicator()
            spin.style = .spinning
            spin.controlSize = .small
            spin.startAnimation(nil)
            buttons.addArrangedSubview(spin)
        }

        let stack = NSStackView(views: [head, sub, readBack, note, buttons])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 6
        stack.edgeInsets = NSEdgeInsets(top: 10, left: 10, bottom: 10, right: 10)
        stack.wantsLayer = true
        stack.layer?.cornerRadius = 8
        stack.layer?.borderWidth = 1
        stack.layer?.borderColor = NSColor.separatorColor.cgColor
        stack.translatesAutoresizingMaskIntoConstraints = false
        stack.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        return stack
    }

    private func plainLabel(_ s: String, size: CGFloat, color: NSColor, weight: NSFont.Weight = .regular) -> NSTextField {
        let l = NSTextField(wrappingLabelWithString: "")
        l.stringValue = s // plain text only
        l.allowsEditingTextAttributes = false
        l.font = .systemFont(ofSize: size, weight: weight)
        l.textColor = color
        l.isSelectable = false
        return l
    }
}

/// A push button that runs a closure, and works on the first click in the
/// HUD's non-activating panel (no "click once to focus" first).
private final class ActionButton: NSButton {
    private let handler: () -> Void

    init(title: String, handler: @escaping () -> Void) {
        self.handler = handler
        super.init(frame: .zero)
        self.title = title
        bezelStyle = .rounded
        controlSize = .small
        font = .systemFont(ofSize: NSFont.systemFontSize(for: .small))
        target = self
        action = #selector(fire)
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    @objc private func fire() { handler() }
}

/// The blob: one `CAShapeLayer` whose outline breathes with the level and
/// wobbles more the louder it is. Colour follows the phase.
private final class BlobView: NSView {
    private let shape = CAShapeLayer()
    private var shown: CGFloat = 0

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.addSublayer(shape)
        shape.lineWidth = 0
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func layout() {
        super.layout()
        shape.frame = bounds
    }

    func update(level: Float, phase: ActivityModel.Phase, time: TimeInterval) {
        // Ease toward the target so 30Hz level steps look continuous.
        let target = CGFloat(min(1, max(0, level)))
        shown += (target - shown) * 0.35

        let side = min(bounds.width, bounds.height)
        guard side > 0 else { return }
        let c = CGPoint(x: bounds.midX, y: bounds.midY)
        let base = side * 0.34
        let r0 = base * (1 + 0.32 * shown)
        let wobble = 0.03 + 0.11 * shown
        let path = CGMutablePath()
        let n = 64
        for i in 0...n {
            let th = CGFloat(i) / CGFloat(n) * 2 * .pi
            let w = 0.6 * sin(3 * th + CGFloat(time) * 2.1) + 0.4 * sin(5 * th - CGFloat(time) * 1.3)
            let r = r0 * (1 + wobble * w)
            let p = CGPoint(x: c.x + r * cos(th), y: c.y + r * sin(th))
            if i == 0 { path.move(to: p) } else { path.addLine(to: p) }
        }
        path.closeSubpath()

        CATransaction.begin()
        CATransaction.setDisableActions(true)
        shape.path = path
        shape.fillColor = Self.color(phase).cgColor
        CATransaction.commit()
    }

    private static func color(_ p: ActivityModel.Phase) -> NSColor {
        switch p {
        case .idle: return .tertiaryLabelColor
        case .listening: return .systemBlue
        case .thinking: return .systemPurple
        case .responding: return .systemTeal
        case .needsYou: return .systemOrange
        }
    }
}
