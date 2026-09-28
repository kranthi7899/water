import AppKit
import MetalKit
import QuartzCore
import WaterClientCore

/// The globe: a small liquid-glass orb (owner brief 2026-09-26) drawn with
/// Metal on a clear background, or — when Metal can't be used — a
/// CAGradientLayer orb in the same palette. It knows nothing about the
/// daemon or windows: the host feeds it a state and a level, either by
/// `setInput(state:level:)` or by an `inputProvider` polled every frame
/// (e.g. `{ (GlobeState(phase: model.phase), model.displayLevel(now: Date())) }`),
/// and it smooths everything itself through `GlobeSmoother`.
///
/// Sizing: the sphere's diameter is about `sphereFraction` of the view's
/// side; the rest is room for the soft outer glow. For an orb of D points,
/// make the view `GlobeView.side(forOrbDiameter: D)` square.
///
/// It renders at display rate (60fps) only while it is in a visible,
/// unoccluded window, not hidden, and `isActive`; otherwise it is paused
/// and costs nothing.
final class GlobeView: NSView {
    /// Sphere diameter / view side (at rest; energy swells it ~6%).
    static let sphereFraction: CGFloat = 0.68

    static func side(forOrbDiameter d: CGFloat) -> CGFloat {
        (d / sphereFraction).rounded(.up)
    }

    /// Polled once per frame when set; wins over `setInput`.
    var inputProvider: (() -> (GlobeState, Float))?
    /// The host's look (hover, transparent mode), eased in like the rest.
    var look = GlobeLook.normal
    /// The sphere's radius in clip units (0.68 = `sphereFraction`); the
    /// docked mini orb uses a bigger one so the orb fills the menu bar.
    var sphereRadius: Float = GlobeRenderer.defaultSphereRadius {
        didSet {
            metalView?.sphereRadius = sphereRadius
            fallback?.sphereFraction = CGFloat(sphereRadius)
        }
    }
    /// Frames per second while running (the docked mini orb runs slower).
    var preferredFPS = 60 { didSet { metalView?.preferredFramesPerSecond = preferredFPS } }

    /// The host's switch (e.g. false while the orb's window is ordered
    /// out). Rendering also pauses by itself when hidden or occluded.
    var isActive = true { didSet { updateRunning() } }

    /// True when drawing with Metal, false for the gradient fallback.
    let usesMetal: Bool

    private var state: GlobeState = .idle
    private var level: Float = 0
    private var smoother = GlobeSmoother()
    private var lastTick: CFTimeInterval?
    private let startTime = CACurrentMediaTime()

    private let metalView: GlobeMetalView?
    private let fallback: GlobeFallbackView?
    private var occlusionObserver: NSObjectProtocol?

    override init(frame: NSRect) {
        if let renderer = GlobeRenderer.makeDefault() {
            metalView = GlobeMetalView(frame: NSRect(origin: .zero, size: frame.size), renderer: renderer)
            fallback = nil
            usesMetal = true
        } else {
            metalView = nil
            fallback = GlobeFallbackView(frame: NSRect(origin: .zero, size: frame.size))
            usesMetal = false
        }
        super.init(frame: frame)
        wantsLayer = true
        layer?.backgroundColor = .clear
        let surface: NSView = metalView ?? fallback!
        surface.autoresizingMask = [.width, .height]
        addSubview(surface)
        metalView?.frameSource = { [weak self] in self?.nextFrame() }
        fallback?.frameSource = { [weak self] in self?.nextFrame() }
        updateRunning()
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    deinit {
        if let o = occlusionObserver { NotificationCenter.default.removeObserver(o) }
    }

    /// Clicks go through the orb to whatever is behind it: a host that
    /// wants the orb's mouse (GlobeHUD's floating orb) wraps it in a view
    /// that hit-tests the circle itself.
    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    func setInput(state: GlobeState, level: Float) {
        self.state = state
        self.level = level
    }

    /// Settles the orb straight to `state` (no easing), e.g. when it first
    /// appears, so it doesn't fade in from a stale state.
    func reset(to state: GlobeState = .idle) {
        let t = CACurrentMediaTime() - startTime
        smoother = GlobeSmoother(start: GlobeModel.target(state: state, level: 0, time: t, look: look))
        self.state = state
        level = 0
        lastTick = nil
    }

    // MARK: frame clock

    private func nextFrame() -> GlobeFrame {
        let now = CACurrentMediaTime()
        let dt = lastTick.map { now - $0 } ?? (1.0 / 60)
        lastTick = now
        if let p = inputProvider {
            let (s, l) = p()
            state = s
            level = l
        }
        smoother.step(state: state, level: level, time: now - startTime, dt: dt, look: look)
        return smoother.frame
    }

    // MARK: pausing

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if let o = occlusionObserver { NotificationCenter.default.removeObserver(o) }
        occlusionObserver = nil
        if let w = window {
            occlusionObserver = NotificationCenter.default.addObserver(
                forName: NSWindow.didChangeOcclusionStateNotification, object: w, queue: .main
            ) { [weak self] _ in self?.updateRunning() }
        }
        updateRunning()
    }

    override func viewDidHide() { super.viewDidHide(); updateRunning() }
    override func viewDidUnhide() { super.viewDidUnhide(); updateRunning() }

    private func updateRunning() {
        let visible = window.map { $0.occlusionState.contains(.visible) } ?? false
        let run = isActive && !isHiddenOrHasHiddenAncestor && visible
        if run, lastTick != nil, metalView?.isPaused ?? fallback?.isPaused ?? true {
            lastTick = nil // don't integrate the paused gap
        }
        metalView?.isPaused = !run
        fallback?.isPaused = !run
    }
}

/// The Metal surface: an MTKView on a clear, non-opaque CAMetalLayer,
/// drawing one GlobeRenderer frame per display refresh. Its drawable is
/// only as big as the view's backing (a ~100pt view is ~200px square), so
/// the GPU cost is tiny.
final class GlobeMetalView: MTKView, MTKViewDelegate {
    var frameSource: (() -> GlobeFrame?)?
    var sphereRadius: Float = GlobeRenderer.defaultSphereRadius
    private let renderer: GlobeRenderer

    init(frame: NSRect, renderer: GlobeRenderer) {
        self.renderer = renderer
        super.init(frame: frame, device: renderer.device)
        colorPixelFormat = GlobeRenderer.pixelFormat
        clearColor = MTLClearColor(red: 0, green: 0, blue: 0, alpha: 0)
        framebufferOnly = true
        preferredFramesPerSecond = 60
        enableSetNeedsDisplay = false
        isPaused = true
        layer?.isOpaque = false
        (layer as? CAMetalLayer)?.isOpaque = false
        delegate = self
    }

    required init(coder: NSCoder) { fatalError("not used") }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    func mtkView(_ view: MTKView, drawableSizeWillChange size: CGSize) {}

    func draw(in view: MTKView) {
        guard let frame = frameSource?(),
              let pass = currentRenderPassDescriptor,
              let drawable = currentDrawable,
              let cb = renderer.queue.makeCommandBuffer() else { return }
        pass.colorAttachments[0].loadAction = .clear
        pass.colorAttachments[0].clearColor = MTLClearColor(red: 0, green: 0, blue: 0, alpha: 0)
        renderer.encode(frame, pass: pass, width: Int(drawableSize.width), height: Int(drawableSize.height),
                        sphereRadius: sphereRadius, into: cb)
        cb.present(drawable)
        cb.commit()
    }
}
