import CoreGraphics
import Foundation
import Testing
@testable import WaterClientCore

/// The globe interaction (2026-09-26): drag like a file, dock into the menu
/// bar, the double-click menu, the remembered position/dock/transparent
/// preferences, the hover and transparent looks, and the searching state.
@Suite struct GlobeInteractionTests {
    let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)
    let vf = CGRect(x: 0, y: 0, width: 1440, height: 875)
    let side: CGFloat = 106
    let orb: CGFloat = 72
    var start: CGRect { CGRect(x: 667, y: 163, width: side, height: side) }

    func drag() -> GlobeDrag { GlobeDrag(orbDiameter: orb, screen: screen, visible: vf) }

    // MARK: drag gesture

    @Test func aSingleClickWithoutMovementDoesNothing() {
        var d = drag()
        #expect(d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1) == .none)
        #expect(d.mouseUp(at: CGPoint(x: 720, y: 216)) == .none)
    }

    @Test func aTinyJitterIsStillAClick() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        #expect(d.mouseDragged(to: CGPoint(x: 721, y: 217)) == .none)
        #expect(d.mouseUp(at: CGPoint(x: 721, y: 217)) == .none)
    }

    @Test func dragMovesTheWindowByThePointerDelta() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        let out = d.mouseDragged(to: CGPoint(x: 520, y: 416))
        guard case .move(let f) = out else { Issue.record("expected move, got \(out)"); return }
        #expect(f.origin == CGPoint(x: start.minX - 200, y: start.minY + 200))
        #expect(f.size == start.size)
        // Release: settles (already on screen, so unchanged) and is saved.
        #expect(d.mouseUp(at: CGPoint(x: 520, y: 416)) == .settle(f))
    }

    @Test func dragIsClampedToTheScreen() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        guard case .move(let f) = d.mouseDragged(to: CGPoint(x: 9000, y: 216)) else { Issue.record("no move"); return }
        #expect(OverlayLayout.orbRect(globe: f, orbDiameter: orb).maxX <= screen.maxX + 0.5)
    }

    @Test func pushingItIntoTheMenuBarDocksAtOnce() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        _ = d.mouseDragged(to: CGPoint(x: 720, y: 600))
        #expect(d.mouseDragged(to: CGPoint(x: 720, y: 900)) == .dock)
        // The gesture is over: nothing more comes of it.
        #expect(d.mouseDragged(to: CGPoint(x: 720, y: 300)) == .none)
        #expect(d.mouseUp(at: CGPoint(x: 720, y: 300)) == .none)
    }

    @Test func doubleClickOpensTheMenuAndNeverDrags() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        _ = d.mouseUp(at: CGPoint(x: 720, y: 216))
        #expect(d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 2) == .menu)
        #expect(d.mouseDragged(to: CGPoint(x: 300, y: 300)) == .none)
        #expect(d.mouseUp(at: CGPoint(x: 300, y: 300)) == .none)
    }

    @Test func dragOntoASecondScreenClampsToIt() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        let second = CGRect(x: 1440, y: 0, width: 1920, height: 1080)
        d = d.retargeted(screen: second, visible: CGRect(x: 1440, y: 0, width: 1920, height: 1055))
        guard case .move(let f) = d.mouseDragged(to: CGPoint(x: 2400, y: 216)) else { Issue.record("no move"); return }
        #expect(abs(f.midX - (start.midX + 1680)) <= 0.5) // not clamped back to the first screen
        #expect(second.contains(OverlayLayout.orbRect(globe: f, orbDiameter: orb)))
    }

    @Test func eventsWithoutAMouseDownAreIgnored() {
        var d = drag()
        #expect(d.mouseDragged(to: CGPoint(x: 1, y: 1)) == .none)
        #expect(d.mouseUp(at: CGPoint(x: 1, y: 1)) == .none)
    }

    @Test func releasingHighSettlesBackOnScreen() {
        var d = drag()
        _ = d.mouseDown(at: CGPoint(x: 720, y: 216), frame: start, clickCount: 1)
        // Up to just under the dock zone.
        guard case .move = d.mouseDragged(to: CGPoint(x: 720, y: 216 + (vf.maxY - 216) - 5)) else {
            Issue.record("no move"); return
        }
        guard case .settle(let f) = d.mouseUp(at: CGPoint(x: 720, y: vf.maxY - 5)) else { Issue.record("no settle"); return }
        #expect(vf.contains(OverlayLayout.orbRect(globe: f, orbDiameter: orb)))
    }

    // MARK: preferences

    func freshDefaults() -> MemoryPrefsStore { MemoryPrefsStore() }

    @Test func prefsDefaultToNothingRemembered() {
        let p = GlobePrefs(defaults: freshDefaults())
        #expect(!p.docked && !p.transparent)
        #expect(p.orbCenter(screen: "1", visible: vf) == nil)
    }

    @Test func prefsRoundTripPerScreen() {
        let d = freshDefaults()
        var p = GlobePrefs(defaults: d)
        p.remember(orbCenter: CGPoint(x: 300, y: 400), visible: vf, screen: "1")
        let second = CGRect(x: 1440, y: -200, width: 1920, height: 1055)
        p.remember(orbCenter: CGPoint(x: 2000, y: 100), visible: second, screen: "2")
        p.docked = true
        p.transparent = true
        p.save(to: d)

        let q = GlobePrefs(defaults: d)
        #expect(q == p)
        #expect(q.docked && q.transparent)
        #expect(q.lastScreen == "2")
        #expect(q.orbCenter(screen: "1", visible: vf) == CGPoint(x: 300, y: 400))
        #expect(q.orbCenter(screen: "2", visible: second) == CGPoint(x: 2000, y: 100))
        #expect(q.orbCenter(screen: "3", visible: vf) == nil)
        // Stored relative to the visible frame: a moved screen keeps the spot.
        let moved = second.offsetBy(dx: 100, dy: 50)
        #expect(q.orbCenter(screen: "2", visible: moved) == CGPoint(x: 2100, y: 150))
        #expect(d.object(forKey: GlobePrefs.originKey) != nil)
    }

    @Test func garbageInDefaultsIsIgnored() {
        let d = freshDefaults()
        d.set(["1": ["x"], "2": [Double.nan, 3], "3": [1, 2, 3], "4": "no"] as [String: Any], forKey: GlobePrefs.originKey)
        d.set(42, forKey: GlobePrefs.screenKey)
        let p = GlobePrefs(defaults: d)
        #expect(p.centers.isEmpty)
        #expect(p.lastScreen == nil)
        d.set("junk", forKey: GlobePrefs.originKey)
        #expect(GlobePrefs(defaults: d).centers.isEmpty)
    }

    // MARK: looks

    @Test func hoverBrightensEveryState() {
        for s in GlobeState.allCases {
            let plain = GlobeModel.target(state: s, level: 0.3, time: 1)
            let lit = GlobeModel.target(state: s, level: 0.3, time: 1, look: GlobeLook(hover: true))
            #expect(lit.hover == 1 && plain.hover == 0)
            #expect(lit.energy > plain.energy)
            #expect(lit.glow >= plain.glow)
            #expect(lit.energy <= 1 && lit.glow <= 1)
        }
    }

    @Test func transparentModeIsGreyAndFainterButStillReacts() {
        let normal = GlobeModel.target(state: .listening, level: 0.8, time: 1)
        let mono = GlobeModel.target(state: .listening, level: 0.8, time: 1, look: GlobeLook(transparent: true))
        #expect(normal.saturation == 1 && normal.opacity == 1)
        #expect(mono.saturation == 0)
        #expect(mono.opacity < 1 && mono.opacity >= 0.5)
        // State and level still drive it.
        #expect(mono.energy == normal.energy && mono.speed == normal.speed)
        let quiet = GlobeModel.target(state: .listening, level: 0, time: 1, look: GlobeLook(transparent: true))
        #expect(mono.energy > quiet.energy)
        let hovered = GlobeModel.target(state: .listening, level: 0.8, time: 1, look: GlobeLook(hover: true, transparent: true))
        #expect(hovered.energy > mono.energy)
    }

    @Test func smootherEasesIntoHoverAndTransparent() {
        var s = GlobeSmoother()
        let look = GlobeLook(hover: true, transparent: true)
        let first = s.step(state: .idle, level: 0, time: 1.0 / 60, dt: 1.0 / 60, look: look)
        #expect(first.hover > 0 && first.hover < 1) // eased, never a jump
        #expect(first.saturation < 1 && first.saturation > 0)
        for i in 2...240 { s.step(state: .idle, level: 0, time: Double(i) / 60, dt: 1.0 / 60, look: look) }
        #expect(abs(s.current.hover - 1) < 0.01)
        #expect(abs(s.current.saturation) < 0.01)
    }

    // MARK: searching

    @Test func searchingHasItsOwnLook() {
        #expect(GlobeState(name: "searching") == .searching)
        #expect(GlobeState(phase: .searching) == .searching)
        let search = GlobeModel.target(state: .searching, level: 0, time: 1)
        let think = GlobeModel.target(state: .thinking, level: 0, time: 1)
        #expect(search.search == 1)
        #expect(think.search == 0)
        #expect(search.hueShift > think.hueShift) // cooler, icier
        for s in GlobeState.allCases where s != .searching {
            #expect(GlobeModel.target(state: s, level: 0.5, time: 1).search == 0)
        }
        // Time-driven: level plays no part.
        #expect(GlobeModel.target(state: .searching, level: 1, time: 1) == search)
    }

    func ev(_ kind: TurnEvent.Kind, stepID: String? = nil, tool: String? = nil, status: String? = nil) -> TurnEvent {
        TurnEvent(kind: kind, stepID: stepID, tool: tool, status: status)
    }

    let t0 = Date(timeIntervalSince1970: 1_000_000)

    @Test func aWebSearchToolStartEntersSearchingUntilToolEnd() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        #expect(m.phase == .thinking)
        m.event(ev(.toolStart, stepID: "s1", tool: "research.web"), turn: turn, now: t0)
        #expect(m.phase == .searching)
        #expect(m.isVisible)
        m.event(ev(.toolEnd, stepID: "s1", tool: "research.web", status: "ok"), turn: turn, now: t0)
        #expect(m.phase == .thinking)
        m.event(ev(.sentence), turn: turn, now: t0)
        #expect(m.phase == .responding)
    }

    // docs/slices/UI.md Phase 6, U1-A: the globe's "announcing tools" look
    // for a relay-producing call reuses the identical `.searching` state a
    // web search already gets, driven by the same `searchTools` set --
    // proven here against linear.create_comment specifically (the call
    // whose successful result, flagged relay: true, streams as a `note`
    // artifact once it completes).
    @Test func aRelayCommentToolStartEntersSearchingUntilToolEnd() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        #expect(m.phase == .thinking)
        m.event(ev(.toolStart, stepID: "s1", tool: "linear.create_comment"), turn: turn, now: t0)
        #expect(m.phase == .searching)
        #expect(m.isVisible)
        m.event(ev(.toolEnd, stepID: "s1", tool: "linear.create_comment", status: "ok"), turn: turn, now: t0)
        #expect(m.phase == .thinking)
    }

    @Test func aSentenceEndsSearching() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        m.event(ev(.toolStart, stepID: "s1", tool: " research.web "), turn: turn, now: t0)
        #expect(m.phase == .searching)
        m.event(ev(.sentence), turn: turn, now: t0)
        #expect(m.phase == .responding)
    }

    @Test func otherToolsDontSearch() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        m.event(ev(.toolStart, stepID: "s1", tool: "gcal.list_events"), turn: turn, now: t0)
        #expect(m.phase == .thinking)
        m.event(ev(.toolStart, stepID: "s2"), turn: turn, now: t0)
        #expect(m.phase == .thinking)
    }

    @Test func searchingWhileSpeakingReturnsToResponding() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        m.event(ev(.sentence), turn: turn, now: t0)
        m.speechStarted(now: t0)
        m.event(ev(.toolStart, stepID: "s1", tool: "research.web"), turn: turn, now: t0)
        #expect(m.phase == .searching)
        m.event(ev(.toolEnd, stepID: "s1", status: "ok"), turn: turn, now: t0)
        #expect(m.phase == .responding)
    }

    @Test func searchingEndsWithTheTurnAndOnEsc() {
        var m = ActivityModel()
        var turn = m.turnSent(now: t0)
        m.event(ev(.toolStart, stepID: "s1", tool: "research.web"), turn: turn, now: t0)
        m.event(ev(.done), turn: turn, now: t0)
        #expect(m.phase == .idle)

        turn = m.turnSent(now: t0)
        m.event(ev(.toolStart, stepID: "s1", tool: "research.web"), turn: turn, now: t0)
        m.dismissNow(now: t0)
        #expect(m.phase == .idle && !m.isVisible)
        // A stale tool_start from the cancelled turn changes nothing.
        m.event(ev(.toolStart, stepID: "s2", tool: "research.web"), turn: turn, now: t0)
        #expect(m.phase == .idle)
    }

    @Test func aStaleSearchAfterTheTurnEndedIsIgnored() {
        var m = ActivityModel()
        let turn = m.turnSent(now: t0)
        m.event(ev(.done), turn: turn, now: t0)
        m.event(ev(.toolStart, stepID: "s9", tool: "research.web"), turn: turn, now: t0)
        #expect(m.phase == .idle)
    }
}
