import AppKit
import WaterClientCore

/// `Water --globe-selftest`: proves the globe's AppKit wiring end to end
/// without showing anything. It builds a real GlobeHUD (panel, orb view,
/// hit-testing) that is never ordered on screen, with its preferences in
/// memory (MemoryPrefsStore; no preferences file) and a fake pointer, and
/// sends synthetic mouse events through the orb's view exactly as AppKit
/// would: the default spot, the panel never taking focus, only the orb's
/// circle hit, a click that doesn't move it, a drag that moves it and is
/// remembered (and restored by a fresh HUD), a push into the menu bar that
/// docks it, undocking, and transparent mode being saved. It adds no status
/// item, registers no key, and touches no daemon, network or the real
/// defaults. The double-click menu is not opened (it would block);
/// GlobeInteractionTests covers that gesture. Exits 0 on PASS.
enum GlobeSelfTest {
    static func run() -> Int32 {
        _ = NSApplication.shared
        guard let screen = NSScreen.screens.first, let screenID = GlobeHUD.screenID(screen) else {
            print("globe-selftest: no screen; skipped")
            return 0
        }
        let defaults = MemoryPrefsStore()

        var ok = true
        func check(_ name: String, _ pass: Bool) {
            ok = ok && pass
            print("globe-selftest: \(name) \(pass ? "ok" : "FAIL")")
        }
        func near(_ a: CGPoint?, _ b: CGPoint, _ tol: CGFloat = 1) -> Bool {
            guard let a else { return false }
            return abs(a.x - b.x) <= tol && abs(a.y - b.y) <= tol
        }
        func makeHUD() -> GlobeHUD {
            GlobeHUD(client: UnixSocketClient(socketPath: "/nonexistent/water-selftest.sock"),
                     tokens: TokenProvider(store: MemoryTokenStore()), defaults: defaults)
        }

        let hud = makeHUD()
        var ptr = CGPoint.zero
        hud.pointer = { ptr }
        hud.placeForSelfTest()
        let vf = screen.visibleFrame
        let f0 = hud.floatingFrame
        let c0 = CGPoint(x: f0.midX, y: f0.midY)
        let r = GlobeHUD.orbDiameter / 2
        check("default spot: centred", abs(c0.x - vf.midX) <= 1)
        check("default spot: orb \(Int(OverlayLayout.globeCenterAboveBottom))pt above the bottom",
              abs(c0.y - vf.minY - OverlayLayout.globeCenterAboveBottom) <= 1)
        check("never takes key/main/first responder", !hud.panelCanTakeFocus)
        check("orb centre takes the mouse", hud.hitsOrb(atScreen: c0))
        check("orb edge takes the mouse", hud.hitsOrb(atScreen: CGPoint(x: c0.x + r - 2, y: c0.y)))
        check("glow is click-through", !hud.hitsOrb(atScreen: CGPoint(x: c0.x + r + 8, y: c0.y)))
        check("window corner is click-through", !hud.hitsOrb(atScreen: CGPoint(x: f0.minX + 2, y: f0.minY + 2)))

        ptr = c0
        hud.sendMouse(.leftMouseDown)
        hud.sendMouse(.leftMouseUp)
        check("a click doesn't move it", hud.floatingFrame == f0)
        check("a click saves nothing", defaults.object(forKey: GlobePrefs.originKey) == nil)

        hud.sendMouse(.leftMouseDown)
        ptr = CGPoint(x: c0.x - 150, y: c0.y + 120)
        hud.sendMouse(.leftMouseDragged)
        let f1 = hud.floatingFrame
        check("drag moves it with the pointer", near(CGPoint(x: f1.midX, y: f1.midY), ptr))
        hud.sendMouse(.leftMouseUp)
        let saved = GlobePrefs(defaults: defaults)
        check("release remembers the spot for this screen", near(saved.orbCenter(screen: screenID, visible: vf), ptr))

        let again = makeHUD()
        again.placeForSelfTest()
        let f2 = again.floatingFrame
        check("a fresh globe comes back there", near(CGPoint(x: f2.midX, y: f2.midY), ptr))

        ptr = CGPoint(x: f1.midX, y: f1.midY)
        hud.sendMouse(.leftMouseDown)
        ptr.y = screen.frame.maxY + 40
        hud.sendMouse(.leftMouseDragged)
        check("pushing it into the menu bar docks it", hud.isDocked && GlobePrefs(defaults: defaults).docked)
        hud.sendMouse(.leftMouseUp)
        check("docking kept the last resting spot",
              near(GlobePrefs(defaults: defaults).orbCenter(screen: screenID, visible: vf), CGPoint(x: f1.midX, y: f1.midY)))
        hud.undock()
        check("undock", !hud.isDocked && !GlobePrefs(defaults: defaults).docked)

        hud.setTransparentForSelfTest(true)
        check("transparent mode is saved", GlobePrefs(defaults: defaults).transparent)

        print(ok ? "globe-selftest: PASS" : "globe-selftest: FAIL")
        return ok ? 0 : 1
    }
}
