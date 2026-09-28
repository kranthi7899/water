import AVFoundation
import Foundation
import WaterClientCore

/// `Water --asr-selftest <file.wav>`: drives the real `ParakeetCapture`
/// (the same `start`/`endAudio` calls `VoiceSession` makes, the same
/// per-hold `ParakeetSession`, the same shared models in
/// ~/Library/Application Support/Water/Models) with a file instead of the
/// mic, for two holds in a row, and prints each transcript. Exits 0 only
/// when both holds produce text — the second hold is the one that caught
/// the dead-input-stream bug (a reused manager returned "" from then on).
/// Runs before any app or daemon setup; reads the models, never downloads
/// unless they're missing.
enum AsrSelfTest {
    static let usage = "usage: Water --asr-selftest <file.wav>"

    static func run(arguments args: [String]) -> Int32 {
        guard let i = args.firstIndex(of: "--asr-selftest"), i + 1 < args.count else {
            FileHandle.standardError.write(Data((usage + "\n").utf8))
            return 2
        }
        let url = URL(fileURLWithPath: args[i + 1])
        let buffers: [AVAudioPCMBuffer]
        do {
            buffers = try FileSource.chunks(of: url)
        } catch {
            FileHandle.standardError.write(Data("asr-selftest: can't read \(url.path): \(error)\n".utf8))
            return 2
        }
        print("asr-selftest: \(buffers.count) buffers of ~100ms from \(url.lastPathComponent)")

        let source = FileSource(buffers: buffers)
        let capture = ParakeetCapture(source: source)
        var ok = true
        for hold in 1...2 {
            let t0 = Date()
            var final: SpeechResult?
            var partials = 0
            do {
                try capture.start { result in
                    switch result {
                    case .partial: partials += 1
                    case .final, .error: final = result
                    }
                }
            } catch {
                print("asr-selftest: hold \(hold) start failed: \(error)")
                return 1
            }
            capture.endAudio() // like releasing the key right after speaking
            let deadline = Date().addingTimeInterval(180)
            while final == nil && Date() < deadline {
                RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.05))
            }
            let ms = Int(Date().timeIntervalSince(t0) * 1000)
            switch final {
            case .final(let text)?:
                print("asr-selftest: hold \(hold): \"\(text)\" (\(partials) partials, \(ms)ms)")
                if text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { ok = false }
            case .error(let message)?:
                print("asr-selftest: hold \(hold) error: \(message)")
                ok = false
            default:
                print("asr-selftest: hold \(hold) timed out")
                ok = false
            }
        }
        capture.cancel()
        print(ok ? "asr-selftest: PASS" : "asr-selftest: FAIL")
        return ok ? 0 : 1
    }
}

/// A `PCMSource` that replays a file's buffers, all at once, on `start`.
private final class FileSource: PCMSource {
    private let buffers: [AVAudioPCMBuffer]
    init(buffers: [AVAudioPCMBuffer]) { self.buffers = buffers }

    func start(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        for b in buffers { onBuffer(b) }
    }

    func stop() {}

    /// The file's audio in ~100ms buffers in its processing format (like
    /// a mic tap's).
    static func chunks(of url: URL) throws -> [AVAudioPCMBuffer] {
        let file = try AVAudioFile(forReading: url)
        let frames = AVAudioFrameCount(max(1, file.processingFormat.sampleRate / 10))
        var out: [AVAudioPCMBuffer] = []
        while file.framePosition < file.length {
            guard let b = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: frames) else { break }
            try file.read(into: b)
            if b.frameLength == 0 { break }
            out.append(b)
        }
        return out
    }
}
