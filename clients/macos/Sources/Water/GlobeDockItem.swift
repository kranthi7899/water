import AppKit
import WaterClientCore

/// The docked globe (2026-09-26 globe interaction): when the owner pushes
/// the orb up into the menu bar (or picks "Go to panel"), the floating
/// globe hides and this separate status item shows a small live orb that
/// follows the same state (idle, listening, thinking, responding,
/// searching) and look. It is a GlobeView (same shader, same smoothing)
/// sized to the menu bar, at 30fps; its drawable is ~44px square, so the
/// cost is tiny, and it pauses whenever the item is removed.
///
/// Clicking it undocks (`onClick`). GlobeHUD adds it only while docked AND
/// the globe would be showing (voice mode on, or an interaction/approval
/// holding it) and removes it otherwise — so it never lingers once voice
/// mode is off. The app's own drop icon and menu are a different item and
/// are untouched.
final class GlobeDockItem: NSObject {
    /// The mini orb's view side, points (the menu bar is 24pt, or taller
    /// with a notch).
    static let side: CGFloat = 22
    /// The status item's width.
    static let length: CGFloat = 28
    /// The sphere's radius in the mini view's clip space: bigger than the
    /// floating orb's (GlobeView.sphereFraction), so the orb itself is
    /// about 18pt and the glow is a thin halo.
    static let sphereRadius: Float = 0.82

    var onClick: (() -> Void)?
    /// Polled every frame, like the floating globe's.
    var inputProvider: (() -> (GlobeState, Float))?
    var look = GlobeLook.normal { didSet { globe?.look = look } }

    private var item: NSStatusItem?
    private var globe: GlobeView?

    var isShown: Bool { item != nil }

    /// Adds the item (if not already there) and starts its orb, settled
    /// straight into `state`.
    func show(state: GlobeState) {
        guard item == nil else { return }
        let it = NSStatusBar.system.statusItem(withLength: Self.length)
        guard let button = it.button else {
            NSStatusBar.system.removeStatusItem(it)
            return
        }
        button.image = nil
        button.title = ""
        button.toolTip = "Water's globe — click to bring it back"
        button.setAccessibilityLabel("Water globe")
        button.target = self
        button.action = #selector(clicked)
        let h = max(button.bounds.height, NSStatusBar.system.thickness)
        let g = GlobeView(frame: NSRect(x: (Self.length - Self.side) / 2, y: ((h - Self.side) / 2).rounded(),
                                        width: Self.side, height: Self.side))
        g.autoresizingMask = [.minXMargin, .maxXMargin, .minYMargin, .maxYMargin]
        g.sphereRadius = Self.sphereRadius
        g.preferredFPS = 30
        g.look = look
        g.inputProvider = { [weak self] in self?.inputProvider?() ?? (.idle, 0) }
        g.reset(to: state)
        g.isActive = true
        button.addSubview(g)
        item = it
        globe = g
    }

    /// Removes the item; its orb stops.
    func hide() {
        globe?.isActive = false
        globe?.removeFromSuperview()
        globe = nil
        if let it = item { NSStatusBar.system.removeStatusItem(it) }
        item = nil
    }

    /// The status button's frame on screen, for placing the glass tab.
    var buttonScreenFrame: NSRect? {
        guard let b = item?.button, let w = b.window else { return nil }
        return w.convertToScreen(b.convert(b.bounds, to: nil))
    }

    @objc private func clicked() { onClick?() }
}
