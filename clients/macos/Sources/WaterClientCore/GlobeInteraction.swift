import CoreGraphics
import Foundation

// The globe's mouse interaction (owner, 2026-09-26): hover brightens it,
// click-and-drag moves it like a file, pushing it up into the menu bar docks
// it there, and a double-click opens a small menu (Transparent mode, Go to
// panel, Quit voice mode). A single click without movement does nothing.
// Pure: GlobeHUD feeds it screen points (AppKit's bottom-left coordinates)
// and applies the outcomes (GlobeInteractionTests).

/// One press-drag-release on the orb.
public struct GlobeDrag: Equatable {
    /// Points the pointer must travel before a press becomes a drag, so a
    /// click with a shaky hand stays a click.
    public static let threshold: CGFloat = 3

    public enum Outcome: Equatable {
        case none
        /// Move the globe window to this frame (already clamped).
        case move(CGRect)
        /// The orb was pushed into the menu bar: dock it. The gesture ends.
        case dock
        /// The drag was released: come to rest here, and remember it.
        case settle(CGRect)
        /// A double-click: show the globe's menu.
        case menu
    }

    public let orbDiameter: CGFloat
    public private(set) var screen: CGRect
    public private(set) var visible: CGRect

    private var downAt: CGPoint?
    private var startFrame: CGRect = .zero
    private var current: CGRect = .zero
    private var dragging = false

    public init(orbDiameter: CGFloat, screen: CGRect, visible: CGRect) {
        self.orbDiameter = orbDiameter
        self.screen = screen
        self.visible = visible
    }

    public var isDragging: Bool { dragging }

    /// The same gesture, clamped to another screen from now on (the
    /// pointer crossed onto it).
    public func retargeted(screen: CGRect, visible: CGRect) -> GlobeDrag {
        var d = self
        d.screen = screen
        d.visible = visible
        return d
    }

    /// The button went down on the orb, at screen point `p`, with the
    /// globe window at `frame`. A second click of a double-click opens the
    /// menu and never starts a drag.
    public mutating func mouseDown(at p: CGPoint, frame: CGRect, clickCount: Int) -> Outcome {
        dragging = false
        if clickCount >= 2 {
            downAt = nil
            return .menu
        }
        downAt = p
        startFrame = frame
        current = frame
        return .none
    }

    public mutating func mouseDragged(to p: CGPoint) -> Outcome {
        guard let d = downAt else { return .none }
        let dx = p.x - d.x, dy = p.y - d.y
        if !dragging {
            guard dx * dx + dy * dy >= Self.threshold * Self.threshold else { return .none }
            dragging = true
        }
        let proposed = startFrame.offsetBy(dx: dx, dy: dy)
        current = OverlayLayout.dragFrame(proposed, orbDiameter: orbDiameter, screen: screen, visible: visible)
        if OverlayLayout.isInDockZone(orbCenter: CGPoint(x: current.midX, y: current.midY), screen: screen, visible: visible) {
            downAt = nil
            dragging = false
            return .dock
        }
        return .move(current)
    }

    public mutating func mouseUp(at p: CGPoint) -> Outcome {
        guard downAt != nil else { return .none }
        downAt = nil
        guard dragging else { return .none } // a plain click: nothing
        dragging = false
        return .settle(OverlayLayout.settledFrame(current, orbDiameter: orbDiameter, visible: visible))
    }
}

/// Where GlobePrefs lives: UserDefaults in the app, `MemoryPrefsStore` in
/// tests and `--globe-selftest` (so they never write a preferences file).
public protocol GlobePrefsStore: AnyObject {
    func object(forKey key: String) -> Any?
    func set(_ value: Any?, forKey key: String)
}

extension UserDefaults: GlobePrefsStore {}

/// An in-memory GlobePrefsStore.
public final class MemoryPrefsStore: GlobePrefsStore {
    public private(set) var values: [String: Any] = [:]
    public init() {}
    public func object(forKey key: String) -> Any? { values[key] }
    public func set(_ value: Any?, forKey key: String) { values[key] = value }
}

/// What the globe remembers across launches, in UserDefaults:
/// - `globeOrigin`: per screen, the orb's centre as an offset from that
///   screen's visible-frame origin (so a moved Dock or menu bar keeps it
///   roughly in place), `[screenID: [dx, dy]]`;
/// - `globeScreen`: the screen it was last left on;
/// - `globeDocked`: docked in the menu bar;
/// - `globeTransparent`: transparent (black-and-white) mode.
/// Anything malformed in the store is ignored, never trusted.
public struct GlobePrefs: Equatable {
    public static let originKey = "globeOrigin"
    public static let screenKey = "globeScreen"
    public static let dockedKey = "globeDocked"
    public static let transparentKey = "globeTransparent"

    /// Orb centre offsets from each screen's visible-frame origin.
    public private(set) var centers: [String: CGPoint] = [:]
    public private(set) var lastScreen: String?
    public var docked = false
    public var transparent = false

    public init() {}

    public init(defaults d: GlobePrefsStore) {
        if let raw = d.object(forKey: Self.originKey) as? [String: Any] {
            for (k, v) in raw {
                guard let a = v as? [Any], a.count == 2,
                      let x = (a[0] as? NSNumber)?.doubleValue, let y = (a[1] as? NSNumber)?.doubleValue,
                      x.isFinite, y.isFinite else { continue }
                centers[k] = CGPoint(x: x, y: y)
            }
        }
        lastScreen = d.object(forKey: Self.screenKey) as? String
        docked = (d.object(forKey: Self.dockedKey) as? Bool) ?? false
        transparent = (d.object(forKey: Self.transparentKey) as? Bool) ?? false
    }

    public func save(to d: GlobePrefsStore) {
        d.set(centers.mapValues { [Double($0.x), Double($0.y)] }, forKey: Self.originKey)
        d.set(lastScreen, forKey: Self.screenKey)
        d.set(docked, forKey: Self.dockedKey)
        d.set(transparent, forKey: Self.transparentKey)
    }

    /// Records where the orb was left on `screen`, and that screen as the last.
    public mutating func remember(orbCenter c: CGPoint, visible vf: CGRect, screen: String) {
        guard c.x.isFinite, c.y.isFinite else { return }
        centers[screen] = CGPoint(x: c.x - vf.minX, y: c.y - vf.minY)
        lastScreen = screen
    }

    /// The orb's remembered centre on `screen` (absolute), or nil.
    public func orbCenter(screen: String, visible vf: CGRect) -> CGPoint? {
        guard let o = centers[screen] else { return nil }
        return CGPoint(x: vf.minX + o.x, y: vf.minY + o.y)
    }
}
