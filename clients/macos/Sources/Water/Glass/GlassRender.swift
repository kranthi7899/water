import AppKit
import WaterClientCore

/// `Water --render-glass <out.png> --sample email|readback|draft|display|notice|warning|confirm`: renders the
/// glass tab with sample content offscreen into a PNG (with alpha) and
/// exits, so its look can be checked without a daemon, a window server
/// session or a real approval. Touches no network, daemon or UserDefaults.
enum GlassRender {
    static func run(arguments: [String]) -> Int32 {
        func value(_ flag: String) -> String? {
            guard let i = arguments.firstIndex(of: flag), i + 1 < arguments.count else { return nil }
            return arguments[i + 1]
        }
        guard let out = value("--render-glass"), !out.hasPrefix("--") else {
            FileHandle.standardError.write(Data("usage: Water --render-glass <out.png> --sample email|readback|draft|display|notice|warning|confirm\n".utf8))
            return 2
        }
        let sample = value("--sample") ?? "email"
        guard let item = Self.sample(sample) else {
            FileHandle.standardError.write(Data("unknown --sample \(sample) (email, readback, draft, display, notice, warning, confirm)\n".utf8))
            return 2
        }
        do {
            try render(item, to: URL(fileURLWithPath: out))
            return 0
        } catch {
            FileHandle.standardError.write(Data("render-glass: \(error)\n".utf8))
            return 1
        }
    }

    /// Renders one item at 2x into a PNG.
    static func render(_ item: GlassItem, to url: URL, scale: CGFloat = 2) throws {
        _ = NSApplication.shared // AppKit text rendering wants an app object.
        let view = GlassTabView(frame: NSRect(x: 0, y: 0, width: GlassTabView.width, height: 200))
        view.appearance = NSAppearance(named: .darkAqua)
        view.opaqueBackdrop = true
        view.render(item)
        let size = view.preferredSize
        view.setFrameSize(size)
        view.layoutSubtreeIfNeeded()
        view.layout()

        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(size.width * scale), pixelsHigh: Int(size.height * scale),
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                         colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else {
            throw WaterClientError.protocolError("no bitmap")
        }
        rep.size = size
        view.cacheDisplay(in: view.bounds, to: rep)
        guard let png = rep.representation(using: .png, properties: [:]) else { throw WaterClientError.protocolError("png encode failed") }
        try png.write(to: url)
    }

    static func sample(_ name: String) -> GlassItem? {
        switch name {
        case "email":
            let body = """
            Hi Dana,

            Thanks for the nudge on the board deck. The finance section is still waiting on the Q3 close, which lands Wednesday, so I'd like to send the full deck Thursday morning rather than today. That still gives everyone two full days before the meeting.

            If the committee wants an early look, I can share the strategy and hiring pages tonight; they're final. Let me know if that helps, or if Thursday is too late for anyone.

            Best,
            Kranthi
            """
            var item = GlassItem(kind: .approval(id: "env_sample", action: "gmail.send_message", risk: "high",
                                                 readBack: "Send email to dana@example.com, subject 'Re: Board deck timing'.",
                                                 payloadHash: "sha256:sample"),
                                 email: EmailFields(to: ["dana@example.com"], cc: ["board-office@example.com"],
                                                    subject: "Re: Board deck timing", body: body),
                                 readBack: "Send email to dana@example.com, subject 'Re: Board deck timing'.")
            item.tool = nil
            return item
        case "draft":
            return GlassItem(kind: .draft,
                             email: EmailFields(to: ["dana@example.com"], subject: "Re: Board deck timing",
                                                body: "Hi Dana,\n\nThursday morning works for the full deck.\n\nBest,\nKranthi"),
                             readBack: "Draft to dana@example.com", tool: "gmail.draft_for_review")
        case "readback":
            let rb = "Create a calendar event 'Board prep: deck review' on Thursday, October 1 from 9:00 to 9:45 AM (America/New_York), " +
                "with dana@example.com and cfo@example.com, location 'Zoom'. Description: 'Walk through the finance pages before the deck goes out.'. " +
                "Say yes to create it or no to cancel."
            return GlassItem(kind: .approval(id: "env_sample2", action: "gcal.create_event", risk: "medium",
                                             readBack: rb, payloadHash: "sha256:sample2"),
                             email: nil, readBack: rb)
        case "display":
            return GlassItem.decide(channel: .voice, kind: .artifact, tool: "display.show", artifactType: "display",
                                    title: "Runway at the current burn",
                                    body: "Cash on hand: $4.2M\nNet burn: $300k / month\nRunway: 14 months (to November 2027)\n\n" +
                                        "If the two Q4 hires start in October, runway drops to 12 months.")
        case "warning":
            // Slice W D4c: an envelope whose recipient check warned (the
            // CEO confirmed a look-alike domain; its mail server is missing).
            let rb = "Send email to kranthi@gmial.com, subject 'Job application'."
            return GlassItem(kind: .approval(id: "env_sample3", action: "gmail.send_message", risk: "high",
                                             readBack: rb, payloadHash: "sha256:sample3"),
                             email: EmailFields(to: ["kranthi@gmial.com"], subject: "Job application",
                                                body: "Hi Kranthi,\n\nAttaching the role description we talked about.\n\nBest,\nWater"),
                             readBack: rb,
                             warnings: ["gmial.com looks like gmail.com. You confirmed this address; check it before you tap Approve.",
                                        "gmial.com has no mail server, so this email would bounce."])
        case "confirm":
            // Slice W D5b: a spoken yes armed the two-step send.
            let rb = "Send email to dana@example.com, subject 'Re: Board deck timing'."
            return GlassItem(kind: .approval(id: "env_sample4", action: "gmail.send_message", risk: "high",
                                             readBack: rb, payloadHash: "sha256:sample4"),
                             email: EmailFields(to: ["dana@example.com"], subject: "Re: Board deck timing",
                                                body: "Hi Dana,\n\nThursday morning works for the full deck.\n\nBest,\nKranthi"),
                             readBack: rb, confirmPhrase: "confirm send")
        case "notice":
            return GlassItem.notice("Hold ⌃V to talk. Esc to leave voice mode.")
        default:
            return nil
        }
    }
}
