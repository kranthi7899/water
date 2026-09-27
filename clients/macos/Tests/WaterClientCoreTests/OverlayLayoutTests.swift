import CoreGraphics
import Foundation
import Testing
@testable import WaterClientCore

/// Globe and glass tab placement: the globe centred horizontally, its orb
/// about three inches above the bottom of the visible frame (2026-09-26
/// globe interaction); the tab above the orb, flipping below when there's
/// no room.
@Suite struct OverlayLayoutTests {
    // A 1440x900 screen whose visible frame loses 25pt to the menu bar.
    let vf = CGRect(x: 0, y: 0, width: 1440, height: 875)
    let side: CGFloat = 106
    let orb: CGFloat = 72

    @Test func globeIsCentredAFewInchesAboveTheBottom() {
        let g = OverlayLayout.globeFrame(visible: vf, side: side)
        #expect(abs(g.midX - vf.midX) <= 0.5)
        #expect(abs((g.midY - vf.minY) - OverlayLayout.globeCenterAboveBottom) <= 0.5)
        #expect(g.width == side && g.height == side)
        // About three inches (72pt per inch), and clearly not at the very bottom.
        #expect(OverlayLayout.globeCenterAboveBottom >= 180 && OverlayLayout.globeCenterAboveBottom <= 250)
    }

    @Test func globeRespectsARaisedVisibleBottom() {
        // A Dock at the bottom raises the visible frame's minY.
        let docked = CGRect(x: 0, y: 70, width: 1440, height: 805)
        let g = OverlayLayout.globeFrame(visible: docked, side: side)
        #expect(abs((g.midY - docked.minY) - OverlayLayout.globeCenterAboveBottom) <= 0.5)
    }

    @Test func globeFollowsAnOffsetScreen() {
        let second = CGRect(x: 1440, y: -200, width: 1920, height: 1055)
        let g = OverlayLayout.globeFrame(visible: second, side: side)
        #expect(abs(g.midX - second.midX) <= 0.5)
        #expect(second.contains(g))
    }

    @Test func aShortScreenKeepsTheOrbOnScreen() {
        let short = CGRect(x: 0, y: 0, width: 800, height: 200)
        let g = OverlayLayout.globeFrame(visible: short, side: side)
        let orbRect = OverlayLayout.orbRect(globe: g, orbDiameter: orb)
        #expect(short.contains(orbRect))
    }

    @Test func glassSitsCentredJustAboveTheOrb() {
        let g = OverlayLayout.globeFrame(visible: vf, side: side)
        let size = CGSize(width: 460, height: 300)
        let o = OverlayLayout.glassOrigin(size: size, globe: g, orbDiameter: orb, visible: vf)
        let tab = CGRect(origin: o, size: size)
        #expect(abs(tab.midX - g.midX) <= 0.5)
        let orbTop = g.midY + orb / 2
        #expect(abs((tab.minY - orbTop) - OverlayLayout.glassGap) <= 0.5)
    }

    @Test func glassFlipsBelowWhenThereIsNoRoomAbove() {
        // The orb dragged up near the top of the screen.
        let g = OverlayLayout.globeFrame(orbCenter: CGPoint(x: 700, y: vf.maxY - 60), side: side,
                                         orbDiameter: orb, visible: vf)
        let size = CGSize(width: 460, height: 300)
        let o = OverlayLayout.glassOrigin(size: size, globe: g, orbDiameter: orb, visible: vf)
        let tab = CGRect(origin: o, size: size)
        let orbBottom = g.midY - orb / 2
        #expect(abs((orbBottom - tab.maxY) - OverlayLayout.glassGap) <= 0.5)
        #expect(vf.contains(tab))
    }

    @Test func glassBelowTheMenuBarWhenAnchoredOnTheDockedOrb() {
        // The docked orb's status button sits in the menu bar, above vf.
        let button = CGRect(x: 1100, y: 875, width: 26, height: 25)
        let size = CGSize(width: 460, height: 300)
        let o = OverlayLayout.glassOrigin(size: size, globe: button, orbDiameter: button.height, visible: vf)
        let tab = CGRect(origin: o, size: size)
        #expect(vf.contains(tab))
        #expect(tab.maxY <= vf.maxY - OverlayLayout.margin + 0.5)
    }

    @Test func aTallTabIsClampedOnScreen() {
        let g = OverlayLayout.globeFrame(visible: vf, side: side)
        let size = CGSize(width: 460, height: 800)
        let o = OverlayLayout.glassOrigin(size: size, globe: g, orbDiameter: orb, visible: vf)
        #expect(o.y >= vf.minY + OverlayLayout.margin)
        #expect(vf.contains(CGRect(origin: o, size: size)))
    }

    @Test func aTinyScreenKeepsTheTabInsideHorizontally() {
        let small = CGRect(x: 0, y: 0, width: 400, height: 600)
        let g = OverlayLayout.globeFrame(visible: small, side: side)
        let o = OverlayLayout.glassOrigin(size: CGSize(width: 460, height: 200), globe: g, orbDiameter: orb, visible: small)
        #expect(o.x == small.minX + OverlayLayout.margin) // wider than the screen: left edge stays on
    }

    /// The text bar sits 18% down, centred: a globe placed there must not cover it.
    @Test func globeMovesAboveTheTextBar() {
        let bar = CGRect(x: vf.midX - 340, y: 200, width: 680, height: 360)
        let g = OverlayLayout.globeFrame(visible: vf, side: side, avoiding: bar)
        #expect(!g.intersects(bar))
        #expect(g.minY >= bar.maxY)
        #expect(vf.contains(g))
    }

    @Test func globeGoesBelowTheTextBarWhenThereIsNoRoomAbove() {
        let center = CGPoint(x: vf.midX, y: vf.maxY - 100)
        let bar = CGRect(x: vf.midX - 340, y: vf.maxY - 80 - 64, width: 680, height: 64)
        let g = OverlayLayout.globeFrame(orbCenter: center, side: side, orbDiameter: orb, visible: vf, avoiding: bar)
        #expect(!g.intersects(bar))
        #expect(g.maxY <= bar.minY)
    }

    @Test func aTextBarElsewhereDoesntMoveIt() {
        let plain = OverlayLayout.globeFrame(visible: vf, side: side)
        let far = CGRect(x: 0, y: 700, width: 100, height: 50)
        #expect(OverlayLayout.globeFrame(visible: vf, side: side, avoiding: far) == plain)
    }

    // MARK: remembered positions

    @Test func aRememberedCentreIsUsedAsIs() {
        let c = CGPoint(x: 300, y: 500)
        let g = OverlayLayout.globeFrame(orbCenter: c, side: side, orbDiameter: orb, visible: vf)
        #expect(abs(g.midX - c.x) <= 0.5 && abs(g.midY - c.y) <= 0.5)
    }

    @Test func aRememberedCentreOffScreenIsPulledBack() {
        let g = OverlayLayout.globeFrame(orbCenter: CGPoint(x: -500, y: 5000), side: side, orbDiameter: orb, visible: vf)
        #expect(vf.insetBy(dx: OverlayLayout.margin - 0.5, dy: OverlayLayout.margin - 0.5)
            .contains(OverlayLayout.orbRect(globe: g, orbDiameter: orb)))
    }

    // MARK: hit testing

    @Test func onlyTheOrbsCircleIsHit() {
        let g = CGRect(x: 100, y: 100, width: side, height: side)
        #expect(OverlayLayout.orbContains(CGPoint(x: g.midX, y: g.midY), globe: g, orbDiameter: orb))
        #expect(OverlayLayout.orbContains(CGPoint(x: g.midX + orb / 2 - 1, y: g.midY), globe: g, orbDiameter: orb))
        // The glow margin and the window's corners stay click-through.
        #expect(!OverlayLayout.orbContains(CGPoint(x: g.midX + orb / 2 + 6, y: g.midY), globe: g, orbDiameter: orb))
        #expect(!OverlayLayout.orbContains(CGPoint(x: g.minX + 2, y: g.minY + 2), globe: g, orbDiameter: orb))
        // A corner of the orb's bounding square is outside the circle.
        let r = orb / 2
        #expect(!OverlayLayout.orbContains(CGPoint(x: g.midX + r * 0.9, y: g.midY + r * 0.9), globe: g, orbDiameter: orb))
    }

    // MARK: dragging and docking

    let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)

    @Test func aDragIsClampedToTheScreen() {
        let far = CGRect(x: 5000, y: -3000, width: side, height: side)
        let f = OverlayLayout.dragFrame(far, orbDiameter: orb, screen: screen, visible: vf)
        let o = OverlayLayout.orbRect(globe: f, orbDiameter: orb)
        #expect(o.maxX <= screen.maxX + 0.5)
        #expect(o.minY >= vf.minY - 0.5)
        #expect(f.width == side)
    }

    @Test func aDragMayReachTheMenuBar() {
        let up = CGRect(x: 600, y: 2000, width: side, height: side)
        let f = OverlayLayout.dragFrame(up, orbDiameter: orb, screen: screen, visible: vf)
        #expect(f.midY >= vf.maxY) // the orb's centre can enter the menu bar
        #expect(f.midY <= screen.maxY + 0.5)
        #expect(OverlayLayout.isInDockZone(orbCenter: CGPoint(x: f.midX, y: f.midY), screen: screen, visible: vf))
    }

    @Test func dockZoneIsTheMenuBarOrTheTopBand() {
        #expect(!OverlayLayout.isInDockZone(orbCenter: CGPoint(x: 700, y: 500), screen: screen, visible: vf))
        #expect(!OverlayLayout.isInDockZone(orbCenter: CGPoint(x: 700, y: vf.maxY - 20), screen: screen, visible: vf))
        #expect(OverlayLayout.isInDockZone(orbCenter: CGPoint(x: 700, y: vf.maxY + 1), screen: screen, visible: vf))
        // An auto-hidden menu bar: the visible frame reaches the top, so the
        // top `dockBand` points of the screen dock.
        let full = screen
        #expect(OverlayLayout.isInDockZone(orbCenter: CGPoint(x: 700, y: screen.maxY - 10), screen: screen, visible: full))
        #expect(!OverlayLayout.isInDockZone(orbCenter: CGPoint(x: 700, y: screen.maxY - 40), screen: screen, visible: full))
    }

    @Test func aReleasedDragSettlesInsideTheVisibleFrame() {
        let high = CGRect(x: 1400, y: 860, width: side, height: side)
        let f = OverlayLayout.settledFrame(high, orbDiameter: orb, visible: vf)
        let o = OverlayLayout.orbRect(globe: f, orbDiameter: orb)
        #expect(vf.insetBy(dx: OverlayLayout.margin - 0.5, dy: OverlayLayout.margin - 0.5).contains(o))
        // Already inside: unchanged.
        let fine = CGRect(x: 400, y: 300, width: side, height: side)
        #expect(OverlayLayout.settledFrame(fine, orbDiameter: orb, visible: vf) == fine)
    }
}
