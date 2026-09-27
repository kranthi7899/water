import AppKit
import WaterClientCore

/// The glass tab's content (owner brief 2026-09-26): blue frosted glass over
/// dark navy liquid chrome, showing one `GlassItem` — an email laid out as a
/// message (To / Cc / Subject / Body) or the envelope's exact read-back —
/// with Approve / Edit / Reject for an approval or Close for anything else
/// (a draft, something the model chose to show, a notice). Every item has
/// the ✕ in its header; on an approval it only hides the tab.
///
/// Every string is untrusted (a subject, a body, a recipient's name) and is
/// set as plain `stringValue` / `NSTextView.string` on a non-rich text view:
/// never attributed, never HTML, no link or data detection.
///
/// It draws and reports clicks; it knows nothing about the daemon or the
/// window (GlassPanel hosts it, GlassRender renders it offscreen).
final class GlassTabView: NSView {
    var onApprove: ((GlassItem) -> Void)?
    var onReject: ((GlassItem) -> Void)?
    var onEdit: ((GlassItem) -> Void)?
    var onClose: (() -> Void)?
    /// The pointer entered (true) or left (false) the tab: its linger
    /// count pauses meanwhile (GlassTabState.hover).
    var onHover: ((Bool) -> Void)?
    private var tracking: NSTrackingArea?

    static let width: CGFloat = 460
    static let corner: CGFloat = 18
    private static let pad: CGFloat = 22
    private static let labelColumn: CGFloat = 76
    private static let bodyMin: CGFloat = 72
    private static let bodyMax: CGFloat = 280

    /// Opaque under the chrome: for the offscreen render, where there is no
    /// desktop behind to blur. Live, the chrome sits over the panel's
    /// behind-window blur at `liveAlpha` so the desktop shows through.
    var opaqueBackdrop = false
    static let liveAlpha: CGFloat = 0.86

    private(set) var item: GlassItem?

    private let orb = GlowDot()
    private let title = GlassTabView.label(size: 14, weight: .semibold, color: .white)
    private let subtitle = GlassTabView.label(size: 11, weight: .medium, color: GlassChrome.icy.withAlphaComponent(0.75))
    private let badge = Badge()
    private let closeX = GlassButton(title: "✕", style: .icon)
    private var fieldRows: [(NSTextField, NSTextField)] = []
    private let bodyScroll = NSScrollView()
    private let bodyText = NSTextView()
    /// Recipient warnings (Slice W D4c), above the read-back: never
    /// truncated, never dismissable on its own.
    private let warningBanner = WarningBanner()
    /// `Say “confirm send” or tap Approve` (D5b), under the body.
    private let hint = GlassTabView.label(size: 12.5, weight: .semibold, color: GlassChrome.icy)
    private let note = GlassTabView.label(size: 11.5, weight: .medium, color: NSColor(srgbRed: 1, green: 0.78, blue: 0.72, alpha: 1))
    private let approve = GlassButton(title: "Approve", style: .primary)
    private let edit = GlassButton(title: "Edit", style: .ghost)
    private let reject = GlassButton(title: "Reject", style: .ghost)
    private let close = GlassButton(title: "Close", style: .ghost)
    private var dividers: [CGFloat] = []

    override var isFlipped: Bool { true }

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.cornerRadius = Self.corner
        layer?.masksToBounds = true
        for v in [orb, title, subtitle, badge, closeX, warningBanner, hint, note, approve, edit, reject, close] as [NSView] { addSubview(v) }

        bodyText.isRichText = false
        bodyText.importsGraphics = false
        bodyText.isEditable = false
        bodyText.isSelectable = true
        bodyText.allowsUndo = false
        bodyText.isAutomaticLinkDetectionEnabled = false
        bodyText.isAutomaticDataDetectionEnabled = false
        bodyText.usesFontPanel = false
        bodyText.drawsBackground = false
        bodyText.textColor = NSColor.white.withAlphaComponent(0.94)
        bodyText.font = .systemFont(ofSize: 13.5)
        bodyText.textContainerInset = NSSize(width: 0, height: 2)
        bodyText.textContainer?.lineFragmentPadding = 0
        bodyText.isVerticallyResizable = true
        bodyText.isHorizontallyResizable = false
        bodyText.autoresizingMask = [.width]
        bodyText.selectedTextAttributes = [.backgroundColor: GlassChrome.bright.withAlphaComponent(0.45)]
        let para = NSMutableParagraphStyle()
        para.lineSpacing = 3.5
        bodyText.defaultParagraphStyle = para
        bodyScroll.documentView = bodyText
        bodyScroll.drawsBackground = false
        bodyScroll.hasVerticalScroller = true
        bodyScroll.autohidesScrollers = true
        bodyScroll.scrollerStyle = .overlay
        bodyScroll.borderType = .noBorder
        addSubview(bodyScroll)

        note.maximumNumberOfLines = 3
        hint.maximumNumberOfLines = 0
        hint.lineBreakMode = .byWordWrapping
        hint.cell?.wraps = true
        note.lineBreakMode = .byWordWrapping
        title.lineBreakMode = .byTruncatingTail
        subtitle.lineBreakMode = .byTruncatingTail

        approve.target = self; approve.action = #selector(tapApprove)
        reject.target = self; reject.action = #selector(tapReject)
        edit.target = self; edit.action = #selector(tapEdit)
        close.target = self; close.action = #selector(tapClose)
        closeX.target = self; closeX.action = #selector(tapClose)
        closeX.toolTip = "Close"
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let tracking { removeTrackingArea(tracking) }
        // activeAlways: the panel never activates Water, so it's rarely key.
        let t = NSTrackingArea(rect: .zero, options: [.mouseEnteredAndExited, .activeAlways, .inVisibleRect],
                               owner: self, userInfo: nil)
        addTrackingArea(t)
        tracking = t
    }

    override func mouseEntered(with event: NSEvent) { onHover?(true) }
    override func mouseExited(with event: NSEvent) { onHover?(false) }

    // MARK: content

    func render(_ item: GlassItem) {
        self.item = item
        title.stringValue = item.title
        var sub: [String] = []
        switch item.kind {
        case .approval:
            if let a = item.action { sub.append(a) }
            sub.append("waiting on you")
        case .draft:
            if let a = item.action { sub.append(a) }
            sub.append("saved as a draft, nothing sent")
        case .display:
            sub.append("Water is showing you this")
        case .notice:
            sub.append("voice mode")
        }
        subtitle.stringValue = sub.joined(separator: "  ·  ")
        badge.text = item.risk.map { "\($0.uppercased()) RISK" }
        badge.isHidden = badge.text == nil
        // Every item can be closed; on an approval that only hides the tab.
        closeX.isHidden = false
        closeX.toolTip = item.isApproval ? "Hide (it stays in the workspace)" : "Close"

        for (l, v) in fieldRows { l.removeFromSuperview(); v.removeFromSuperview() }
        fieldRows = []
        if let e = item.email {
            // An approval's recipients are drawn in full, never cut with "…"
            // (GlassItem.emailFields only lays out a list that fits).
            let recipientLines = item.truncatesRecipients ? 2 : 0
            func row(_ name: String, _ value: String, bold: Bool = false, lines: Int = 2) {
                let l = Self.label(size: 11.5, weight: .semibold, color: GlassChrome.icy.withAlphaComponent(0.7))
                l.stringValue = name.uppercased()
                let v = Self.label(size: 13.5, weight: bold ? .semibold : .regular, color: .white)
                v.stringValue = value
                v.maximumNumberOfLines = bold ? 3 : lines
                v.lineBreakMode = lines == 0 ? .byWordWrapping : .byTruncatingTail
                v.cell?.truncatesLastVisibleLine = lines != 0
                v.cell?.wraps = true
                v.isSelectable = true
                addSubview(l); addSubview(v)
                fieldRows.append((l, v))
            }
            row("To", e.to.isEmpty ? "—" : e.to.joined(separator: ", "), lines: recipientLines)
            if !e.cc.isEmpty { row("Cc", e.cc.joined(separator: ", "), lines: recipientLines) }
            row("Subject", e.subject.isEmpty ? "(no subject)" : e.subject, bold: true)
            bodyText.string = e.body
        } else {
            bodyText.string = item.readBack
        }
        bodyText.font = .systemFont(ofSize: item.email == nil ? 14 : 13.5)
        bodyText.textColor = NSColor.white.withAlphaComponent(0.94)
        bodyScroll.contentView.scroll(to: .zero)

        warningBanner.lines = item.warnings
        warningBanner.isHidden = item.warnings.isEmpty
        hint.stringValue = item.confirmHint ?? ""
        hint.isHidden = hint.stringValue.isEmpty

        note.stringValue = item.note ?? (item.submitting ? "Sending your decision…" : "")
        note.isHidden = note.stringValue.isEmpty
        for b in [approve, edit, reject] { b.isHidden = !item.isApproval }
        approve.isEnabled = item.canDecide
        reject.isEnabled = item.canDecide
        close.isHidden = item.isApproval
        needsLayout = true
        needsDisplay = true
    }

    // MARK: layout

    var preferredSize: NSSize { NSSize(width: Self.width, height: layoutAll(apply: false)) }

    override func layout() {
        super.layout()
        _ = layoutAll(apply: true)
    }

    /// One pass for both measuring and placing; returns the total height.
    @discardableResult
    private func layoutAll(apply: Bool) -> CGFloat {
        let W = Self.width, p = Self.pad, inner = W - 2 * p
        var y = p - 2
        var divs: [CGFloat] = []
        func put(_ v: NSView, _ r: NSRect) { if apply { v.frame = r } }

        // Header: glowing dot, title, badge or close.
        put(orb, NSRect(x: p - 3, y: y - 1, width: 22, height: 22))
        var right = W - p
        if !closeX.isHidden { put(closeX, NSRect(x: right - 22, y: y - 1, width: 22, height: 22)); right -= 30 }
        if !badge.isHidden {
            let bw = badge.intrinsicContentSize.width
            put(badge, NSRect(x: right - bw, y: y + 1, width: bw, height: 18)); right -= bw + 8
        }
        put(title, NSRect(x: p + 24, y: y, width: right - (p + 24), height: 20))
        y += 21
        put(subtitle, NSRect(x: p + 24, y: y, width: inner - 24, height: 16))
        y += 16 + 14
        divs.append(y)
        y += 14

        // Recipient warnings, before anything the CEO might approve.
        if !warningBanner.isHidden {
            let bh = warningBanner.height(forWidth: inner)
            put(warningBanner, NSRect(x: p, y: y, width: inner, height: bh))
            y += bh + 14
        }

        // Message fields.
        let valueX = p + Self.labelColumn, valueW = inner - Self.labelColumn
        for (l, v) in fieldRows {
            let natural = v.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: valueW, height: 1000)).height ?? 18
            // 0 lines means "all of it" (an approval's recipients).
            let h = v.maximumNumberOfLines == 0 ? natural : min(natural, CGFloat(v.maximumNumberOfLines) * 19)
            put(l, NSRect(x: p, y: y + 1.5, width: Self.labelColumn - 8, height: 16))
            put(v, NSRect(x: valueX, y: y, width: valueW, height: ceil(h)))
            y += ceil(h) + 9
        }
        if !fieldRows.isEmpty {
            y += 3
            divs.append(y)
            y += 14
        }

        // Body (or read-back), scrollable past bodyMax.
        let textH = bodyHeight(width: inner)
        let bodyH = min(max(textH, fieldRows.isEmpty ? 40 : Self.bodyMin), Self.bodyMax)
        put(bodyScroll, NSRect(x: p, y: y, width: inner, height: bodyH))
        if apply { bodyText.frame = NSRect(x: 0, y: 0, width: inner, height: max(textH, bodyH)) }
        y += bodyH + 16

        if !hint.isHidden {
            let hh = ceil(hint.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: inner, height: 200)).height ?? 17)
            put(hint, NSRect(x: p, y: y, width: inner, height: hh))
            y += hh + 10
        }

        if !note.isHidden {
            let nh = ceil(note.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: inner, height: 60)).height ?? 16)
            put(note, NSRect(x: p, y: y, width: inner, height: nh))
            y += nh + 10
        }

        // Buttons, right-aligned.
        let bh: CGFloat = 32
        var x = W - p
        let buttons: [GlassButton] = (item?.isApproval ?? false) ? [approve, edit, reject] : [close]
        for b in buttons {
            let bw = max(b.intrinsicContentSize.width, 76)
            put(b, NSRect(x: x - bw, y: y, width: bw, height: bh))
            x -= bw + 10
        }
        y += bh + p
        if apply { dividers = divs }
        return ceil(y)
    }

    private func bodyHeight(width: CGFloat) -> CGFloat {
        guard let lm = bodyText.layoutManager, let tc = bodyText.textContainer else { return 40 }
        tc.containerSize = NSSize(width: width, height: .greatestFiniteMagnitude)
        tc.widthTracksTextView = false
        lm.ensureLayout(for: tc)
        return ceil(lm.usedRect(for: tc).height) + 6
    }

    // MARK: drawing

    override func draw(_ dirtyRect: NSRect) {
        guard let ctx = NSGraphicsContext.current?.cgContext else { return }
        let r = bounds
        let shape = NSBezierPath(roundedRect: r, xRadius: Self.corner, yRadius: Self.corner)
        shape.addClip()

        if opaqueBackdrop {
            GlassChrome.deep.setFill()
            r.fill()
        }
        let scale = min(max(abs(ctx.userSpaceToDeviceSpaceTransform.a), 1), 3)
        if let img = GlassChrome.image(size: r.size, scale: scale) {
            ctx.saveGState()
            ctx.setAlpha(opaqueBackdrop ? 1 : Self.liveAlpha)
            // The CGImage is bottom-up; this view is flipped.
            ctx.translateBy(x: 0, y: r.height)
            ctx.scaleBy(x: 1, y: -1)
            ctx.draw(img, in: NSRect(origin: .zero, size: r.size))
            ctx.restoreGState()
        }

        // Blue glass tint, deeper toward the bottom where the text sits.
        let tint = NSGradient(colors: [
            GlassChrome.steel.withAlphaComponent(0.20),
            GlassChrome.navy.withAlphaComponent(0.46),
            GlassChrome.deep.withAlphaComponent(0.58),
        ], atLocations: [0, 0.35, 1], colorSpace: .sRGB)
        tint?.draw(in: r, angle: 90)

        // Glass sheen: a soft bright band along the top edge.
        let sheen = NSGradient(colors: [NSColor.white.withAlphaComponent(0.16), NSColor.white.withAlphaComponent(0)])
        sheen?.draw(in: NSRect(x: 0, y: 0, width: r.width, height: 56), angle: 90)

        // Dividers: hairlines that fade out at both ends.
        for dy in dividers {
            let g = NSGradient(colors: [NSColor.white.withAlphaComponent(0), NSColor.white.withAlphaComponent(0.20),
                                        NSColor.white.withAlphaComponent(0.20), NSColor.white.withAlphaComponent(0)],
                               atLocations: [0, 0.15, 0.85, 1], colorSpace: .sRGB)
            g?.draw(in: NSRect(x: Self.pad, y: dy, width: r.width - 2 * Self.pad, height: 1), angle: 0)
        }

        // Border: a 1px light rim, brighter at the top like lit glass.
        let rim = NSBezierPath(roundedRect: r.insetBy(dx: 0.5, dy: 0.5), xRadius: Self.corner - 0.5, yRadius: Self.corner - 0.5)
        rim.lineWidth = 1
        ctx.saveGState()
        rim.setClip()
        NSColor.white.withAlphaComponent(0.16).setStroke()
        rim.stroke()
        let top = NSBezierPath(roundedRect: r.insetBy(dx: 0.5, dy: 0.5), xRadius: Self.corner - 0.5, yRadius: Self.corner - 0.5)
        top.lineWidth = 1
        NSRect(x: 0, y: 0, width: r.width, height: r.height * 0.4).clip()
        GlassChrome.icy.withAlphaComponent(0.30).setStroke()
        top.stroke()
        ctx.restoreGState()
    }

    // MARK: clicks

    @objc private func tapApprove() { if let i = item, i.canDecide { onApprove?(i) } }
    @objc private func tapReject() { if let i = item, i.canDecide { onReject?(i) } }
    @objc private func tapEdit() { if let i = item { onEdit?(i) } }
    @objc private func tapClose() { onClose?() }

    static func label(size: CGFloat, weight: NSFont.Weight, color: NSColor) -> NSTextField {
        let l = NSTextField(labelWithString: "")
        l.font = .systemFont(ofSize: size, weight: weight)
        l.textColor = color
        l.allowsEditingTextAttributes = false
        l.drawsBackground = false
        l.isBezeled = false
        return l
    }
}

/// The recipient-warning banner: an amber-tinted panel with one "⚠" line per
/// warning, wrapped and drawn in full (never truncated). Plain text only.
private final class WarningBanner: NSView {
    static let amber = NSColor(srgbRed: 1.0, green: 0.74, blue: 0.30, alpha: 1)
    private static let inset = NSSize(width: 12, height: 9)
    private let text = GlassTabView.label(size: 12.5, weight: .semibold, color: NSColor(srgbRed: 1, green: 0.90, blue: 0.72, alpha: 1))

    var lines: [String] = [] {
        didSet { text.stringValue = lines.map { "\u{26A0}\u{FE0E}  \($0)" }.joined(separator: "\n") ; needsLayout = true }
    }

    override var isFlipped: Bool { true }

    override init(frame: NSRect) {
        super.init(frame: frame)
        text.maximumNumberOfLines = 0
        text.lineBreakMode = .byWordWrapping
        text.cell?.wraps = true
        text.cell?.truncatesLastVisibleLine = false
        text.isSelectable = true
        addSubview(text)
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    func height(forWidth w: CGFloat) -> CGFloat {
        let tw = w - 2 * Self.inset.width
        let th = text.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: tw, height: 10_000)).height ?? 17
        return ceil(th) + 2 * Self.inset.height
    }

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        text.frame = bounds.insetBy(dx: Self.inset.width, dy: Self.inset.height)
    }

    override func draw(_ dirtyRect: NSRect) {
        let r = bounds.insetBy(dx: 0.5, dy: 0.5)
        let shape = NSBezierPath(roundedRect: r, xRadius: 10, yRadius: 10)
        Self.amber.withAlphaComponent(0.16).setFill()
        shape.fill()
        Self.amber.withAlphaComponent(0.60).setStroke()
        shape.lineWidth = 1
        shape.stroke()
    }
}

/// The header's small glowing orb, echoing the globe.
private final class GlowDot: NSView {
    override func draw(_ dirtyRect: NSRect) {
        guard let ctx = NSGraphicsContext.current?.cgContext else { return }
        let c = CGPoint(x: bounds.midX, y: bounds.midY)
        GlassChrome.radial(ctx, at: c, radius: bounds.width / 2, GlassChrome.icy.withAlphaComponent(0.55))
        let core = NSBezierPath(ovalIn: bounds.insetBy(dx: 5, dy: 5))
        NSGradient(colors: [.white, GlassChrome.icy, GlassChrome.bright])?.draw(in: core, relativeCenterPosition: NSPoint(x: -0.3, y: 0.3))
    }
}

/// The risk pill in the header.
private final class Badge: NSView {
    var text: String? { didSet { needsDisplay = true; invalidateIntrinsicContentSize() } }
    private var attrs: [NSAttributedString.Key: Any] {
        [.font: NSFont.systemFont(ofSize: 9.5, weight: .bold), .foregroundColor: NSColor.white.withAlphaComponent(0.92), .kern: 0.6]
    }
    override var intrinsicContentSize: NSSize {
        NSSize(width: ceil((text ?? "").size(withAttributes: attrs).width) + 18, height: 18)
    }
    override func draw(_ dirtyRect: NSRect) {
        guard let text else { return }
        let r = bounds.insetBy(dx: 0.5, dy: 0.5)
        let pill = NSBezierPath(roundedRect: r, xRadius: r.height / 2, yRadius: r.height / 2)
        GlassChrome.bright.withAlphaComponent(0.22).setFill()
        pill.fill()
        GlassChrome.icy.withAlphaComponent(0.45).setStroke()
        pill.lineWidth = 1
        pill.stroke()
        let s = text.size(withAttributes: attrs)
        text.draw(at: NSPoint(x: bounds.midX - s.width / 2, y: bounds.midY - s.height / 2), withAttributes: attrs)
    }
}

/// A glass pill button, drawn by hand so it matches the tab in any
/// appearance and renders the same offscreen.
final class GlassButton: NSButton {
    enum Style { case primary, ghost, icon }
    let style: Style

    init(title: String, style: Style) {
        self.style = style
        super.init(frame: .zero)
        self.title = title
        isBordered = false
        bezelStyle = .regularSquare
        setButtonType(.momentaryChange)
        focusRingType = .none
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override var isEnabled: Bool { didSet { needsDisplay = true } }
    override var isFlipped: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    private var pillFont: NSFont { .systemFont(ofSize: style == .icon ? 11 : 13, weight: style == .primary ? .semibold : .medium) }

    override var intrinsicContentSize: NSSize {
        let w = title.size(withAttributes: [.font: pillFont]).width
        return NSSize(width: ceil(w) + (style == .icon ? 10 : 36), height: style == .icon ? 22 : 32)
    }

    override func draw(_ dirtyRect: NSRect) {
        let r = bounds.insetBy(dx: 0.5, dy: 0.5)
        let pill = NSBezierPath(roundedRect: r, xRadius: r.height / 2, yRadius: r.height / 2)
        let down = isHighlighted
        let alpha: CGFloat = isEnabled ? 1 : 0.45
        switch style {
        case .primary:
            NSGradient(colors: [
                GlassChrome.icy.blended(withFraction: 0.35, of: GlassChrome.bright)!.withAlphaComponent(alpha),
                GlassChrome.bright.withAlphaComponent(alpha),
                GlassChrome.steel.withAlphaComponent(alpha),
            ])?.draw(in: pill, angle: down ? 90 : -90)
            NSGraphicsContext.saveGraphicsState()
            pill.addClip()
            NSGradient(colors: [NSColor.white.withAlphaComponent(0.35 * alpha), NSColor.white.withAlphaComponent(0)])?
                .draw(in: NSRect(x: r.minX, y: r.minY, width: r.width, height: r.height * 0.5), angle: 90)
            NSGraphicsContext.restoreGraphicsState()
            NSColor.white.withAlphaComponent(0.55 * alpha).setStroke()
        case .ghost, .icon:
            NSColor.white.withAlphaComponent((down ? 0.20 : 0.09) * alpha).setFill()
            pill.fill()
            NSColor.white.withAlphaComponent(0.26 * alpha).setStroke()
        }
        pill.lineWidth = 1
        pill.stroke()

        let color: NSColor = style == .primary ? NSColor(srgbRed: 0.02, green: 0.07, blue: 0.16, alpha: alpha)
                                               : NSColor.white.withAlphaComponent(0.92 * alpha)
        let attrs: [NSAttributedString.Key: Any] = [.font: pillFont, .foregroundColor: color]
        let s = title.size(withAttributes: attrs)
        title.draw(at: NSPoint(x: bounds.midX - s.width / 2, y: bounds.midY - s.height / 2), withAttributes: attrs)
    }
}
