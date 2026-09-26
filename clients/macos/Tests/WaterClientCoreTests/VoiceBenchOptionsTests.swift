import Foundation
import Testing
@testable import WaterClientCore

@Suite struct VoiceBenchOptionsTests {
    func parse(_ a: String...) -> Result<VoiceBenchOptions, BenchArgumentError> {
        VoiceBenchOptions.parse(["Water", "--voice-bench"] + a)
    }

    @Test func defaultsAreThirtyTurnsWithAppleSpeech() throws {
        #expect(try parse().get() == VoiceBenchOptions(n: 30, tts: .apple))
    }

    @Test func ttsKokoroAndN() throws {
        #expect(try parse("--tts", "kokoro").get() == VoiceBenchOptions(n: 30, tts: .kokoro))
        #expect(try parse("--n", "5", "--tts", "Kokoro").get() == VoiceBenchOptions(n: 5, tts: .kokoro))
        #expect(try parse("--tts", "apple", "--n", "2").get() == VoiceBenchOptions(n: 2, tts: .apple))
    }

    @Test func badValuesAreRefused() {
        for bad in [["--n", "0"], ["--n", "x"], ["--n"], ["--tts"], ["--tts", "say"]] {
            guard case .failure = VoiceBenchOptions.parse(["Water", "--voice-bench"] + bad) else {
                Issue.record("accepted \(bad)")
                continue
            }
        }
    }
}
