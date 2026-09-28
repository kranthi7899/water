// The globe's Metal shader, kept as a Swift string and compiled at runtime
// with MTLDevice.makeLibrary(source:options:): the package builds with the
// Command Line Tools alone, which ship no `metal` toolchain, so a .metal
// file isn't an option.
//
// One full-screen triangle; the fragment shader draws everything:
// - a sphere (2D circular mask read as a hemisphere, so each pixel has a
//   normal and a depth `z`), anti-aliased at its edge;
// - inside it, "liquid glass": an iq-style domain-warped fBm height field,
//   seen through a lens (coordinates bulge toward the rim), shaded as a
//   glossy surface from its own gradient — broad sheets of navy -> mid ->
//   bright -> icy blue, with white specular streaks where the warped field
//   folds steeply;
// - two sparkle layers of tiny twinkling points drifting with the flow;
// - a luminous fresnel rim, a soft glass highlight, and a soft outer glow
//   that falls off to fully clear.
// Output is premultiplied alpha on a clear background.
//
// - searching (2026-09-26): a bright arc orbiting the rim, with a soft
//   spot circling inside and a matching flare in the glow;
// - hover: a brightness lift; transparent mode: the whole orb and glow
//   desaturated to black and white and faded to `opacity`.
//
// Uniform layout (four float4s, matching GlobeParams in GlobeRenderer):
//   a = (energy, speed, pulse, hueShift)
//   b = (glow, flowTime, time, search)
//   c = (width px, height px, hover, sphere radius in clip units; 0 = 0.68)
//   d = (saturation, opacity, 0, 0)
enum GlobeShader {
    static let vertexFunction = "globe_vertex"
    static let fragmentFunction = "globe_fragment"

    static let source = """
    #include <metal_stdlib>
    using namespace metal;

    struct GlobeParams {
        float4 a;
        float4 b;
        float4 c;
        float4 d;
    };

    struct VOut {
        float4 pos [[position]];
        float2 uv;
    };

    vertex VOut globe_vertex(uint vid [[vertex_id]]) {
        float2 p = float2(float((vid << 1) & 2), float(vid & 2));
        VOut o;
        o.pos = float4(p * 2.0 - 1.0, 0.0, 1.0);
        o.uv = p * 2.0 - 1.0; // clip space, y up
        return o;
    }

    #define NAVY float3(0.039, 0.102, 0.212) // #0a1a36
    #define MID float3(0.122, 0.310, 0.561) // #1f4f8f
    #define BRIGHT float3(0.298, 0.561, 0.839) // #4c8fd6
    #define ICY float3(0.663, 0.831, 1.000) // #a9d4ff
    #define WHITE float3(0.960, 0.985, 1.000)

    static float hash21(float2 p) {
        p = fract(p * float2(123.34, 456.21));
        p += dot(p, p + 45.32);
        return fract(p.x * p.y);
    }

    static float vnoise(float2 p) {
        float2 i = floor(p);
        float2 f = fract(p);
        float2 u = f * f * f * (f * (f * 6.0 - 15.0) + 10.0);
        float a = hash21(i);
        float b = hash21(i + float2(1.0, 0.0));
        float c = hash21(i + float2(0.0, 1.0));
        float d = hash21(i + float2(1.0, 1.0));
        return mix(mix(a, b, u.x), mix(c, d, u.x), u.y);
    }

    // Three octaves only: the liquid should read as broad, smooth sheets,
    // not crinkled foil, and its gradient (the lighting) amplifies detail.
    static float fbm(float2 p) {
        const float2x2 m = float2x2(float2(1.6, 1.2), float2(-1.2, 1.6));
        float v = 0.0;
        float amp = 0.5;
        for (int i = 0; i < 3; i++) {
            v += amp * vnoise(p);
            p = m * p;
            amp *= 0.5;
        }
        return v / 0.875;
    }

    // iq's two-level domain warp, f(p + k*r(p + k*q(p))). Returns the
    // warped value and the inner warp vector r (which carries the flow's
    // direction, used to fold the sheets into ribbons).
    static float3 liquid(float2 p, float t) {
        float2 q = float2(fbm(p + 0.10 * t),
                          fbm(p + float2(5.2, 1.3) - 0.08 * t));
        float2 r = float2(fbm(p + 2.4 * q + float2(1.7, 9.2) + 0.13 * t),
                          fbm(p + 2.4 * q + float2(8.3, 2.8) - 0.11 * t));
        float w = fbm(p + 2.8 * r);
        return float3(w, r);
    }

    // The ribbon surface: the warped field folded into sheets. Returns
    // (height in -1...1, warped value w).
    static float2 ribbons(float2 p, float t) {
        float3 l = liquid(p, t);
        return float2(sin(l.x * 6.5 + l.y * 2.5 + 0.35 * t), l.x);
    }

    static float3 palette(float v, float hue) {
        v = clamp(v + hue, 0.0, 1.0);
        float3 c = mix(NAVY, MID, smoothstep(0.00, 0.35, v));
        c = mix(c, BRIGHT, smoothstep(0.30, 0.62, v));
        c = mix(c, ICY, smoothstep(0.58, 0.88, v));
        c = mix(c, WHITE, smoothstep(0.90, 1.00, v));
        return c;
    }

    // pow() is undefined for a zero base under fast math, and the sphere's
    // centre has 1 - z == 0 exactly: always go through here.
    static float spow(float x, float n) {
        return pow(max(x, 1e-5), n);
    }

    static float2 rot(float2 p, float a) {
        float s = sin(a), c = cos(a);
        return float2(c * p.x - s * p.y, s * p.x + c * p.y);
    }

    static float sparkles(float2 p, float t, float density, float px) {
        float2 cell = floor(p);
        float2 f = fract(p) - 0.5;
        float h = hash21(cell);
        float2 o = (float2(hash21(cell + 3.1), hash21(cell + 7.7)) - 0.5) * 0.7;
        float d = length(f - o);
        float tw = 0.5 + 0.5 * sin(t * (1.5 + 3.0 * h) + h * 40.0);
        tw = tw * tw * tw * tw;
        float on = step(1.0 - density, h);
        float core = exp(-d * d / (px * px * 0.9));
        return core * on * tw;
    }

    fragment float4 globe_fragment(VOut in [[stage_in]], constant GlobeParams &P [[buffer(0)]]) {
        float energy = P.a.x;
        float pulse  = P.a.z;
        float hue    = P.a.w;
        float glow   = P.b.x;
        float ft     = P.b.y;
        float time   = P.b.z;
        float search = clamp(P.b.w, 0.0, 1.0);
        float2 res   = max(P.c.xy, float2(1.0));
        float hover  = clamp(P.c.z, 0.0, 1.0);
        float sat    = clamp(P.d.x, 0.0, 1.0);
        float opac   = clamp(P.d.y, 0.0, 1.0);

        // The searching orbit: the angle of the highlight, and each
        // pixel's angular distance from it (0...pi).
        float orbit = time * 1.5;
        float ang = atan2(in.uv.y, in.uv.x * res.x / res.y);
        float dA = abs(fmod(ang - orbit + 9.42477796, 6.28318531) - 3.14159265);

        float2 uv = in.uv;
        uv.x *= res.x / res.y;

        float R0 = P.c.w > 0.0 ? P.c.w : 0.68;
        float R = R0 * (1.0 + 0.06 * energy);
        float r = length(uv) / R;
        float px = 2.0 / (res.y * R);          // one pixel, in sphere radii
        float inside = 1.0 - smoothstep(1.0 - 1.2 * px, 1.0 + 0.8 * px, r);

        // Soft outer glow, fully clear by the edge of the drawable.
        float g = max(r - 1.0, 0.0);
        float glowA = exp(-g * (9.0 - 3.0 * pulse)) * (0.22 + 0.30 * glow + 0.22 * pulse);
        glowA *= 1.0 - smoothstep(0.30, 0.46, g);
        float3 glowC = mix(BRIGHT, ICY, 0.35 + 0.3 * glow);
        glowA *= 1.0 + 0.35 * hover;
        // The search flare rides the glow just outside the rim.
        glowA += search * exp(-dA * dA / 0.20) * exp(-g * 14.0) * 0.55 * (1.0 - smoothstep(0.30, 0.46, g));
        glowC = mix(glowC, ICY, 0.5 * search);

        float3 sphere = float3(0.0);
        if (r < 1.0 + 2.0 * px) {
            float rr = min(r, 1.0);
            float z = sqrt(max(1.0 - rr * rr, 0.0));
            float2 s = uv / R;                 // unit disc

            // Lens: the liquid bulges toward the rim and turns slowly.
            float2 q = s * (1.0 + 0.55 * (1.0 - z));
            // A gentle vortex: the core turns faster than the rim, so the
            // sheets wind into swirling ribbons.
            q = rot(q, 0.06 * ft + 1.1 * (1.0 - rr * rr));
            float scale = 0.95 + 0.30 * energy;
            // Stretched along one axis so the sheets read as long ribbons.
            float2 p = float2(q.x * 0.55, q.y * 1.15) * scale + float2(3.1, 1.7);

            float2 rb = ribbons(p, ft);
            float e = 0.012;
            float hx = ribbons(p + float2(e, 0.0), ft).x;
            float hy = ribbons(p + float2(0.0, e), ft).x;
            float2 grad = float2(hx - rb.x, hy - rb.x) / e;
            float H = rb.x;
            float w = rb.y;

            float3 n = normalize(float3(-grad * 0.06, 1.0));
            float3 L = normalize(float3(-0.45, 0.62, 0.64));
            float3 V = float3(0.0, 0.0, 1.0);
            float diff = clamp(dot(n, L), 0.0, 1.0);
            float3 Hv = normalize(L + V);
            float nh = clamp(dot(n, Hv), 0.0, 1.0);
            float spec = spow(nh, 90.0);
            float sheen = spow(nh, 8.0);

            // Broad light and dark zones from the warp, ribbons on top.
            float depth = smoothstep(0.30, 0.78, w);
            float v = 0.02 + 0.44 * depth + 0.16 * (0.5 + 0.5 * H) + 0.06 * energy;
            float3 c = palette(v, hue);
            c *= 0.32 + 0.80 * diff;
            c += BRIGHT * sheen * (0.20 + 0.30 * energy) * (0.4 + 0.6 * depth);
            c += mix(ICY, WHITE, 0.65) * spec * (0.95 + 1.10 * energy) * (0.30 + 0.70 * depth);
            // Bright strands along the ribbon edges (where each sheet turns
            // over), like light caught in the folds of glass.
            float edge = spow(1.0 - abs(H), 10.0) * smoothstep(8.0, 22.0, length(grad));
            c += ICY * edge * (0.25 + 0.45 * energy) * (0.35 + 0.65 * depth);

            // Richer blues: push saturation back up after the white terms.
            float luma = dot(c, float3(0.299, 0.587, 0.114));
            c = max(mix(float3(luma), c, 1.35), 0.0);

            // Glass volume: lit from the upper left, deeper toward the
            // lower right and a little darker toward the rim...
            float3 sn = float3(s, z);
            c *= 0.62 + 0.55 * clamp(dot(sn, L) * 0.5 + 0.5, 0.0, 1.0);
            c *= 0.75 + 0.25 * z;
            // ...then a luminous fresnel rim.
            float fres = spow(1.0 - z, 2.2);
            c = mix(c, mix(BRIGHT, ICY, 0.6), fres * (0.18 + 0.35 * glow));
            c += ICY * spow(1.0 - z, 9.0) * (0.30 + 0.60 * glow);

            // Soft glass highlight, upper left, plus a crisp small one.
            float2 hl = s - float2(-0.36, 0.44);
            c += WHITE * exp(-dot(hl, hl) / 0.05) * 0.22;
            float2 hs = s - float2(-0.30, 0.52);
            c += WHITE * exp(-dot(hs, hs) / 0.0018) * 0.55;
            // Faint bounce light at the lower right.
            float2 bl = s - float2(0.45, -0.55);
            c += BRIGHT * exp(-dot(bl, bl) / 0.06) * 0.18;

            // Sparkles drifting with the flow.
            float2 sp = rot(q, 0.03 * ft) + float2(0.07 * ft, -0.05 * ft);
            float spk = sparkles(sp * 14.0, time * 1.3, 0.24, 14.0 * px * 1.1)
                      + 0.7 * sparkles(sp * 23.0 + 11.0, time * 1.7, 0.16, 23.0 * px * 0.8);
            // Tiny drawables (the docked mini orb) would turn the sparkles
            // into noise: fade them out there.
            spk *= smoothstep(70.0, 180.0, res.y);
            c += mix(ICY, WHITE, 0.6) * spk * (0.9 + 0.8 * energy) * (0.4 + 0.6 * z);

            // Searching: a bright arc orbiting the rim, a trailing tail,
            // and a soft spot circling inside.
            float arc = exp(-dA * dA / 0.12) * spow(1.0 - z, 1.4);
            float tailA = abs(fmod(ang - (orbit - 0.8) + 9.42477796, 6.28318531) - 3.14159265);
            float tail = exp(-tailA * tailA / 0.45) * spow(1.0 - z, 1.8);
            float2 spot = s - 0.58 * float2(cos(orbit), sin(orbit));
            // Cooler: the whole orb leans teal-cyan while searching.
            c = mix(c, c * float3(0.70, 1.02, 1.05) + float3(0.0, 0.03, 0.04), search);
            c += search * (mix(ICY, WHITE, 0.55) * (2.6 * arc + 0.9 * tail)
                           + mix(ICY, WHITE, 0.3) * exp(-dot(spot, spot) / 0.03) * 0.8);

            // Hover: a gentle lift.
            c *= 1.0 + 0.30 * hover;

            // Soft tone map so highlights roll off instead of clipping.
            c = 1.0 - exp(-c * 1.35);
            sphere = c;
        }

        // A big sphere (the docked mini orb) leaves little room: fade the
        // glow out before the drawable's edge so it never shows a square.
        glowA *= 1.0 - smoothstep(0.86, 1.0, max(abs(in.uv.x), abs(in.uv.y)));
        glowA = clamp(glowA, 0.0, 1.0);
        float3 outC = sphere * inside + glowC * glowA * (1.0 - inside);
        float outA = inside + glowA * (1.0 - inside);
        // Transparent mode: black and white (a touch lifted so the dark
        // navy doesn't read as a hole), then faded. Premultiplied, so
        // colour and alpha scale together.
        float lum = dot(outC, float3(0.299, 0.587, 0.114));
        outC = mix(float3(min(lum * 1.15, outA)), outC, sat);
        return float4(outC * opac, outA * opac);
    }
    """
}
