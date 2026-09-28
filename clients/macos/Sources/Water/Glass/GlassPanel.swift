import AppKit
import QuartzCore
import WaterClientCore

/// A borderless, transparent, non-activating floating panel: it never takes
/// focus from the app the CEO is in, and becomes key only when one of its
/// buttons (or the body text) is clicked.
final class GlassPanel: NSPanel {
    init(size: NSSize) {
        super.init(contentRect: NSRect(origin: .zero, size: size),
                   styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: true)
        isFloatingPanel = true
        level = .floating
        hidesOnDeactivate = false
        becomesKeyOnlyIfNeeded = true
        isOpaque = false
        backgroundColor = .clear
        hasShadow = true
        isMovableByWindowBackground = true
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        isReleasedWhenClosed = false
        appearance = NSAppearance(named: .darkAqua)
        animationBehavior = .none
    }

    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
}

/// The glass tab (owner brief 2026-09-26): owns one `GlassTabState` and
/// the panel that shows it. AppDelegate feeds it each turn event with its
/// channel; `GlassItem.shouldShow` (WaterClientCore, tested) decides what
/// shows, `GlassTabState` when it hides.
///
/// Approve and Reject post POST /v1/approvals/{id}/decision with the
/// payload_hash the tab's text came with — the same `decideApproval` the
/// old Activity HUD used; that click is the tap for a tap-required envelope.
/// Edit hands the id to the workspace (`onEdit`). An approval for an
/// outward message is re-read (GET /v1/approvals/{id}) to lay out its
/// payload; every re-read replaces text and hash together.
final class GlassTabController {
    /// Edit: open the workspace at this approval.
    var onEdit: ((String) -> Void)?
    /// A plain-text note for the transcript (an approved action that failed).
    var onNote: ((String) -> Void)?
    /// Called after a click here decided an approval, so the globe can re-read.
    var onDecided: ((String) -> Void)?
    /// Where to sit: centred on the globe, above it (below when there's no
    /// room), or under the docked mini orb (OverlayLayout).
    var placement: ((NSSize) -> NSPoint)?
    /// The ✕ hid an approval without deciding it (it stays pending in the
    /// workspace): the globe stops pinning on it.
    var onApprovalHidden: ((String) -> Void)?

    private(set) var state = GlassTabState()
    private let panel: GlassPanel
    private let host = NSView()
    private let blur = NSVisualEffectView()
    private let view = GlassTabView(frame: NSRect(x: 0, y: 0, width: GlassTabView.width, height: 200))
    private let client: UnixSocketClient
    private let tokens: TokenProvider
    private let work = DispatchQueue(label: "water.glass-tab", qos: .userInitiated)
    private var timer: Timer?
    private var shown: GlassItem?

    init(client: UnixSocketClient, tokens: TokenProvider) {
        self.client = client
        self.tokens = tokens
        panel = GlassPanel(size: NSSize(width: GlassTabView.width, height: 200))

        host.wantsLayer = true
        host.layer?.cornerRadius = GlassTabView.corner
        host.layer?.masksToBounds = true
        blur.material = .hudWindow
        blur.blendingMode = .behindWindow
        blur.state = .active
        blur.frame = host.bounds
        blur.autoresizingMask = [.width, .height]
        view.frame = host.bounds
        view.autoresizingMask = [.width, .height]
        host.addSubview(blur)
        host.addSubview(view)
        panel.contentView = host

        view.onApprove = { [weak self] item in self?.decide(item, approve: true) }
        view.onReject = { [weak self] item in self?.decide(item, approve: false) }
        view.onEdit = { [weak self] item in if let id = item.approvalID { self?.onEdit?(id) } }
        view.onClose = { [weak self] in
            guard let self else { return }
            let hidden = self.state.item?.approvalID
            self.update { $0.close() }
            if let hidden { self.onApprovalHidden?(hidden) }
        }
        view.onHover = { [weak self] inside in self?.update { $0.hover(inside, now: Date()) } }
    }

    // MARK: inputs (main thread)

    /// One turn event with its turn's channel: shows what
    /// `GlassItem.shouldShow` picks, and re-reads an open approval when the
    /// turn ends.
    func event(_ e: TurnEvent, channel: Channel) {
        if let item = GlassItem.shouldShow(channel: channel, event: e) { show(item) }
        if e.kind == .done || e.kind == .error { refresh() }
    }

    /// Shows an item (from `GlassItem.shouldShow(channel:event:)`).
    func show(_ item: GlassItem) {
        update { $0.show(item, now: Date()) }
        if item.isApproval { refresh() }
    }

    /// Re-reads the open approval (call on turn end, and after the HUD
    /// decided something): gone or decided hides the tab.
    func refresh() {
        guard let cur = state.item, let id = cur.approvalID else { return }
        let client = self.client, tokens = self.tokens
        work.async { [weak self] in
            guard let token = try? tokens.token() else { return }
            let body: Data?
            do { body = try client.fetchApprovalBody(id: id, token: token) } catch { return }
            DispatchQueue.main.async {
                guard let self, let now = self.state.item, now.approvalID == id else { return }
                self.update { $0.approvalReread(id: id, item: now.applyingApproval(body: body)) }
            }
        }
    }

    /// A short plain-text note (a voice-mode hint or error), shown briefly
    /// in place of popping up the text bar. Never covers an approval.
    func notice(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        show(GlassItem.notice(t))
    }

    /// An approval decided somewhere else (the HUD, a spoken yes).
    func resolved(id: String) { update { $0.resolved(id: id) } }

    func hide() { update { $0.close() } }

    // MARK: approvals

    private func decide(_ item: GlassItem, approve: Bool) {
        guard let id = item.approvalID, let hash = item.payloadHash else { return }
        update { $0.submitting(id: id) }
        let client = self.client, tokens = self.tokens
        work.async { [weak self] in
            let result: Result<DecisionOutcome, Error>
            do {
                let token = try tokens.token()
                result = .success(try client.decideApproval(id: id, payloadHash: hash, approve: approve, token: token))
            } catch {
                result = .failure(error)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let o):
                    if let env = o.envelope, env.id == id, !env.isPending { self.update { $0.resolved(id: id) } }
                    if let err = o.error {
                        let what = item.action ?? "the action"
                        self.onNote?(o.outcomeUnknown
                            ? "Approved \(what), but Water couldn't confirm it ran (\(err)). Check before asking again."
                            : "Approved \(what), but it failed: \(err)")
                    }
                    self.onDecided?(id)
                case .failure(let err):
                    self.update { $0.failed(id: id, message: ApprovalActions.failureMessage(err)) }
                }
                self.refresh()
            }
        }
    }

    // MARK: display

    private func update(_ change: (inout GlassTabState) -> Void) {
        change(&state)
        sync()
    }

    private func sync() {
        guard let item = state.item else {
            shown = nil
            stopTimer()
            fadeOut()
            return
        }
        if item != shown {
            shown = item
            view.render(item)
            let size = view.preferredSize
            let origin = placement?(size) ?? defaultOrigin(size)
            panel.setFrame(NSRect(origin: origin, size: size), display: true)
            view.needsLayout = true
        }
        if !panel.isVisible || panel.alphaValue < 1 { fadeIn() }
        startTimer()
    }

    /// The globe moved (a drag, dock or undock): an open tab follows it.
    func reposition() {
        guard shown != nil, panel.isVisible else { return }
        let size = panel.frame.size
        let origin = placement?(size) ?? defaultOrigin(size)
        if panel.frame.origin != origin { panel.setFrameOrigin(origin) }
    }

    private func defaultOrigin(_ size: NSSize) -> NSPoint {
        GlobeHUD.glassOrigin(for: size)
    }

    private func fadeIn() {
        if !panel.isVisible {
            panel.alphaValue = 0
            panel.orderFrontRegardless()
        }
        NSAnimationContext.runAnimationGroup { ctx in
            ctx.duration = 0.22
            panel.animator().alphaValue = 1
        }
    }

    private func fadeOut() {
        guard panel.isVisible else { return }
        NSAnimationContext.runAnimationGroup({ ctx in
            ctx.duration = 0.28
            panel.animator().alphaValue = 0
        }, completionHandler: { [weak self] in
            guard let self, self.state.item == nil else { return }
            self.panel.orderOut(nil)
        })
    }

    private func startTimer() {
        guard timer == nil else { return }
        let t = Timer(timeInterval: 1, repeats: true) { [weak self] _ in
            guard let self else { return }
            let before = self.state
            self.state.tick(now: Date())
            if self.state != before { self.sync() }
        }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }
}
