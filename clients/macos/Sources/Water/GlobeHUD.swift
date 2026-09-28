import AppKit
import CoreGraphics
import QuartzCore
import WaterClientCore

/// The globe's window: borderless, clear, floating over everything
/// (menu-bar level) on every Space and beside full-screen apps. It never
/// becomes key or main, so it never takes focus: typing in other apps is
/// untouched, and Space/Esc stay voice mode's Carbon hot keys.
private final class GlobePanel: NSPanel {
    override var canBecomeKey: Bool { false }
    override var canBecomeMain: Bool { false }
}

/// The floating globe's content view: it takes the mouse only on the
/// orb's circle (OverlayLayout.orbContains); the glow margin hit-tests to
/// nothing. It accepts the first click (the app is never active) and
/// never becomes first responder, so no key event is ever routed here.
private final class OrbHitView: NSView {
    var orbDiameter: CGFloat = 72
    var onMouseDown: ((NSEvent) -> Void)?
    var onMouseDragged: ((NSEvent) -> Void)?
    var onMouseUp: ((NSEvent) -> Void)?

    override func hitTest(_ point: NSPoint) -> NSView? {
        let p = superview.map { convert(point, from: $0) } ?? point
        guard bounds.contains(p), OverlayLayout.orbContains(p, globe: bounds, orbDiameter: orbDiameter) else { return nil }
        return self
    }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override var acceptsFirstResponder: Bool { false }
    override var mouseDownCanMoveWindow: Bool { false }

    override func mouseDown(with e: NSEvent) { onMouseDown?(e) }
    override func mouseDragged(with e: NSEvent) { onMouseDragged?(e) }
    override func mouseUp(with e: NSEvent) { onMouseUp?(e) }
    override func rightMouseDown(with e: NSEvent) {} // never forwarded to what's behind by accident
}

/// A menu item that runs a closure.
private final class ActionItem: NSMenuItem {
    private let run: () -> Void
    init(_ title: String, state: NSControl.StateValue = .off, enabled: Bool = true, _ run: @escaping () -> Void) {
        self.run = run
        super.init(title: title, action: #selector(fire), keyEquivalent: "")
        target = self
        self.state = state
        isEnabled = enabled
    }

    required init(coder: NSCoder) { fatalError("not used") }
    @objc private func fire() { run() }
}

/// The small globe (owner brief 2026-09-26), shown only during an
/// interaction (hold or text submit until the reply ends plus
/// `ActivityModel.dismissDelay`), while a voice approval waits in the glass
/// tab, or all through voice mode (`keepVisible`).
///
/// Interaction (2026-09-26, docs/slices/V.md §11):
/// - Where: by default centred on the menu-bar screen with the orb about
///   three inches above the bottom (OverlayLayout); wherever the owner
///   dragged it last, per screen (`GlobePrefs`, UserDefaults).
/// - Hover: while the pointer is over the orb's circle it brightens
///   (GlobeLook.hover). The pointer is polled at 60Hz while the globe is up,
///   which also switches `ignoresMouseEvents` so ONLY the orb's circle
///   takes clicks — everywhere else (the glow, the window's corners) the
///   window is click-through.
/// - Click-and-drag moves it like a file (GlobeDrag), clamped to the
///   screen; releasing settles the orb inside the visible frame and saves
///   the spot. A single click without movement does nothing.
/// - Pushing the orb up into the menu bar docks it: the floating globe
///   hides and a small live orb shows in the menu bar (GlobeDockItem);
///   clicking that undocks.
/// - Double-click: a small menu — Transparent mode (✓, saved), Go to panel
///   (dock), Quit voice mode (exactly Esc).
///
/// AppDelegate feeds it the inputs (hotkey, capture, turn events, speech,
/// mic and TTS levels). Every rule about when it shows and hides lives in
/// `ActivityModel` (WaterClientCore, tested); the globe's look is driven
/// from the model's phase and level by `GlobeModel`/`GlobeSmoother`.
///
/// It still re-reads GET /v1/approvals/{id} for every approval pinning it,
/// after each turn ends and whenever asked (after a glass-tab click), so an
/// envelope decided elsewhere (a spoken yes, the workspace) unpins it.
final class GlobeHUD {
    /// Orb diameter in points (the window is larger, for the glow).
    static let orbDiameter: CGFloat = 72
    /// How often the pointer is polled for hover and hit-testing.
    static let pointerPollInterval: TimeInterval = 1.0 / 60
    /// Called when a re-read resolved at least one approval, so the glass
    /// tab can re-read too.
    var onApprovalsResolved: (() -> Void)?
    /// The menu's "Quit voice mode": AppDelegate runs exactly the Esc path.
    var onQuitVoiceMode: (() -> Void)?
    /// Whether voice mode is on (the menu enables "Quit voice mode" by it).
    var isVoiceModeOn: (() -> Bool)?
    /// The globe moved or docked/undocked: the glass tab re-places itself.
    var onMoved: (() -> Void)?
    /// Where the pointer is, in screen coordinates (injectable for
    /// `--globe-selftest`).
    var pointer: () -> CGPoint = { NSEvent.mouseLocation }

    private(set) var model = ActivityModel()
    /// Voice mode (⌃⌥V) keeps the globe up — idle while it waits for Space
    /// — on top of the model's own rules; clearing it lets the model decide
    /// again (so it fades out unless a turn or approval still holds it).
    var keepVisible = false {
        didSet { if keepVisible != oldValue { sync() } }
    }
    /// The text bar's frame while it's open, so the globe never covers it
    /// (OverlayLayout moves the globe above it).
    var avoidFrame: (() -> NSRect?)?
    private let panel: GlobePanel
    private let hitView: OrbHitView
    private let globe: GlobeView
    private let dockItem = GlobeDockItem()
    private let side: CGFloat
    private var timer: Timer?
    private var pointerTimer: Timer?
    /// The globe should be up (floating, or docked in the menu bar).
    private var showing = false
    /// The floating panel is up (showing and not docked).
    private var floating = false
    private var hovering = false
    private var drag: GlobeDrag?
    private var prefs: GlobePrefs
    private let defaults: GlobePrefsStore
    private let client: UnixSocketClient
    private let tokens: TokenProvider
    private let work = DispatchQueue(label: "water.globe-hud", qos: .userInitiated)

    init(client: UnixSocketClient, tokens: TokenProvider, defaults: GlobePrefsStore = UserDefaults.standard) {
        self.client = client
        self.tokens = tokens
        self.defaults = defaults
        prefs = GlobePrefs(defaults: defaults)
        side = GlobeView.side(forOrbDiameter: Self.orbDiameter)
        let frame = NSRect(x: 0, y: 0, width: side, height: side)
        panel = GlobePanel(contentRect: frame, styleMask: [.borderless, .nonactivatingPanel],
                           backing: .buffered, defer: true)
        panel.isFloatingPanel = true
        panel.level = .statusBar
        panel.hidesOnDeactivate = false
        panel.becomesKeyOnlyIfNeeded = true
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = false
        // Click-through until the pointer is on the orb (pointer poll).
        panel.ignoresMouseEvents = true
        panel.isMovable = false // moved by GlobeDrag only, never by AppKit
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle, .stationary]
        panel.isReleasedWhenClosed = false
        panel.animationBehavior = .none

        hitView = OrbHitView(frame: frame)
        hitView.orbDiameter = Self.orbDiameter
        globe = GlobeView(frame: frame)
        globe.autoresizingMask = [.width, .height]
        globe.isActive = false
        hitView.addSubview(globe)
        panel.contentView = hitView

        let input: () -> (GlobeState, Float) = { [weak self] in
            guard let self else { return (.idle, 0) }
            return (GlobeState(phase: self.model.phase), self.model.displayLevel(now: Date()))
        }
        globe.inputProvider = input
        dockItem.inputProvider = input
        dockItem.onClick = { [weak self] in self?.undock() }
        applyLook()

        hitView.onMouseDown = { [weak self] e in self?.mouseDown(e) }
        hitView.onMouseDragged = { [weak self] _ in self?.mouseDragged() }
        hitView.onMouseUp = { [weak self] _ in self?.mouseUp() }
    }

    // MARK: inputs (main thread)

    func holdStarted() { update { $0.holdStarted(now: Date()) } }
    func holdEnded() { update { $0.holdEnded(now: Date()) } }
    func cancelled() { update { $0.cancelled(now: Date()) } }
    /// The transcript is queued behind the speech models' load: keep thinking.
    func transcriptWarmingUp() { update { $0.transcriptWarmingUp(now: Date()) } }

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

    /// Esc left voice mode: the turn and speech were cancelled, so hide now
    /// (an approval still pinned stays; Esc never closes one).
    func dismissNow() {
        keepVisible = false
        update { $0.dismissNow(now: Date()) }
    }

    /// The glass tab's ✕ hid this approval without deciding it: stop
    /// pinning on it (it stays pending in the workspace).
    func approvalHidden(_ id: String) { update { $0.approvalResolved(id, now: Date()) } }

    func speechStarted() { update { $0.speechStarted(now: Date()) } }
    func speechIdle() { update { $0.speechIdle(now: Date()) } }
    // Levels arrive at ~30Hz: the globe polls the model every frame.
    func micLevel(_ v: Float) { model.micLevel(v, now: Date()) }
    func ttsLevel(_ v: Float) { model.ttsLevel(v, now: Date()) }

    /// Re-reads every approval pinning the globe: gone or decided elsewhere
    /// resolves it; still pending keeps it. A failed read (daemon down)
    /// leaves it as it was.
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
                DispatchQueue.main.async {
                    guard let self else { return }
                    let before = self.model.approvals.count
                    self.update { $0.approvalReread(id: id, state: state, now: Date()) }
                    if self.model.approvals.count < before { self.onApprovalsResolved?() }
                }
            }
        }
    }

    // MARK: display

    private func update(_ change: (inout ActivityModel) -> Void) {
        change(&model)
        sync()
    }

    private func sync() {
        let wanted = model.isVisible || keepVisible
        if wanted {
            let appearing = !showing
            showing = true
            if prefs.docked {
                hideFloating()
                if !dockItem.isShown {
                    dockItem.show(state: GlobeState(phase: model.phase))
                    onMoved?()
                }
            } else {
                dockItem.hide()
                showFloating(reset: appearing || !floating)
            }
            startTimer()
        } else {
            stopTimer()
            guard showing else { return }
            showing = false
            dockItem.hide()
            hideFloating()
        }
    }

    private func showFloating(reset: Bool) {
        if drag == nil { place() }
        guard !floating else { return }
        floating = true
        if reset { globe.reset(to: GlobeState(phase: model.phase)) }
        globe.isActive = true
        if !panel.isVisible { panel.alphaValue = 0 }
        panel.orderFrontRegardless()
        NSAnimationContext.runAnimationGroup { ctx in
            ctx.duration = 0.2
            panel.animator().alphaValue = 1
        }
        startPointerPoll()
    }

    private func hideFloating() {
        stopPointerPoll()
        drag = nil
        setHover(false)
        panel.ignoresMouseEvents = true
        guard floating else { return }
        floating = false
        NSAnimationContext.runAnimationGroup({ ctx in
            ctx.duration = 0.3
            panel.animator().alphaValue = 0
        }, completionHandler: { [weak self] in
            guard let self, !self.floating else { return }
            self.panel.orderOut(nil)
            self.globe.isActive = false
        })
    }

    /// Where the floating globe goes: its remembered spot on its screen,
    /// or the default (OverlayLayout), clear of the text bar.
    private func place() {
        let frame = targetFrame()
        if panel.frame != frame { panel.setFrame(frame, display: false) }
    }

    private func targetFrame() -> NSRect {
        let (id, vf) = Self.homeScreen(prefs: prefs)
        let avoid = avoidFrame?() ?? nil
        if let id, let c = prefs.orbCenter(screen: id, visible: vf) {
            return OverlayLayout.globeFrame(orbCenter: c, side: side, orbDiameter: Self.orbDiameter, visible: vf, avoiding: avoid)
        }
        return OverlayLayout.globeFrame(visible: vf, side: side, orbDiameter: Self.orbDiameter, avoiding: avoid)
    }

    /// The screen the globe was last left on if it's still connected, else
    /// the one with the menu bar: its id and visible frame.
    private static func homeScreen(prefs: GlobePrefs) -> (String?, NSRect) {
        if let last = prefs.lastScreen, let s = NSScreen.screens.first(where: { screenID($0) == last }) {
            return (last, s.visibleFrame)
        }
        guard let s = NSScreen.screens.first ?? NSScreen.main else { return (nil, visibleFrame) }
        return (screenID(s), s.visibleFrame)
    }

    /// A stable id for a display (its UUID; the display number otherwise).
    static func screenID(_ s: NSScreen) -> String? {
        guard let n = s.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber else { return nil }
        let did = CGDirectDisplayID(n.uint32Value)
        if let u = CGDisplayCreateUUIDFromDisplayID(did)?.takeRetainedValue(),
           let str = CFUUIDCreateString(nil, u) as String? {
            return str
        }
        return n.stringValue
    }

    /// The screen with the menu bar's visible frame.
    static var visibleFrame: NSRect {
        (NSScreen.screens.first ?? NSScreen.main)?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
    }

    /// The model's clock (auto-dismiss, stall guard). The globe animates
    /// itself; this only decides when it hides.
    private func startTimer() {
        guard timer == nil else { return }
        let t = Timer(timeInterval: 0.1, repeats: true) { [weak self] _ in
            guard let self else { return }
            let before = self.model
            self.model.tick(now: Date())
            if self.model != before { self.sync() }
        }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }

    // MARK: pointer: hover and click-through

    /// Polls the pointer while the floating globe is up: on the orb's
    /// circle the panel takes the mouse and the orb brightens; anywhere
    /// else the panel is click-through. (A tracking area can't do this: a
    /// window that ignores mouse events gets no tracking events either.)
    private func startPointerPoll() {
        guard pointerTimer == nil else { return }
        let t = Timer(timeInterval: Self.pointerPollInterval, repeats: true) { [weak self] _ in self?.pollPointer() }
        RunLoop.main.add(t, forMode: .common)
        pointerTimer = t
        pollPointer()
    }

    private func stopPointerPoll() {
        pointerTimer?.invalidate()
        pointerTimer = nil
    }

    private func pollPointer() {
        guard floating else { return }
        let over = OverlayLayout.orbContains(pointer(), globe: panel.frame, orbDiameter: Self.orbDiameter)
        // A button already held when the pointer arrives (another app's
        // drag or text selection passing over the orb) keeps it
        // click-through: the globe only takes presses that start on it.
        let arrivingPressed = panel.ignoresMouseEvents && NSEvent.pressedMouseButtons != 0
        let takesMouse = drag != nil || (over && !arrivingPressed)
        if panel.ignoresMouseEvents == takesMouse { panel.ignoresMouseEvents = !takesMouse }
        setHover(takesMouse)
    }

    private func setHover(_ on: Bool) {
        guard on != hovering else { return }
        hovering = on
        applyLook()
    }

    private func applyLook() {
        globe.look = GlobeLook(hover: hovering, transparent: prefs.transparent)
        dockItem.look = GlobeLook(hover: false, transparent: prefs.transparent)
    }

    // MARK: drag, dock, menu

    private func mouseDown(_ e: NSEvent) {
        let p = pointer()
        let scr = Self.screen(containing: p) ?? panel.screen
        var d = GlobeDrag(orbDiameter: Self.orbDiameter, screen: scr?.frame ?? panel.frame,
                          visible: scr?.visibleFrame ?? Self.visibleFrame)
        switch d.mouseDown(at: p, frame: panel.frame, clickCount: e.clickCount) {
        case .menu:
            drag = nil
            showMenu()
        default:
            drag = d
        }
    }

    private func mouseDragged() {
        guard var d = drag else { return }
        let p = pointer()
        // Dragging onto another screen clamps to that one.
        if let s = Self.screen(containing: p), s.frame != d.screen {
            d = d.retargeted(screen: s.frame, visible: s.visibleFrame)
        }
        let out = d.mouseDragged(to: p)
        drag = d
        switch out {
        case .move(let f):
            panel.setFrameOrigin(f.origin)
            onMoved?()
        case .dock:
            drag = nil
            dock()
        default: break
        }
    }

    private func mouseUp() {
        guard var d = drag else { return }
        let out = d.mouseUp(at: pointer())
        drag = nil
        guard case .settle(let f) = out else { return }
        let center = CGPoint(x: f.midX, y: f.midY)
        if let s = Self.screen(containing: center) ?? panel.screen, let id = Self.screenID(s) {
            prefs.remember(orbCenter: center, visible: s.visibleFrame, screen: id)
            prefs.save(to: defaults)
        }
        if f != panel.frame {
            NSAnimationContext.runAnimationGroup { ctx in
                ctx.duration = 0.18
                ctx.timingFunction = CAMediaTimingFunction(name: .easeOut)
                panel.animator().setFrame(f, display: true)
            }
        }
        onMoved?()
    }

    private static func screen(containing p: CGPoint) -> NSScreen? {
        NSScreen.screens.first { NSMouseInRect(p, $0.frame, false) }
    }

    private func showMenu() {
        let menu = NSMenu()
        menu.autoenablesItems = false
        menu.addItem(ActionItem("Transparent mode", state: prefs.transparent ? .on : .off) { [weak self] in
            self?.setTransparent(!(self?.prefs.transparent ?? false))
        })
        menu.addItem(ActionItem("Go to panel") { [weak self] in self?.dock() })
        menu.addItem(.separator())
        menu.addItem(ActionItem("Quit voice mode", enabled: isVoiceModeOn?() ?? false) { [weak self] in
            self?.onQuitVoiceMode?()
        })
        // Just right of the orb, level with its centre.
        let at = NSPoint(x: hitView.bounds.midX + Self.orbDiameter / 2 + 4, y: hitView.bounds.midY)
        menu.popUp(positioning: nil, at: at, in: hitView)
        pollPointer()
    }

    private func setTransparent(_ on: Bool) {
        prefs.transparent = on
        prefs.save(to: defaults)
        applyLook()
    }

    /// Into the menu bar: the floating globe hides, the mini orb shows
    /// (while the globe is showing at all). Remembered across launches.
    func dock() {
        guard !prefs.docked else { return }
        prefs.docked = true
        prefs.save(to: defaults)
        sync()
        onMoved?()
    }

    /// Back out of the menu bar, at the remembered spot.
    func undock() {
        guard prefs.docked else { return }
        prefs.docked = false
        prefs.save(to: defaults)
        dockItem.hide()
        sync()
        onMoved?()
    }

    // MARK: glass tab placement

    /// The glass tab's origin: centred on the orb, above it (below when
    /// there's no room), on the globe's screen (OverlayLayout). Docked, it
    /// hangs under the mini orb in the menu bar. Uses where the globe is,
    /// or would be.
    func glassOrigin(for size: NSSize) -> NSPoint {
        if prefs.docked {
            let (_, vf) = Self.homeScreen(prefs: prefs)
            if let b = dockItem.buttonScreenFrame {
                let svf = Self.screen(containing: CGPoint(x: b.midX, y: b.minY - 1))?.visibleFrame ?? vf
                return OverlayLayout.glassOrigin(size: size, globe: b, orbDiameter: b.height, visible: svf)
            }
            // Not in the menu bar yet: top centre, under the menu bar.
            let anchor = NSRect(x: vf.midX - GlobeDockItem.length / 2, y: vf.maxY, width: GlobeDockItem.length,
                                height: GlobeDockItem.side)
            return OverlayLayout.glassOrigin(size: size, globe: anchor, orbDiameter: anchor.height, visible: vf)
        }
        let globeFrame = floating ? panel.frame : targetFrame()
        let vf = Self.screen(containing: CGPoint(x: globeFrame.midX, y: globeFrame.midY))?.visibleFrame
            ?? Self.homeScreen(prefs: prefs).1
        return OverlayLayout.glassOrigin(size: size, globe: globeFrame, orbDiameter: Self.orbDiameter, visible: vf)
    }

    /// The same, for a caller without the globe (no text bar to avoid).
    static func glassOrigin(for size: NSSize) -> NSPoint {
        let vf = visibleFrame
        let globe = OverlayLayout.globeFrame(visible: vf, side: GlobeView.side(forOrbDiameter: orbDiameter),
                                             orbDiameter: orbDiameter)
        return OverlayLayout.glassOrigin(size: size, globe: globe, orbDiameter: orbDiameter, visible: vf)
    }
}

// MARK: - `--globe-selftest` access (never ordered on screen there)

extension GlobeHUD {
    var floatingFrame: NSRect { panel.frame }
    var isDocked: Bool { prefs.docked }
    var isTransparent: Bool { prefs.transparent }
    var panelCanTakeFocus: Bool { panel.canBecomeKey || panel.canBecomeMain || hitView.acceptsFirstResponder }

    /// Puts the (unshown) panel where it would go.
    func placeForSelfTest() { place() }

    /// What the window's own hit-test returns for a screen point: true when
    /// the orb view takes it.
    func hitsOrb(atScreen p: CGPoint) -> Bool {
        guard let frameView = hitView.superview else { return false }
        let inWindow = NSPoint(x: p.x - panel.frame.minX, y: p.y - panel.frame.minY)
        return hitView.hitTest(frameView.convert(inWindow, from: nil)) === hitView
    }

    /// Feeds one synthetic mouse event through the orb view, as AppKit would.
    func sendMouse(_ type: NSEvent.EventType, clickCount: Int = 1) {
        guard let e = NSEvent.mouseEvent(with: type, location: .zero, modifierFlags: [], timestamp: 0,
                                         windowNumber: 0, context: nil, eventNumber: 0,
                                         clickCount: clickCount, pressure: 1) else { return }
        switch type {
        case .leftMouseDown: hitView.mouseDown(with: e)
        case .leftMouseDragged: hitView.mouseDragged(with: e)
        case .leftMouseUp: hitView.mouseUp(with: e)
        default: break
        }
    }

    func setTransparentForSelfTest(_ on: Bool) { setTransparent(on) }
}
