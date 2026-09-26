import Foundation

/// `Water --voice-bench [--n <count>] [--tts apple|kokoro]`'s arguments,
/// parsed purely so the rules are testable. The bench itself is
/// Sources/Water/VoiceBench.swift.
public struct VoiceBenchOptions: Equatable {
    public enum TTS: String, Equatable {
        /// AVSpeechSynthesizer: nothing to download, runs anywhere.
        case apple
        /// Kokoro via FluidAudio: needs the models already downloaded
        /// (the app's consent dialog); the bench never downloads.
        case kokoro
    }

    public static let defaultN = 30

    public var n = defaultN
    public var tts: TTS = .apple

    public init(n: Int = defaultN, tts: TTS = .apple) {
        self.n = n
        self.tts = tts
    }

    /// Parses the process arguments (argv[0] first). Errors are one-line,
    /// user-facing messages.
    public static func parse(_ arguments: [String]) -> Result<VoiceBenchOptions, BenchArgumentError> {
        var o = VoiceBenchOptions()
        var args = Array(arguments.dropFirst().filter { $0 != "--voice-bench" })
        func value(_ flag: String) -> Result<String?, BenchArgumentError> {
            guard let i = args.firstIndex(of: flag) else { return .success(nil) }
            guard i + 1 < args.count else { return .failure(BenchArgumentError("\(flag) needs a value")) }
            let v = args[i + 1]
            args.removeSubrange(i...(i + 1))
            return .success(v)
        }
        switch value("--n") {
        case .failure(let e): return .failure(e)
        case .success(let v?):
            guard let n = Int(v), n > 0 else {
                return .failure(BenchArgumentError("--n needs a positive integer, got \(v.debugDescription)"))
            }
            o.n = n
        case .success(nil): break
        }
        switch value("--tts") {
        case .failure(let e): return .failure(e)
        case .success(let v?):
            guard let t = TTS(rawValue: v.lowercased()) else {
                return .failure(BenchArgumentError("--tts is apple or kokoro, got \(v.debugDescription)"))
            }
            o.tts = t
        case .success(nil): break
        }
        return .success(o)
    }
}

public struct BenchArgumentError: Error, Equatable {
    public let message: String
    public init(_ message: String) { self.message = message }
}
