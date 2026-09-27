import Foundation

// The globe's pure model (owner brief 2026-09-26): what the orb's shader
// is fed each frame, derived from the ActivityModel's phase and its
// display level. No AppKit, no Metal, so every rule is unit-tested
// (GlobeModelTests). Sources/Water/Globe/ draws it.
//
// - idle: a slow, calm flow.
// - listening: swells and brightens with live mic amplitude; flow speeds up.
// - thinking: a slower hypnotic swirl with a time-driven breathing pulse.
// - responding: moves with the TTS output level.
// - searching: a web search (research.web) is running — a cooler, icier
//   orb with a highlight slowly orbiting its rim (2026-09-26).
//
// On top of the state, a `GlobeLook` from the host: `hover` (the pointer
// is over the orb: a brightness lift) and `transparent` (the owner's
// transparent mode: black and white and a little see-through, so it
// doesn't disrupt other apps). Both are smoothed like everything else.
//
// Every frame goes through `GlobeSmoother`, an exponential follower with a
// hard per-frame step cap, so a state change or a level spike is never a
// jump on screen.

/// What the globe is doing. `needsYou` (an approval waiting) shows as a
/// calm idle orb.
public enum GlobeState: String, CaseIterable, Equatable {
    case idle, listening, thinking, responding, searching

    public init(phase: ActivityModel.Phase) {
        switch phase {
        case .idle, .needsYou: self = .idle
        case .listening: self = .listening
        case .thinking: self = .thinking
        case .responding: self = .responding
        case .searching: self = .searching
        }
    }

    /// Parses a CLI name (`--state listening`), case-insensitively.
    public init?(name: String) {
        self.init(rawValue: name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased())
    }
}

/// The host's overlay on the state: the pointer over the orb, and the
/// owner's transparent mode.
public struct GlobeLook: Equatable {
    public var hover: Bool
    public var transparent: Bool

    public init(hover: Bool = false, transparent: Bool = false) {
        self.hover = hover
        self.transparent = transparent
    }

    public static let normal = GlobeLook()
}

/// The shader's inputs, each finite. energy, pulse, glow, search, hover,
/// saturation and opacity are 0...1; speed is a flow-rate multiplier in
/// (0, 2]; hueShift is a small signed tint (-0.2...0.2) toward icy (+) or
/// deep navy (-).
public struct GlobeUniforms: Equatable {
    /// Ribbon brightness and scale, plus a slight radius swell.
    public var energy: Float
    /// How fast the liquid flows.
    public var speed: Float
    /// Modulates the outer glow (breathing while thinking, amplitude otherwise).
    public var pulse: Float
    public var hueShift: Float
    /// Fresnel rim strength.
    public var glow: Float
    /// The searching look: a highlight orbiting the rim.
    public var search: Float
    /// The hover lift (pointer over the orb).
    public var hover: Float
    /// 1 = full colour, 0 = black and white (transparent mode).
    public var saturation: Float
    /// Overall opacity (transparent mode fades it a little).
    public var opacity: Float

    public init(energy: Float, speed: Float, pulse: Float, hueShift: Float, glow: Float,
                search: Float = 0, hover: Float = 0, saturation: Float = 1, opacity: Float = 1) {
        self.energy = energy
        self.speed = speed
        self.pulse = pulse
        self.hueShift = hueShift
        self.glow = glow
        self.search = search
        self.hover = hover
        self.saturation = saturation
        self.opacity = opacity
    }

    public var components: [Float] { [energy, speed, pulse, hueShift, glow, search, hover, saturation, opacity] }

    init(components c: [Float]) {
        self.init(energy: c[0], speed: c[1], pulse: c[2], hueShift: c[3], glow: c[4],
                  search: c[5], hover: c[6], saturation: c[7], opacity: c[8])
    }
}

/// One frame to draw: the uniforms, the integrated flow clock (advances by
/// `speed`, so speeding up never rewinds the pattern) and the wall clock
/// (sparkle twinkle).
public struct GlobeFrame: Equatable {
    public var uniforms: GlobeUniforms
    public var flowTime: Double
    public var time: Double

    public init(uniforms: GlobeUniforms, flowTime: Double, time: Double) {
        self.uniforms = uniforms
        self.flowTime = flowTime
        self.time = time
    }
}

public enum GlobeModel {
    /// The thinking breath's period, seconds.
    public static let thinkingPeriod: TimeInterval = 2.4
    /// Idle's much slower, shallower breath.
    public static let idlePeriod: TimeInterval = 6
    /// The searching breath's period (the orbit itself is in the shader).
    public static let searchingPeriod: TimeInterval = 3.2
    /// How much hovering lifts energy and glow.
    public static let hoverLift: Float = 0.14
    /// Transparent mode's opacity.
    public static let transparentOpacity: Float = 0.62

    /// Where the uniforms want to be for `state` at `level` (0...1, clamped;
    /// only listening and responding read it) and wall-clock `time`, with
    /// the host's `look` on top.
    public static func target(state: GlobeState, level: Float, time: TimeInterval,
                              look: GlobeLook = .normal) -> GlobeUniforms {
        var u = base(state: state, level: level, time: time)
        if look.hover {
            u.energy = min(1, u.energy + hoverLift)
            u.glow = min(1, u.glow + hoverLift)
            u.hover = 1
        }
        if look.transparent {
            u.saturation = 0
            u.opacity = transparentOpacity
        }
        return u
    }

    private static func base(state: GlobeState, level: Float, time: TimeInterval) -> GlobeUniforms {
        let lv = level.isFinite ? min(1, max(0, level)) : 0
        let t = time.isFinite ? time : 0
        switch state {
        case .idle:
            let b = breath(t, period: idlePeriod)
            return GlobeUniforms(energy: 0.30 + 0.04 * b, speed: 0.35, pulse: 0.30 + 0.2 * b,
                                 hueShift: 0, glow: 0.50)
        case .listening:
            return GlobeUniforms(energy: 0.45 + 0.55 * lv, speed: 0.85 + 0.95 * lv, pulse: 0.35 + 0.65 * lv,
                                 hueShift: 0.06 + 0.06 * lv, glow: 0.62 + 0.38 * lv)
        case .thinking:
            let b = breath(t, period: thinkingPeriod)
            return GlobeUniforms(energy: 0.40 + 0.12 * b, speed: 0.28, pulse: b,
                                 hueShift: -0.06, glow: 0.52 + 0.22 * b)
        case .responding:
            return GlobeUniforms(energy: 0.42 + 0.50 * lv, speed: 0.60 + 0.60 * lv, pulse: 0.30 + 0.70 * lv,
                                 hueShift: 0.03 + 0.05 * lv, glow: 0.58 + 0.32 * lv)
        case .searching:
            let b = breath(t, period: searchingPeriod)
            return GlobeUniforms(energy: 0.40 + 0.08 * b, speed: 0.45, pulse: 0.35 + 0.35 * b,
                                 hueShift: 0.14, glow: 0.62 + 0.18 * b, search: 1)
        }
    }

    /// A settled frame (as if the state had held forever) for offscreen
    /// renders: target uniforms with flowTime = time * speed.
    public static func steady(state: GlobeState, level: Float, time: TimeInterval,
                              look: GlobeLook = .normal) -> GlobeFrame {
        let t = time.isFinite ? time : 0
        let u = target(state: state, level: level, time: t, look: look)
        return GlobeFrame(uniforms: u, flowTime: t * Double(u.speed), time: t)
    }

    /// 0...1, starting at 0.5 and rising.
    static func breath(_ t: TimeInterval, period: TimeInterval) -> Float {
        Float(0.5 + 0.5 * sin(2 * Double.pi * t / period))
    }
}

/// The per-frame follower: exponential smoothing toward the target with a
/// hard cap of `maxStep` per component per frame, and dt clamped to
/// `maxDT` so a stalled frame (the window was hidden, the machine slept)
/// resumes gently.
public struct GlobeSmoother: Equatable {
    /// Seconds for a component to cover ~63% of the way to its target.
    public static let timeConstant: Double = 0.16
    /// The most any component may move in one frame.
    public static let maxStep: Float = 0.08
    /// The longest frame the smoother will integrate.
    public static let maxDT: Double = 0.1

    public private(set) var current: GlobeUniforms
    public private(set) var flowTime: Double = 0
    public private(set) var time: Double = 0

    public init(start: GlobeUniforms = GlobeModel.target(state: .idle, level: 0, time: 0)) {
        current = start
    }

    public var frame: GlobeFrame { GlobeFrame(uniforms: current, flowTime: flowTime, time: time) }

    /// Advances one frame of `dt` seconds toward `state`/`level` at
    /// wall-clock `time`. A non-positive or non-finite dt changes nothing.
    @discardableResult
    public mutating func step(state: GlobeState, level: Float, time t: TimeInterval, dt: TimeInterval,
                              look: GlobeLook = .normal) -> GlobeUniforms {
        guard dt.isFinite, dt > 0 else { return current }
        let d = min(dt, Self.maxDT)
        let alpha = Float(1 - exp(-d / Self.timeConstant))
        let target = GlobeModel.target(state: state, level: level, time: t, look: look)
        let next = zip(current.components, target.components).map { cur, tgt -> Float in
            let delta = (tgt - cur) * alpha
            return cur + min(Self.maxStep, max(-Self.maxStep, delta))
        }
        current = GlobeUniforms(components: next)
        flowTime += d * Double(current.speed)
        if t.isFinite { time = t }
        return current
    }
}
