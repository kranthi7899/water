import CoreGraphics
import Foundation

/// Where the globe and the glass tab sit, and the rectangle math behind
/// dragging and docking the globe (2026-09-26 globe interaction). Pure
/// rectangle math in AppKit's bottom-left coordinates, so it is tested
/// (OverlayLayoutTests, GlobeInteractionTests); GlobeHUD and
/// GlassTabController only apply it.
///
/// - By default the globe is centred horizontally on the screen with the
///   menu bar, its orb's centre `globeCenterAboveBottom` (about three
///   inches) above the bottom of the visible frame (owner: "not completely
///   bottom, but a few inches up"). A position the owner dragged it to is
///   remembered per screen (`GlobePrefs`) and used instead.
/// - If the globe would cover the text bar (its frame passed as
///   `avoiding`), it sits just above the text bar instead, or just below it
///   when there's no room above.
/// - The glass tab is centred on the orb, `glassGap` above the orb's top
///   edge (not the globe window's, which has room for the glow); when it
///   doesn't fit above it flips below, and it is clamped to stay `margin`
///   inside the visible frame.
/// - Clamping keeps the ORB (not its glow margin) on screen.
public enum OverlayLayout {
    /// The orb's default centre, in points above the visible frame's bottom
    /// (72pt is an inch): three inches.
    public static let globeCenterAboveBottom: CGFloat = 216
    /// Space between the orb's edge and the glass tab.
    public static let glassGap: CGFloat = 12
    /// Space between the globe window and the text bar it avoids.
    public static let avoidGap: CGFloat = 6
    /// The closest the orb or the tab may come to the visible frame's edges.
    public static let margin: CGFloat = 8
    /// With the menu bar hidden, how far below the screen's top the orb's
    /// centre must be pushed to dock.
    public static let dockBand: CGFloat = 24
    /// The orb's diameter / the globe window's side at rest (the rest is
    /// room for the glow): GlobeView.sphereFraction.
    public static let orbFraction: CGFloat = 0.68
    /// Extra points around the orb's circle that still count as a hit.
    public static let hitSlop: CGFloat = 2

    // MARK: globe

    /// The default orb centre on a visible frame.
    public static func defaultOrbCenter(visible vf: CGRect) -> CGPoint {
        CGPoint(x: vf.midX, y: vf.minY + globeCenterAboveBottom)
    }

    /// The globe window's default frame: a `side`-point square centred on
    /// `defaultOrbCenter`. `orbDiameter` defaults to `side * orbFraction`.
    public static func globeFrame(visible vf: CGRect, side: CGFloat, orbDiameter: CGFloat? = nil,
                                  avoiding avoid: CGRect? = nil) -> CGRect {
        globeFrame(orbCenter: defaultOrbCenter(visible: vf), side: side,
                   orbDiameter: orbDiameter ?? (side * orbFraction).rounded(), visible: vf, avoiding: avoid)
    }

    /// The globe window's frame for an orb centred at `center` (a remembered
    /// position), moved clear of `avoid` and clamped so the orb is on screen.
    public static func globeFrame(orbCenter center: CGPoint, side: CGFloat, orbDiameter: CGFloat, visible vf: CGRect,
                                  avoiding avoid: CGRect? = nil) -> CGRect {
        var frame = CGRect(x: (center.x - side / 2).rounded(), y: (center.y - side / 2).rounded(), width: side, height: side)
        frame = settledFrame(frame, orbDiameter: orbDiameter, visible: vf)
        if let avoid, !avoid.isEmpty, frame.intersects(avoid) {
            let above = CGRect(x: frame.minX, y: avoid.maxY + avoidGap, width: side, height: side)
            if above.maxY <= vf.maxY {
                frame = above
            } else {
                frame.origin.y = avoid.minY - avoidGap - side
            }
            frame = settledFrame(frame, orbDiameter: orbDiameter, visible: vf)
        }
        return frame
    }

    /// The orb's square inside a globe window (it is centred in it).
    public static func orbRect(globe: CGRect, orbDiameter d: CGFloat) -> CGRect {
        CGRect(x: globe.midX - d / 2, y: globe.midY - d / 2, width: d, height: d)
    }

    /// True when `p` is on the orb's circle (plus `hitSlop`): only there
    /// does the globe take the mouse; the glow margin stays click-through.
    public static func orbContains(_ p: CGPoint, globe: CGRect, orbDiameter d: CGFloat) -> Bool {
        let dx = p.x - globe.midX, dy = p.y - globe.midY
        let r = d / 2 + hitSlop
        return dx * dx + dy * dy <= r * r
    }

    // MARK: drag and dock

    /// A frame proposed by a drag, clamped so the orb stays on the screen:
    /// inside it horizontally, above the visible frame's bottom (the Dock),
    /// and with its centre no higher than the screen's top — so it can be
    /// pushed up into the menu bar to dock.
    public static func dragFrame(_ proposed: CGRect, orbDiameter d: CGFloat, screen: CGRect, visible vf: CGRect) -> CGRect {
        var out = proposed
        let half = d / 2
        let cx = min(max(out.midX, screen.minX + half), screen.maxX - half)
        let cy = min(max(out.midY, vf.minY + half), screen.maxY)
        out.origin.x = cx - out.width / 2
        out.origin.y = cy - out.height / 2
        return out
    }

    /// Whether an orb centred at `c` has been pushed into the menu bar: above
    /// the visible frame's top, or — with the menu bar hidden — within the
    /// screen's top `dockBand` points.
    public static func isInDockZone(orbCenter c: CGPoint, screen: CGRect, visible vf: CGRect) -> Bool {
        c.y >= min(vf.maxY, screen.maxY - dockBand)
    }

    /// Where a released drag comes to rest: moved (never resized) so the orb
    /// lies `margin` inside the visible frame.
    public static func settledFrame(_ frame: CGRect, orbDiameter d: CGFloat, visible vf: CGRect) -> CGRect {
        let orb = orbRect(globe: frame, orbDiameter: d)
        let moved = clamp(orb, in: vf)
        return frame.offsetBy(dx: moved.minX - orb.minX, dy: moved.minY - orb.minY)
    }

    // MARK: glass tab

    /// The glass tab's origin for a tab of `size`: centred on the orb of the
    /// globe window at `globe` (or any anchor, e.g. the docked orb's status
    /// button, with `orbDiameter` its height), `glassGap` above the orb. If
    /// it doesn't fit above it goes below; if neither fits, on the roomier
    /// side, clamped.
    public static func glassOrigin(size: CGSize, globe: CGRect, orbDiameter: CGFloat, visible vf: CGRect) -> CGPoint {
        let inner = vf.insetBy(dx: margin, dy: margin)
        let orbTop = globe.midY + orbDiameter / 2
        let orbBottom = globe.midY - orbDiameter / 2
        let aboveY = orbTop + glassGap
        let belowY = orbBottom - glassGap - size.height
        let y: CGFloat
        if aboveY + size.height <= inner.maxY {
            y = aboveY
        } else if belowY >= inner.minY {
            y = belowY
        } else {
            y = (inner.maxY - orbTop) >= (orbBottom - inner.minY) ? aboveY : belowY
        }
        let frame = CGRect(x: (globe.midX - size.width / 2).rounded(), y: y.rounded(),
                           width: size.width, height: size.height)
        return clamp(frame, in: vf).origin
    }

    /// `r` moved (never resized) to lie `margin` inside `vf` where it fits;
    /// a rect taller or wider than that keeps its top/left edge on screen.
    static func clamp(_ r: CGRect, in vf: CGRect) -> CGRect {
        let inner = vf.insetBy(dx: margin, dy: margin)
        var out = r
        if out.maxX > inner.maxX { out.origin.x = inner.maxX - out.width }
        if out.minX < inner.minX { out.origin.x = inner.minX }
        if out.minY < inner.minY { out.origin.y = inner.minY }
        if out.maxY > inner.maxY { out.origin.y = inner.maxY - out.height }
        return out
    }
}
