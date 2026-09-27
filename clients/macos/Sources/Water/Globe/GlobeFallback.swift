import AppKit
import QuartzCore
import WaterClientCore

/// The globe without Metal: a radial CAGradientLayer orb in the same
/// palette (navy core -> mid -> bright -> icy rim), a soft glow layer
/// behind it and a glass highlight on top. It follows the same
/// GlobeUniforms — energy swells and brightens it, pulse breathes the glow,
/// speed turns the highlight — so the orb still reads as listening,
/// thinking and responding. Searching adds a spark orbiting the rim, hover
/// lifts it, and transparent mode greys every colour and fades the whole
/// view (GlobeUniforms.saturation/opacity). Driven by a 30Hz timer while
/// not paused.
final class GlobeFallbackView: NSView {
    var frameSource: (() -> GlobeFrame?)?
    var isPaused = true { didSet { isPaused ? stop() : start() } }
    /// Sphere diameter / view side (the docked mini orb's is bigger).
    var sphereFraction: CGFloat = GlobeView.sphereFraction { didSet { needsLayout = true } }

    private let glowLayer = CAGradientLayer()
    private let orbLayer = CAGradientLayer()
    private let rimLayer = CAGradientLayer()
    private let highlightLayer = CAGradientLayer()
    private let orbitLayer = CAGradientLayer()
    private var timer: Timer?
    private var orbRect = CGRect.zero

    static let navy = NSColor(srgbRed: 0.039, green: 0.102, blue: 0.212, alpha: 1)   // #0a1a36
    static let mid = NSColor(srgbRed: 0.122, green: 0.310, blue: 0.561, alpha: 1)    // #1f4f8f
    static let bright = NSColor(srgbRed: 0.298, green: 0.561, blue: 0.839, alpha: 1) // #4c8fd6
    static let icy = NSColor(srgbRed: 0.663, green: 0.831, blue: 1.0, alpha: 1)      // #a9d4ff

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.backgroundColor = .clear
        for g in [glowLayer, orbLayer, rimLayer, highlightLayer, orbitLayer] {
            g.type = .radial
            layer?.addSublayer(g)
        }
        glowLayer.colors = [Self.bright.withAlphaComponent(0.55).cgColor, Self.bright.withAlphaComponent(0).cgColor]
        glowLayer.locations = [0.55, 1.0]
        orbLayer.colors = [Self.bright.cgColor, Self.mid.cgColor, Self.navy.cgColor]
        orbLayer.locations = [0.0, 0.55, 1.0]
        orbLayer.startPoint = CGPoint(x: 0.42, y: 0.58)
        orbLayer.endPoint = CGPoint(x: 1.05, y: 1.05)
        rimLayer.colors = [Self.icy.withAlphaComponent(0).cgColor, Self.icy.withAlphaComponent(0).cgColor,
                           Self.icy.withAlphaComponent(0.85).cgColor]
        rimLayer.locations = [0.0, 0.78, 1.0]
        highlightLayer.colors = [NSColor.white.withAlphaComponent(0.75).cgColor, NSColor.white.withAlphaComponent(0).cgColor]
        highlightLayer.locations = [0.0, 1.0]
        orbitLayer.locations = [0.0, 1.0]
        orbitLayer.startPoint = CGPoint(x: 0.5, y: 0.5)
        orbitLayer.endPoint = CGPoint(x: 1, y: 1)
        orbitLayer.opacity = 0
        layoutOrb(scale: 1)
        apply(GlobeSmoother().frame)
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    override func layout() {
        super.layout()
        layoutOrb(scale: 1)
    }

    private func layoutOrb(scale: CGFloat) {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        let side = min(bounds.width, bounds.height)
        let d = side * sphereFraction * scale
        let orb = CGRect(x: bounds.midX - d / 2, y: bounds.midY - d / 2, width: d, height: d)
        orbRect = orb
        glowLayer.frame = bounds
        glowLayer.startPoint = CGPoint(x: 0.5, y: 0.5)
        glowLayer.endPoint = CGPoint(x: 1, y: 1)
        for l in [orbLayer, rimLayer] {
            l.frame = orb
            l.cornerRadius = d / 2
            l.masksToBounds = true
        }
        rimLayer.startPoint = CGPoint(x: 0.5, y: 0.5)
        rimLayer.endPoint = CGPoint(x: 1, y: 1)
        let h = d * 0.42
        highlightLayer.frame = CGRect(x: orb.minX + d * 0.16, y: orb.maxY - d * 0.16 - h, width: h, height: h)
        highlightLayer.startPoint = CGPoint(x: 0.5, y: 0.5)
        highlightLayer.endPoint = CGPoint(x: 1, y: 1)
        CATransaction.commit()
    }

    /// Draws one frame's uniforms (also used directly by offscreen renders).
    func apply(_ frame: GlobeFrame) {
        let u = frame.uniforms
        let sat = CGFloat(min(1, max(0, u.saturation)))
        func tone(_ c: NSColor, _ a: CGFloat = 1) -> CGColor { Self.tone(c, saturation: sat).withAlphaComponent(a).cgColor }
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        layoutOrb(scale: 1 + 0.06 * CGFloat(u.energy))
        layer?.opacity = min(1, max(0, u.opacity))
        orbLayer.opacity = 1
        let lift = min(1, CGFloat(u.energy) + 0.3 * CGFloat(u.hover))
        orbLayer.colors = [tone(Self.icy.blended(withFraction: 1 - lift, of: Self.bright)!),
                           tone(Self.mid), tone(Self.navy)]
        glowLayer.colors = [tone(Self.bright, 0.55), tone(Self.bright, 0)]
        rimLayer.colors = [tone(Self.icy, 0), tone(Self.icy, 0), tone(Self.icy, 0.85)]
        // Searching: a small icy spark orbiting just inside the rim.
        let orbit = CGFloat(frame.time) * 1.5
        let spark = orbRect.width * 0.34
        let rr = orbRect.width * 0.36
        orbitLayer.frame = CGRect(x: orbRect.midX + rr * cos(orbit) - spark / 2,
                                  y: orbRect.midY + rr * sin(orbit) - spark / 2, width: spark, height: spark)
        orbitLayer.colors = [tone(Self.icy.blended(withFraction: 0.5, of: .white)!, 1), tone(Self.icy, 0)]
        orbitLayer.opacity = min(1, max(0, u.search))
        // The inner light drifts slowly around the centre with the flow.
        let a = CGFloat(frame.flowTime) * 0.6
        orbLayer.startPoint = CGPoint(x: 0.45 + 0.08 * cos(a), y: 0.55 + 0.08 * sin(a))
        rimLayer.opacity = Float(0.35 + 0.65 * u.glow)
        glowLayer.opacity = Float(0.25 + 0.35 * u.glow + 0.3 * u.pulse)
        highlightLayer.opacity = 0.6 + 0.3 * u.energy
        CATransaction.commit()
    }

    /// `c` pulled toward its own grey by 1 - `saturation`.
    static func tone(_ c: NSColor, saturation: CGFloat) -> NSColor {
        guard saturation < 1, let rgb = c.usingColorSpace(.sRGB) else { return c }
        let l = min(1, (0.299 * rgb.redComponent + 0.587 * rgb.greenComponent + 0.114 * rgb.blueComponent) * 1.15)
        let mix = { (v: CGFloat) in l + (v - l) * saturation }
        return NSColor(srgbRed: mix(rgb.redComponent), green: mix(rgb.greenComponent), blue: mix(rgb.blueComponent),
                       alpha: rgb.alphaComponent)
    }

    private func start() {
        guard timer == nil else { return }
        let t = Timer(timeInterval: 1.0 / 30, repeats: true) { [weak self] _ in
            guard let self, let f = self.frameSource?() else { return }
            self.apply(f)
        }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    private func stop() {
        timer?.invalidate()
        timer = nil
    }
}
