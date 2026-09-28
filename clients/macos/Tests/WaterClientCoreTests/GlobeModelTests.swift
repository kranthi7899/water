import Foundation
import Testing
@testable import WaterClientCore

/// The globe's pure uniform model: targets per state, and the per-frame
/// smoothing that keeps the orb from ever jumping.
@Suite struct GlobeModelTests {
    // MARK: state mapping

    @Test func phaseMapsToGlobeState() {
        #expect(GlobeState(phase: .idle) == .idle)
        #expect(GlobeState(phase: .listening) == .listening)
        #expect(GlobeState(phase: .thinking) == .thinking)
        #expect(GlobeState(phase: .responding) == .responding)
        // A waiting approval keeps the orb up, calm.
        #expect(GlobeState(phase: .needsYou) == .idle)
    }

    @Test func parsesStateNames() {
        #expect(GlobeState(name: "idle") == .idle)
        #expect(GlobeState(name: "Listening") == .listening)
        #expect(GlobeState(name: " thinking ") == .thinking)
        #expect(GlobeState(name: "responding") == .responding)
        #expect(GlobeState(name: "dancing") == nil)
    }

    // MARK: targets

    @Test func listeningEnergyIsMonotonicInLevel() {
        var prev = GlobeModel.target(state: .listening, level: 0, time: 3)
        for i in 1...20 {
            let u = GlobeModel.target(state: .listening, level: Float(i) / 20, time: 3)
            #expect(u.energy > prev.energy)
            #expect(u.speed >= prev.speed)
            #expect(u.glow >= prev.glow)
            prev = u
        }
    }

    @Test func respondingEnergyIsMonotonicInLevel() {
        var prev = GlobeModel.target(state: .responding, level: 0, time: 1)
        for i in 1...10 {
            let u = GlobeModel.target(state: .responding, level: Float(i) / 10, time: 1)
            #expect(u.energy > prev.energy)
            prev = u
        }
    }

    @Test func loudListeningIsFasterAndBrighterThanIdle() {
        let idle = GlobeModel.target(state: .idle, level: 0, time: 0)
        let loud = GlobeModel.target(state: .listening, level: 1, time: 0)
        #expect(loud.energy > idle.energy)
        #expect(loud.speed > idle.speed)
        #expect(loud.glow > idle.glow)
    }

    @Test func thinkingPulseIsTimeDrivenAndIgnoresLevel() {
        let a = GlobeModel.target(state: .thinking, level: 0, time: 0)
        let b = GlobeModel.target(state: .thinking, level: 1, time: 0)
        #expect(a == b) // audio level plays no part while thinking

        let quarter = GlobeModel.thinkingPeriod / 4
        let rising = GlobeModel.target(state: .thinking, level: 0, time: quarter)
        let falling = GlobeModel.target(state: .thinking, level: 0, time: 3 * quarter)
        #expect(rising.pulse > falling.pulse + 0.5)
        // Periodic.
        let again = GlobeModel.target(state: .thinking, level: 0, time: quarter + GlobeModel.thinkingPeriod)
        #expect(abs(again.pulse - rising.pulse) < 1e-4)
        // Slower than listening.
        #expect(rising.speed < GlobeModel.target(state: .listening, level: 0, time: 0).speed)
    }

    @Test func idleIgnoresLevel() {
        #expect(GlobeModel.target(state: .idle, level: 0, time: 2) == GlobeModel.target(state: .idle, level: 1, time: 2))
    }

    @Test func targetsStayInRangeForBadInput() {
        for s in GlobeState.allCases {
            for lv in [-5, 0, 0.5, 1, 7, Float.nan, Float.infinity] as [Float] {
                for t in [0, 1.3, -4, 1e7, Double.nan] as [TimeInterval] {
                    let u = GlobeModel.target(state: s, level: lv, time: t)
                    for v in [u.energy, u.speed, u.pulse, u.hueShift, u.glow] {
                        #expect(v.isFinite)
                    }
                    #expect(u.energy >= 0 && u.energy <= 1)
                    #expect(u.pulse >= 0 && u.pulse <= 1)
                    #expect(u.glow >= 0 && u.glow <= 1)
                    #expect(u.speed > 0 && u.speed <= 2)
                }
            }
        }
    }

    // MARK: smoothing

    @Test func smootherStartsAtIdle() {
        let s = GlobeSmoother()
        #expect(s.current == GlobeModel.target(state: .idle, level: 0, time: 0))
        #expect(s.flowTime == 0)
    }

    @Test func smootherNeverJumpsMoreThanBoundedStep() {
        var s = GlobeSmoother()
        var t: TimeInterval = 0
        // Slam between extremes, including a huge dt from a stalled frame.
        let script: [(GlobeState, Float, TimeInterval)] = [
            (.listening, 1, 1.0 / 60), (.idle, 0, 1.0 / 60), (.listening, 1, 2.0),
            (.thinking, 0, 1.0 / 120), (.responding, 1, 0.5), (.idle, 0, 10),
        ]
        for _ in 0..<30 {
            for (state, lv, dt) in script {
                let before = s.current
                t += dt
                let after = s.step(state: state, level: lv, time: t, dt: dt)
                for (a, b) in zip(before.components, after.components) {
                    #expect(abs(a - b) <= GlobeSmoother.maxStep + 1e-6)
                }
            }
        }
    }

    @Test func smootherConvergesToTarget() {
        var s = GlobeSmoother()
        let dt = 1.0 / 60
        var t: TimeInterval = 0
        for _ in 0..<240 { // 4 seconds
            t += dt
            s.step(state: .listening, level: 0.8, time: t, dt: dt)
        }
        let target = GlobeModel.target(state: .listening, level: 0.8, time: t)
        for (a, b) in zip(s.current.components, target.components) {
            #expect(abs(a - b) < 0.01)
        }
    }

    @Test func smootherMovesTowardTargetEveryFrame() {
        var s = GlobeSmoother()
        let target = GlobeModel.target(state: .listening, level: 1, time: 0)
        var last = s.current.energy
        for i in 1...30 {
            let u = s.step(state: .listening, level: 1, time: Double(i) / 60, dt: 1.0 / 60)
            #expect(u.energy > last)
            #expect(u.energy <= target.energy + 1e-6)
            last = u.energy
        }
    }

    @Test func zeroOrBadDtHoldsStill() {
        var s = GlobeSmoother()
        let before = s.current
        s.step(state: .listening, level: 1, time: 1, dt: 0)
        #expect(s.current == before)
        s.step(state: .listening, level: 1, time: 1, dt: -1)
        #expect(s.current == before)
        s.step(state: .listening, level: 1, time: 1, dt: .nan)
        #expect(s.current == before)
    }

    @Test func flowTimeAdvancesBySpeedAndNeverReverses() {
        var s = GlobeSmoother()
        var last = s.flowTime
        for i in 1...120 {
            s.step(state: i < 60 ? .listening : .thinking, level: 0.5, time: Double(i) / 60, dt: 1.0 / 60)
            #expect(s.flowTime > last)
            last = s.flowTime
        }
        // A stalled frame advances flow by at most the clamped dt.
        let before = s.flowTime
        s.step(state: .idle, level: 0, time: 100, dt: 50)
        #expect(s.flowTime - before <= GlobeSmoother.maxDT * 2 + 1e-9)
    }

    @Test func steadyStateHelperMatchesTarget() {
        let u = GlobeModel.steady(state: .responding, level: 0.6, time: 2)
        #expect(u.uniforms == GlobeModel.target(state: .responding, level: 0.6, time: 2))
        #expect(abs(u.flowTime - 2 * Double(u.uniforms.speed)) < 1e-9)
    }
}
