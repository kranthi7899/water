import Carbon.HIToolbox
import Foundation
import WaterClientCore

/// `Water --voice-keys-selftest`: proves the voice-mode key path end to end
/// without taking any real key. It builds `CaptureKeys` exactly as
/// AppDelegate does (its handler feeds `VoiceMode.key`), sends synthetic
/// Carbon hot-key events for the talk key (⌃V) down/up and Esc through the
/// event dispatcher — by hot-key id, not physical key code, so this test is
/// unaffected by which real key the talk key is bound to — and checks each
/// reaches the mode with the right effects and how long the hop to the main
/// queue took. Registers nothing, touches no daemon, network or
/// UserDefaults. Exits 0 on PASS.
enum VoiceKeysSelfTest {
    static func run() -> Int32 {
        var mode = VoiceMode()
        var got: [[VoiceMode.Effect]] = []
        let keys = CaptureKeys { key, pressed in
            got.append(mode.key(key, pressed: pressed, now: Date()))
        }
        _ = mode.toggle(now: Date())

        let steps: [(CaptureKey, Bool, [VoiceMode.Effect])] = [
            (.space, true, [.startHold]),
            (.space, false, [.endHold]),
            (.escape, false, []),
            (.escape, true, [.cancelHold, .cancelTurn, .stopSpeech, .unregisterCaptureKeys, .hideGlobe, .exited(.escape)]),
        ]
        var ok = true
        for (key, pressed, want) in steps {
            let before = got.count
            let t0 = Date()
            let st = keys.simulate(key, pressed: pressed)
            while got.count == before && Date().timeIntervalSince(t0) < 2 {
                RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.001))
            }
            let ms = Date().timeIntervalSince(t0) * 1000
            let fx = got.count > before ? got[before] : nil
            let pass = st == noErr && fx == want
            ok = ok && pass
            print(String(format: "voice-keys-selftest: %@ %@ -> %@ (%.1fms) %@", "\(key)", pressed ? "down" : "up",
                         fx.map { "\($0)" } ?? "NOT DELIVERED", ms, pass ? "ok" : "FAIL (status \(st))"))
        }
        print(ok ? "voice-keys-selftest: PASS" : "voice-keys-selftest: FAIL")
        return ok ? 0 : 1
    }
}
