import AppKit
import QuartzCore
import WaterClientCore

/// A floating panel that never activates Water or takes focus from the app
/// the CEO is in, but whose buttons still work on the first click.
private final class HUDPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
}

/// The Activity HUD (V-hud): owns the one `ActivityModel`, hosts the one
/// `ActivityView` in a non-activating floating panel next to the AskPanel,
/// and turns the view's clicks into the two existing approval calls.
///
/// AppDelegate feeds it the inputs (hotkey, capture, turn events, speech);
/// every rule about when it shows, expands, pins and hides lives in
/// `ActivityModel` (WaterClientCore, tested), not here.
///
/// Approve and Reject post POST /v1/approvals/{id}/decision with the
/// payload_hash from the `approval_required` event (or the latest re-read):
/// that click *is* the tap for a tap-required envelope. Nothing else
/// executes anything. Edit hands the id to the workspace (`onEdit`), so
/// there's one edit UI, not two. After every turn's end and after every
/// click it re-reads GET /v1/approvals/{id} for each open card, so an
/// envelope decided elsewhere (a spoken yes in another turn, the workspace)
/// disappears here too.
final class ActivityHUD {
    /// Where to sit: the AskPanel's frame (visible or not).
    var anchorFrame: (() -> NSRect)?
    /// Edit: open the workspace at this approval.
    var onEdit: ((String) -> Void)?
    /// A note for the transcript (e.g. an approved action that then
    /// failed). Plain text.
    var onNote: ((String) -> Void)?

    private(set) var model = ActivityModel()
    private let panel: HUDPanel
    private let background = NSVisualEffectView()
    private let view = ActivityView()
    private var timer: Timer?
    private let client: UnixSocketClient
    private let tokens: TokenProvider
    /// Serial: approval calls never race each other.
    private let work = DispatchQueue(label: "water.activity-hud", qos: .userInitiated)
    private let start = CACurrentMediaTime()

    init(client: UnixSocketClient, tokens: TokenProvider) {
        self.client = client
        self.tokens = tokens
        panel = HUDPanel(contentRect: NSRect(origin: .zero, size: ActivityView.compactSize),
                         styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: true)
        panel.isFloatingPanel = true
        panel.level = .floating
        panel.hidesOnDeactivate = false
        panel.becomesKeyOnlyIfNeeded = true
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.isMovableByWindowBackground = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        panel.isReleasedWhenClosed = false

        background.material = .hudWindow
        background.blendingMode = .behindWindow
        background.state = .active
        background.wantsLayer = true
        background.layer?.cornerRadius = 14
        background.layer?.masksToBounds = true
        panel.contentView = background

        view.translatesAutoresizingMaskIntoConstraints = false
        background.addSubview(view)
        NSLayoutConstraint.activate([
            view.leadingAnchor.constraint(equalTo: background.leadingAnchor),
            view.trailingAnchor.constraint(equalTo: background.trailingAnchor),
            view.topAnchor.constraint(equalTo: background.topAnchor),
            view.bottomAnchor.constraint(equalTo: background.bottomAnchor),
        ])
        view.onApprove = { [weak self] card in self?.decide(card, approve: true) }
        view.onReject = { [weak self] card in self?.decide(card, approve: false) }
        view.onEdit = { [weak self] card in self?.onEdit?(card.id) }
    }

    // MARK: inputs (main thread)

    func holdStarted() { update { $0.holdStarted(now: Date()) } }
    func holdEnded() { update { $0.holdEnded(now: Date()) } }
    func cancelled() { update { $0.cancelled(now: Date()) } }

    /// Returns the id every event of this turn must carry.
    func turnSent() -> Int {
        var id = 0
        update { id = $0.turnSent(now: Date()) }
        return id
    }

    func event(_ e: TurnEvent, turn: Int) {
        update { $0.event(e, turn: turn, now: Date()) }
        if e.kind == .done || e.kind == .error { refreshOpenApprovals() }
    }

    func turnEnded(turn: Int) {
        update { $0.turnEnded(turn: turn, now: Date()) }
        refreshOpenApprovals()
    }

    func speechStarted() { update { $0.speechStarted(now: Date()) } }
    func speechIdle() { update { $0.speechIdle(now: Date()) } }
    // Levels arrive at ~30Hz: they only move the blob, drawn by the timer.
    func micLevel(_ v: Float) { model.micLevel(v, now: Date()) }
    func ttsLevel(_ v: Float) { model.ttsLevel(v, now: Date()) }

    // MARK: approvals

    private func decide(_ card: ApprovalCard, approve: Bool) {
        guard let hash = card.payloadHash else { return }
        let id = card.id
        update { $0.approvalSubmitting(id) }
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
                    if let env = o.envelope, env.id == id, !env.isPending {
                        self.update { $0.approvalResolved(id, now: Date()) }
                    }
                    if let err = o.error {
                        let what = card.action ?? "the action"
                        self.onNote?(o.outcomeUnknown
                            ? "Approved \(what), but Water couldn't confirm it ran (\(err)). Check before asking again."
                            : "Approved \(what), but it failed: \(err)")
                    }
                case .failure(let err):
                    self.update { $0.approvalFailed(id, message: ApprovalActions.failureMessage(err)) }
                }
                self.refreshOpenApprovals()
            }
        }
    }

    /// Re-reads every open card: gone or decided elsewhere resolves it;
    /// still pending refreshes its hash and read-back. A read that fails
    /// (daemon down) leaves the card as it was.
    func refreshOpenApprovals() {
        let ids = model.approvals.map(\.id)
        guard !ids.isEmpty else { return }
        let client = self.client, tokens = self.tokens
        work.async { [weak self] in
            guard let token = try? tokens.token() else { return }
            for id in ids {
                // nil is a 404 (gone); a thrown error is "couldn't ask".
                let state: ApprovalState?
                do { state = try client.fetchApproval(id: id, token: token) } catch { continue }
                DispatchQueue.main.async { self?.update { $0.approvalReread(id: id, state: state, now: Date()) } }
            }
        }
    }

    // MARK: display

    /// Applies one model change, then syncs the panel.
    private func update(_ change: (inout ActivityModel) -> Void) {
        change(&model)
        sync()
    }

    private func sync() {
        view.render(model)
        if model.isVisible {
            place()
            if !panel.isVisible { panel.orderFrontRegardless() }
            startTimer()
        } else {
            if panel.isVisible { panel.orderOut(nil) }
            stopTimer()
        }
    }

    /// Next to the AskPanel: to its right, tops aligned; below its right
    /// edge when the screen has no room on the right.
    private func place() {
        let size = view.preferredSize
        let screen = NSScreen.main ?? NSScreen.screens.first
        let vf = screen?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let anchor = anchorFrame?() ?? NSRect(x: vf.midX - 340, y: vf.maxY - vf.height * 0.18 - 64, width: 680, height: 64)
        let gap: CGFloat = 12
        var origin = NSPoint(x: anchor.maxX + gap, y: anchor.maxY - size.height)
        if origin.x + size.width > vf.maxX {
            origin = NSPoint(x: anchor.maxX - size.width, y: anchor.minY - gap - size.height)
        }
        origin.x = min(max(origin.x, vf.minX), vf.maxX - size.width)
        origin.y = min(max(origin.y, vf.minY), vf.maxY - size.height)
        let frame = NSRect(origin: origin, size: size)
        if panel.frame != frame { panel.setFrame(frame, display: true) }
    }

    private func startTimer() {
        guard timer == nil else { return }
        let t = Timer(timeInterval: 1.0 / 30, repeats: true) { [weak self] _ in self?.frame() }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }

    /// One display frame: the clock (auto-dismiss, stall guard), then the
    /// blob. The thinking pulse is time-driven, so it moves with no input.
    private func frame() {
        let now = Date()
        let before = model
        model.tick(now: now)
        if model != before { sync() }
        view.animate(level: model.displayLevel(now: now), phase: model.phase, time: CACurrentMediaTime() - start)
    }
}
