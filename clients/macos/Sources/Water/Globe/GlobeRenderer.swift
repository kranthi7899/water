import Metal
import WaterClientCore

/// The globe's Metal pipeline, shared by the live view (GlobeMetalView) and
/// the offscreen PNG path (GlobeRender), so a preview is exactly what the
/// app draws. The shader is compiled at runtime from GlobeShader.source.
/// `init` returns nil (never crashes) when the device can't build it; the
/// caller then falls back to GlobeFallbackView.
final class GlobeRenderer {
    static let pixelFormat: MTLPixelFormat = .bgra8Unorm

    let device: MTLDevice
    let queue: MTLCommandQueue
    private let pipeline: MTLRenderPipelineState

    /// Matches `struct GlobeParams` in the shader: four float4s.
    struct Params {
        var a: SIMD4<Float>
        var b: SIMD4<Float>
        var c: SIMD4<Float>
        var d: SIMD4<Float>
    }

    init?(device: MTLDevice) {
        guard let queue = device.makeCommandQueue() else { return nil }
        do {
            let library = try device.makeLibrary(source: GlobeShader.source, options: Self.compileOptions())
            guard let vf = library.makeFunction(name: GlobeShader.vertexFunction),
                  let ff = library.makeFunction(name: GlobeShader.fragmentFunction) else { return nil }
            let desc = MTLRenderPipelineDescriptor()
            desc.label = "globe"
            desc.vertexFunction = vf
            desc.fragmentFunction = ff
            // The shader writes premultiplied color + alpha itself over a
            // clear target, so no blending is needed.
            desc.colorAttachments[0].pixelFormat = Self.pixelFormat
            desc.colorAttachments[0].isBlendingEnabled = false
            pipeline = try device.makeRenderPipelineState(descriptor: desc)
        } catch {
            NSLog("Water globe: Metal shader unavailable (%@); using the fallback orb", "\(error)")
            return nil
        }
        self.device = device
        self.queue = queue
    }

    /// Safe (IEEE) math, not the default fast math: on macOS 15.6's
    /// compiler, fast math zeroed the blue channel across the whole sphere
    /// once the three highlight terms were combined (each alone was fine) —
    /// a miscompile, since safe math gives sane values everywhere. The
    /// drawable is ~200px square, so the cost is negligible.
    static func compileOptions() -> MTLCompileOptions {
        let o = MTLCompileOptions()
        if #available(macOS 15.0, *) {
            o.mathMode = .safe
        } else {
            o.fastMathEnabled = false
        }
        return o
    }

    /// Convenience: the system default device, or nil.
    static func makeDefault() -> GlobeRenderer? {
        guard let device = MTLCreateSystemDefaultDevice() else { return nil }
        return GlobeRenderer(device: device)
    }

    /// The floating orb's sphere radius in clip units (GlobeView.sphereFraction).
    static let defaultSphereRadius: Float = 0.68

    static func params(_ frame: GlobeFrame, width: Int, height: Int, sphereRadius: Float = defaultSphereRadius) -> Params {
        let u = frame.uniforms
        // Keep the float clocks small enough to stay precise; the pattern
        // is continuous within a period this long and nobody watches an
        // interaction for hours.
        let flow = Float(frame.flowTime.truncatingRemainder(dividingBy: 10_000))
        let time = Float(frame.time.truncatingRemainder(dividingBy: 10_000))
        return Params(
            a: SIMD4(u.energy, u.speed, u.pulse, u.hueShift),
            b: SIMD4(u.glow, flow, time, u.search),
            c: SIMD4(Float(max(1, width)), Float(max(1, height)), u.hover, sphereRadius),
            d: SIMD4(u.saturation, u.opacity, 0, 0)
        )
    }

    /// Encodes one frame into `pass` (whose color attachment must use
    /// `pixelFormat` and should clear to transparent).
    func encode(_ frame: GlobeFrame, pass: MTLRenderPassDescriptor, width: Int, height: Int,
                sphereRadius: Float = defaultSphereRadius, into commandBuffer: MTLCommandBuffer) {
        guard let enc = commandBuffer.makeRenderCommandEncoder(descriptor: pass) else { return }
        var p = Self.params(frame, width: width, height: height, sphereRadius: sphereRadius)
        enc.setRenderPipelineState(pipeline)
        enc.setFragmentBytes(&p, length: MemoryLayout<Params>.stride, index: 0)
        enc.drawPrimitives(type: .triangle, vertexStart: 0, vertexCount: 3)
        enc.endEncoding()
    }

    static func clearPass(texture: MTLTexture) -> MTLRenderPassDescriptor {
        let pass = MTLRenderPassDescriptor()
        pass.colorAttachments[0].texture = texture
        pass.colorAttachments[0].loadAction = .clear
        pass.colorAttachments[0].storeAction = .store
        pass.colorAttachments[0].clearColor = MTLClearColor(red: 0, green: 0, blue: 0, alpha: 0)
        return pass
    }

    /// Renders one frame offscreen and returns its BGRA premultiplied
    /// pixels (row-major, top row first, 4 bytes per pixel), or nil.
    func renderPixels(_ frame: GlobeFrame, size: Int, sphereRadius: Float = defaultSphereRadius) -> [UInt8]? {
        let side = max(8, min(size, 4096))
        let desc = MTLTextureDescriptor.texture2DDescriptor(pixelFormat: Self.pixelFormat,
                                                            width: side, height: side, mipmapped: false)
        desc.usage = [.renderTarget, .shaderRead]
        desc.storageMode = device.hasUnifiedMemory ? .shared : .managed
        guard let tex = device.makeTexture(descriptor: desc),
              let cb = queue.makeCommandBuffer() else { return nil }
        encode(frame, pass: Self.clearPass(texture: tex), width: side, height: side, sphereRadius: sphereRadius, into: cb)
        if tex.storageMode == .managed, let blit = cb.makeBlitCommandEncoder() {
            blit.synchronize(resource: tex)
            blit.endEncoding()
        }
        cb.commit()
        cb.waitUntilCompleted()
        if cb.status == .error { return nil }
        var bytes = [UInt8](repeating: 0, count: side * side * 4)
        bytes.withUnsafeMutableBytes { buf in
            tex.getBytes(buf.baseAddress!, bytesPerRow: side * 4,
                         from: MTLRegionMake2D(0, 0, side, side), mipmapLevel: 0)
        }
        return bytes
    }
}
