import AppKit
import ImageIO
import UniformTypeIdentifiers
import WaterClientCore

/// `Water --render-globe <out.png> [--state idle|listening|thinking|responding|searching]
/// [--level 0..1] [--time <seconds>] [--size <px>] [--hover] [--transparent] [--mini] [--fallback]`
///
/// `--hover` and `--transparent` render the hover lift and transparent
/// (black-and-white) mode; `--mini` renders the docked menu-bar orb's
/// geometry (GlobeDockItem.sphereRadius; its real size is `--size 44`);
/// `--fallback` forces the no-Metal gradient orb.
///
/// Renders ONE globe frame offscreen through the same Metal pipeline the
/// app uses (GlobeRenderer + GlobeShader) into a PNG with alpha, then
/// exits. The frame is the settled state (GlobeModel.steady), as if the
/// state had held for `time` seconds. With no Metal it renders the
/// gradient fallback instead and says so. Touches no daemon, network or
/// UserDefaults. main.swift calls `run(arguments:)` and exits with its code.
enum GlobeRender {
    struct Options: Equatable {
        var out: String
        var state: GlobeState = .idle
        var level: Float = 0
        var time: Double = 2
        var size: Int = 512
        var hover = false
        var transparent = false
        var mini = false
        var fallback = false
    }

    static let usage = "usage: Water --render-globe <out.png> [--state idle|listening|thinking|responding|searching] [--level 0..1] [--time <seconds>] [--size <px>] [--hover] [--transparent] [--mini] [--fallback]"

    struct UsageError: Error, Equatable { let message: String }

    private static func fail(_ m: String) -> Result<Options, UsageError> { .failure(UsageError(message: m)) }

    static func parse(_ args: [String]) -> Result<Options, UsageError> {
        guard let i = args.firstIndex(of: "--render-globe"), i + 1 < args.count,
              !args[i + 1].hasPrefix("--") else { return fail(usage) }
        var o = Options(out: args[i + 1])
        func value(_ flag: String) -> String?? {
            guard let j = args.firstIndex(of: flag) else { return .some(nil) }
            return j + 1 < args.count ? .some(args[j + 1]) : .none
        }
        switch value("--state") {
        case .none: return fail("--state needs a value\n" + usage)
        case .some(nil): break
        case .some(let s?):
            guard let st = GlobeState(name: s) else { return fail("unknown --state \(s)\n" + usage) }
            o.state = st
        }
        switch value("--level") {
        case .none: return fail("--level needs a value\n" + usage)
        case .some(nil): break
        case .some(let s?):
            guard let v = Float(s), v.isFinite else { return fail("bad --level \(s)") }
            o.level = min(1, max(0, v))
        }
        switch value("--time") {
        case .none: return fail("--time needs a value\n" + usage)
        case .some(nil): break
        case .some(let s?):
            guard let v = Double(s), v.isFinite, v >= 0 else { return fail("bad --time \(s)") }
            o.time = v
        }
        switch value("--size") {
        case .none: return fail("--size needs a value\n" + usage)
        case .some(nil): break
        case .some(let s?):
            guard let v = Int(s), (16...4096).contains(v) else { return fail("bad --size \(s) (16...4096)") }
            o.size = v
        }
        o.hover = args.contains("--hover")
        o.transparent = args.contains("--transparent")
        o.mini = args.contains("--mini")
        o.fallback = args.contains("--fallback")
        return .success(o)
    }

    static func run(arguments: [String]) -> Int32 {
        let o: Options
        switch parse(arguments) {
        case .failure(let e):
            let msg = e.message
            FileHandle.standardError.write(Data((msg + "\n").utf8))
            return 2
        case .success(let v): o = v
        }
        let frame = GlobeModel.steady(state: o.state, level: o.level, time: o.time,
                                      look: GlobeLook(hover: o.hover, transparent: o.transparent))
        let image: CGImage?
        if !o.fallback, let renderer = GlobeRenderer.makeDefault(), let px = renderer.renderPixels(frame, size: o.size,
            sphereRadius: o.mini ? GlobeDockItem.sphereRadius : GlobeRenderer.defaultSphereRadius) {
            image = makeImage(bgraPremultiplied: px, side: o.size)
        } else {
            if !o.fallback {
                FileHandle.standardError.write(Data("render-globe: Metal unavailable; rendering the fallback orb\n".utf8))
            }
            image = renderFallback(frame, size: o.size, sphereFraction: o.mini ? CGFloat(GlobeDockItem.sphereRadius) : GlobeView.sphereFraction)
        }
        guard let image, writePNG(image, to: o.out) else {
            FileHandle.standardError.write(Data("render-globe: could not write \(o.out)\n".utf8))
            return 1
        }
        let look = (o.hover ? ", hover" : "") + (o.transparent ? ", transparent" : "") + (o.mini ? ", mini" : "")
        print("render-globe: wrote \(o.out) (\(o.size)px, \(o.state.rawValue), level \(o.level), t \(o.time)s\(look))")
        return 0
    }

    static func makeImage(bgraPremultiplied bytes: [UInt8], side: Int) -> CGImage? {
        guard let provider = CGDataProvider(data: Data(bytes) as CFData) else { return nil }
        let info = CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedFirst.rawValue
            | CGBitmapInfo.byteOrder32Little.rawValue)
        return CGImage(width: side, height: side, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: side * 4,
                       space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: info, provider: provider,
                       decode: nil, shouldInterpolate: false, intent: .defaultIntent)
    }

    static func renderFallback(_ frame: GlobeFrame, size: Int, sphereFraction: CGFloat = GlobeView.sphereFraction) -> CGImage? {
        let view = GlobeFallbackView(frame: NSRect(x: 0, y: 0, width: size, height: size))
        view.sphereFraction = sphereFraction
        view.layoutSubtreeIfNeeded()
        view.apply(frame)
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return nil }
        view.cacheDisplay(in: view.bounds, to: rep)
        return rep.cgImage
    }

    static func writePNG(_ image: CGImage, to path: String) -> Bool {
        let url = URL(fileURLWithPath: path)
        guard let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)
        else { return false }
        CGImageDestinationAddImage(dest, image, nil)
        return CGImageDestinationFinalize(dest)
    }
}
