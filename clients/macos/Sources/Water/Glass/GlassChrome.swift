import AppKit
import CoreImage

/// The glass tab's backdrop (owner reference image 2): dark navy liquid
/// chrome — rolled folds of #050b18 -> #0d2447 -> #2a5d9a with deep black
/// creases and glossy specular crests — drawn once per size with Core
/// Graphics. The folds are frosted (a Gaussian blur: the glass), then crisp
/// tapered highlights go on top, like light catching the glass surface.
/// Drawn in the view itself rather than relying on the window's
/// behind-blur, so an offscreen render (`--render-glass`) looks like the
/// real thing.
enum GlassChrome {
    static let deep = NSColor(srgbRed: 0x05 / 255.0, green: 0x0b / 255.0, blue: 0x18 / 255.0, alpha: 1)
    static let navy = NSColor(srgbRed: 0x0d / 255.0, green: 0x24 / 255.0, blue: 0x47 / 255.0, alpha: 1)
    static let steel = NSColor(srgbRed: 0x2a / 255.0, green: 0x5d / 255.0, blue: 0x9a / 255.0, alpha: 1)
    static let bright = NSColor(srgbRed: 0x4c / 255.0, green: 0x8f / 255.0, blue: 0xd6 / 255.0, alpha: 1)
    static let icy = NSColor(srgbRed: 0xa9 / 255.0, green: 0xd4 / 255.0, blue: 0xff / 255.0, alpha: 1)

    private static var cache: (key: String, image: CGImage)?

    /// A fold: one cubic centre line (unit coordinates, y up), its width in
    /// points, and the shading from its shadowed edge to its lit crest.
    private struct Fold {
        var p: [CGPoint]
        var width: CGFloat
        var shade: [NSColor]
        /// Which way the lit crest leans, as a fraction of `width`.
        var lean: CGVector
        var crest: CGFloat
    }

    private static let folds: [Fold] = [
        // Lower left, darker.
        Fold(p: [CGPoint(x: -0.15, y: 0.34), CGPoint(x: 0.25, y: 0.46), CGPoint(x: 0.45, y: -0.02), CGPoint(x: 0.95, y: -0.12)],
             width: 150, shade: [deep, navy, steel, bright], lean: CGVector(dx: -0.10, dy: 0.16), crest: 0.55),
        // Right middle, a narrower roll.
        Fold(p: [CGPoint(x: 0.55, y: 0.62), CGPoint(x: 0.80, y: 0.55), CGPoint(x: 0.85, y: 0.25), CGPoint(x: 1.15, y: 0.18)],
             width: 90, shade: [deep, navy, steel, bright], lean: CGVector(dx: -0.14, dy: 0.12), crest: 0.5),
        // The big sweep, upper left down across the middle.
        Fold(p: [CGPoint(x: -0.15, y: 0.98), CGPoint(x: 0.35, y: 1.10), CGPoint(x: 0.40, y: 0.36), CGPoint(x: 1.15, y: 0.44)],
             width: 190, shade: [deep, navy, steel, bright, icy], lean: CGVector(dx: -0.12, dy: 0.14), crest: 0.9),
        // Top right, lit.
        Fold(p: [CGPoint(x: 0.52, y: 1.15), CGPoint(x: 0.66, y: 0.86), CGPoint(x: 0.88, y: 0.92), CGPoint(x: 1.15, y: 0.74)],
             width: 100, shade: [navy, steel, bright, icy], lean: CGVector(dx: -0.10, dy: 0.12), crest: 0.8),
    ]

    /// The frosted chrome for a view of `size` points at `scale`.
    static func image(size: NSSize, scale: CGFloat) -> CGImage? {
        let key = "\(Int(size.width))x\(Int(size.height))@\(scale)"
        if let c = cache, c.key == key { return c.image }
        guard let body = draw(size: size, scale: scale, pass: .body) else { return nil }
        let frosted = frost(body, radius: 4.5 * scale) ?? body
        let img = draw(size: size, scale: scale, pass: .gloss(frosted)) ?? frosted
        cache = (key, img)
        return img
    }

    private enum Pass { case body, gloss(CGImage) }

    private static func draw(size: NSSize, scale: CGFloat, pass: Pass) -> CGImage? {
        let w = Int(size.width * scale), h = Int(size.height * scale)
        guard w > 0, h > 0,
              let cs = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.scaleBy(x: scale, y: scale)
        let W = size.width, H = size.height
        func pts(_ f: Fold, shift: CGVector = .zero) -> [CGPoint] {
            f.p.map { CGPoint(x: $0.x * W + shift.dx, y: $0.y * H + shift.dy) }
        }

        switch pass {
        case .body:
            fillLinear(ctx, [deep, navy, steel.blended(withFraction: 0.4, of: navy)!], [0, 0.55, 1],
                       from: CGPoint(x: 0, y: 0), to: CGPoint(x: W, y: H))
            for f in folds {
                // The crease: a deep shadow the fold casts on what's under it.
                ctx.saveGState()
                ctx.setShadow(offset: CGSize(width: 0, height: -6), blur: 26, color: NSColor.black.withAlphaComponent(0.9).cgColor)
                stroke(ctx, pts(f), color: deep, width: f.width * 1.02)
                ctx.restoreGState()
                // The roll: concentric strokes narrowing and brightening
                // toward the crest, drifting along `lean`.
                let n = 48
                for i in 0..<n {
                    let t = CGFloat(i) / CGFloat(n - 1)
                    let wdt = f.width * (1 - t * 0.97)
                    let shift = CGVector(dx: f.lean.dx * f.width * t, dy: f.lean.dy * f.width * t)
                    let c = sample(f.shade, pow(t, 1.25) * f.crest)
                    stroke(ctx, pts(f, shift: shift), color: c, width: wdt)
                }
            }
            // Soft glow where light pools.
            radial(ctx, at: CGPoint(x: W * 0.30, y: H * 0.86), radius: min(W, H) * 0.34, icy.withAlphaComponent(0.22))
            radial(ctx, at: CGPoint(x: W * 0.86, y: H * 0.88), radius: min(W, H) * 0.22, icy.withAlphaComponent(0.20))
        case .gloss(let base):
            ctx.draw(base, in: CGRect(x: 0, y: 0, width: W, height: H))
            // Crisp specular crests, tapered at both ends, with a glow.
            for f in folds {
                let shift = CGVector(dx: f.lean.dx * f.width * 0.92, dy: f.lean.dy * f.width * 0.92)
                ctx.saveGState()
                ctx.setShadow(offset: .zero, blur: 12, color: icy.withAlphaComponent(0.8).cgColor)
                taper(ctx, pts(f, shift: shift), color: NSColor.white, alpha: 0.55 * f.crest, width: 2.4)
                ctx.restoreGState()
                // A broad faint sheen beside it.
                let wide = CGVector(dx: f.lean.dx * f.width * 0.7, dy: f.lean.dy * f.width * 0.7)
                taper(ctx, pts(f, shift: wide), color: icy, alpha: 0.14 * f.crest, width: 14)
            }
        }
        return ctx.makeImage()
    }

    /// The frosted-glass pass: blur so the folds read as light through
    /// glass, clamped so the edges don't fade to clear.
    private static func frost(_ img: CGImage, radius: CGFloat) -> CGImage? {
        let ci = CIImage(cgImage: img).clampedToExtent()
        guard let f = CIFilter(name: "CIGaussianBlur") else { return nil }
        f.setValue(ci, forKey: kCIInputImageKey)
        f.setValue(radius, forKey: kCIInputRadiusKey)
        guard let out = f.outputImage else { return nil }
        let extent = CGRect(x: 0, y: 0, width: img.width, height: img.height)
        return CIContext().createCGImage(out.cropped(to: extent), from: extent)
    }

    // MARK: drawing helpers

    /// Colour at `t` (0...1) along evenly spaced stops.
    private static func sample(_ stops: [NSColor], _ t: CGFloat) -> NSColor {
        let x = min(max(t, 0), 1) * CGFloat(stops.count - 1)
        let i = min(Int(x), stops.count - 2)
        return stops[i].blended(withFraction: x - CGFloat(i), of: stops[i + 1]) ?? stops[i]
    }

    private static func bezier(_ p: [CGPoint], _ t: CGFloat) -> CGPoint {
        let u = 1 - t
        let a = u * u * u, b = 3 * u * u * t, c = 3 * u * t * t, d = t * t * t
        return CGPoint(x: a * p[0].x + b * p[1].x + c * p[2].x + d * p[3].x,
                       y: a * p[0].y + b * p[1].y + c * p[2].y + d * p[3].y)
    }

    /// A stroke whose opacity swells in the middle and fades at both ends.
    private static func taper(_ ctx: CGContext, _ p: [CGPoint], color: NSColor, alpha: CGFloat, width: CGFloat) {
        let n = 90
        ctx.setLineCap(.butt)
        for i in 0..<n {
            let t0 = CGFloat(i) / CGFloat(n), t1 = CGFloat(i + 1) / CGFloat(n)
            let a = pow(sin(.pi * (t0 + t1) / 2), 2.2) * alpha
            guard a > 0.01 else { continue }
            ctx.beginPath()
            ctx.move(to: bezier(p, t0))
            ctx.addLine(to: bezier(p, t1))
            ctx.setStrokeColor(color.withAlphaComponent(a).cgColor)
            ctx.setLineWidth(width * (0.5 + 0.5 * sin(.pi * t0)))
            ctx.strokePath()
        }
    }

    static func fillLinear(_ ctx: CGContext, _ colors: [NSColor], _ locs: [CGFloat], from: CGPoint, to: CGPoint) {
        guard let g = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
                                 colors: colors.map(\.cgColor) as CFArray, locations: locs) else { return }
        ctx.drawLinearGradient(g, start: from, end: to, options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
    }

    static func radial(_ ctx: CGContext, at c: CGPoint, radius: CGFloat, _ color: NSColor) {
        guard let g = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
                                 colors: [color.cgColor, color.withAlphaComponent(0).cgColor] as CFArray,
                                 locations: [0, 1]) else { return }
        ctx.drawRadialGradient(g, startCenter: c, startRadius: 0, endCenter: c, endRadius: radius, options: [])
    }

    static func stroke(_ ctx: CGContext, _ p: [CGPoint], color: NSColor, width: CGFloat) {
        ctx.beginPath()
        ctx.move(to: p[0])
        ctx.addCurve(to: p[3], control1: p[1], control2: p[2])
        ctx.setStrokeColor(color.cgColor)
        ctx.setLineWidth(width)
        ctx.setLineCap(.round)
        ctx.strokePath()
    }
}
