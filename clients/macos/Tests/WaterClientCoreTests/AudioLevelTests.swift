import Foundation
import Testing
@testable import WaterClientCore

@Suite struct AudioLevelTests {
    @Test func rmsOfKnownSignals() {
        #expect(AudioLevel.rms([]) == 0)
        #expect(AudioLevel.rms([0, 0, 0]) == 0)
        #expect(abs(AudioLevel.rms([1, -1, 1, -1]) - 1) < 1e-6)
        #expect(abs(AudioLevel.rms([0.5, -0.5]) - 0.5) < 1e-6)
        // A full-scale sine has RMS 1/sqrt(2).
        let sine = (0..<1000).map { Float(sin(2 * Double.pi * Double($0) / 100)) }
        #expect(abs(AudioLevel.rms(sine) - Float(1 / 2.0.squareRoot())) < 1e-3)
    }

    @Test func rmsIgnoresNonFiniteSamples() {
        #expect(AudioLevel.rms([.nan, .infinity, 0]) == 0)
    }

    @Test func rmsFromPointer() {
        let s: [Float] = [0.25, -0.25, 0.25, -0.25]
        s.withUnsafeBufferPointer { b in
            #expect(abs(AudioLevel.rms(b.baseAddress!, count: b.count) - 0.25) < 1e-6)
            #expect(AudioLevel.rms(b.baseAddress!, count: 0) == 0)
        }
    }

    @Test func rmsToLevelUsesADecibelScale() {
        #expect(AudioLevel.level(rms: 0) == 0)
        #expect(AudioLevel.level(rms: -1) == 0)
        #expect(AudioLevel.level(rms: .nan) == 0)
        #expect(AudioLevel.level(rms: 1) == 1)
        #expect(AudioLevel.level(rms: 4) == 1) // clamped
        // -25 dBFS is halfway between the -50 floor and 0.
        let half = AudioLevel.level(rms: pow(10, -25.0 / 20))
        #expect(abs(half - 0.5) < 1e-4)
        // Quieter than the floor is silence.
        #expect(AudioLevel.level(rms: pow(10, -60.0 / 20)) == 0)
    }

    @Test func decibelsToLevel() {
        #expect(AudioLevel.level(decibels: -160) == 0)
        #expect(AudioLevel.level(decibels: 0) == 1)
        #expect(AudioLevel.level(decibels: 3) == 1)
        #expect(abs(AudioLevel.level(decibels: -10) - 0.8) < 1e-5)
        #expect(AudioLevel.level(decibels: -.infinity) == 0)
    }

    @Test func wordPulseCyclesTheFixedWave() {
        let n = AudioLevel.wordWave.count
        #expect(AudioLevel.wordPulse(0) == AudioLevel.wordWave[0])
        #expect(AudioLevel.wordPulse(n) == AudioLevel.wordWave[0])
        #expect(AudioLevel.wordPulse(n + 2) == AudioLevel.wordWave[2])
        #expect(AudioLevel.wordPulse(-1) == AudioLevel.wordWave[n - 1])
        for v in AudioLevel.wordWave { #expect(v > 0.3 && v < 0.8) } // gentle
    }

    @Test func throttleLetsThroughAboutThirtyPerSecond() {
        var t = LevelThrottle()
        var passed = 0
        // 48 kHz / 1024-frame buffers: ~47 a second, for 2 seconds.
        let step = 1024.0 / 48000
        for i in 0..<94 where t.allow(now: 100 + Double(i) * step) { passed += 1 }
        #expect(passed >= 40 && passed <= 62)
        #expect(passed < 94)
    }

    @Test func throttleFirstValueAlwaysPassesAndClockJumpsResetIt() {
        var t = LevelThrottle(interval: 0.1)
        var got: [Bool] = []
        for now in [5, 5.05, 5.15, 1, 1.01] { got.append(t.allow(now: now)) }
        // 5.05 is too soon; 1 is the clock going backwards: don't stall.
        #expect(got == [true, false, true, true, false])
    }
}
