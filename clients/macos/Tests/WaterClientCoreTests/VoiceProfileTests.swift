import Foundation
import Testing
@testable import WaterClientCore

@Suite struct VoiceProfileTests {
    /// The exact shape internal/gateway/router.go's handleVoiceProfile
    /// serves (GET /v1/voice/profile).
    static let fixture = #"{"name":"Water","handoff":["One moment.","Let me check.","On it."],"tts":{"voice":"Samantha","rate_wpm":185}}"#

    @Test func decodesRealServerShape() throws {
        let profile = try JSONDecoder().decode(VoiceProfile.self, from: Data(Self.fixture.utf8))
        #expect(profile.name == "Water")
        #expect(profile.handoff == ["One moment.", "Let me check.", "On it."])
        #expect(profile.tts.voice == "Samantha")
        #expect(profile.tts.rateWPM == 185)
    }

    @Test func decodesEmptyVoiceAndHandoff() throws {
        let raw = #"{"name":"Water","handoff":[],"tts":{"voice":"","rate_wpm":185}}"#
        let profile = try JSONDecoder().decode(VoiceProfile.self, from: Data(raw.utf8))
        #expect(profile.tts.voice.isEmpty)
        #expect(profile.handoff.isEmpty)
    }

    /// Slice V's V-voice sub-slice (V-7): an optional Kokoro voice name,
    /// present in the server's JSON when style.yaml's voice.tts.kokoro_voice
    /// is set.
    @Test func decodesKokoroVoiceWhenPresent() throws {
        let raw = #"{"name":"Water","handoff":[],"tts":{"voice":"Samantha","rate_wpm":185,"kokoro_voice":"af_heart"}}"#
        let profile = try JSONDecoder().decode(VoiceProfile.self, from: Data(raw.utf8))
        #expect(profile.tts.kokoroVoice == "af_heart")
    }

    /// The server omits the key entirely (internal/gateway/router.go's
    /// handleVoiceProfile) when style.yaml has no kokoro_voice — this must
    /// decode to `nil`, not an empty string or a decode failure.
    @Test func decodesNilKokoroVoiceWhenAbsent() throws {
        let profile = try JSONDecoder().decode(VoiceProfile.self, from: Data(Self.fixture.utf8))
        #expect(profile.tts.kokoroVoice == nil)
    }

    @Test func fetchOverCannedServer() throws {
        let body = Self.fixture
        let server = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\n\r\n" + body])
        let profile = try UnixSocketClient(socketPath: server.path).fetchVoiceProfile(token: "t")
        server.wait()
        #expect(profile.tts.rateWPM == 185)
        #expect(String(decoding: server.request, as: UTF8.self).hasPrefix("GET /v1/voice/profile HTTP/1.1\r\n"))
    }

    @Test func fetchThrowsOn404() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 404 Not Found\r\nContent-Length: 9\r\n\r\nnot found"])
        #expect(throws: WaterClientError.http(status: 404, body: "not found")) {
            try UnixSocketClient(socketPath: server.path).fetchVoiceProfile(token: "t")
        }
    }

    // MARK: - rate_wpm -> AVSpeechUtterance.rate

    @Test func rateMappingKnownPairs() {
        // Anchors: 90 wpm -> minimum rate, 280 wpm -> maximum rate.
        #expect(TTSRateMapping.rate(forWPM: 90) == 0.0)
        #expect(TTSRateMapping.rate(forWPM: 280) == 1.0)
        // style.yaml's own default (185 wpm) lands exactly on
        // AVSpeechUtteranceDefaultSpeechRate (0.5) by construction:
        // (185-90)/(280-90) == 0.5.
        #expect(TTSRateMapping.rate(forWPM: 185) == 0.5)
    }

    @Test func rateMappingClampsOutOfRangeWPM() {
        #expect(TTSRateMapping.rate(forWPM: 10) == 0.0)
        #expect(TTSRateMapping.rate(forWPM: 1000) == 1.0)
        #expect(TTSRateMapping.rate(forWPM: 0) == 0.0)
    }

    @Test func rateMappingIsLinearBetweenAnchors() {
        // Halfway between the anchors in wpm terms is halfway in rate terms.
        let midWPM = Int((TTSRateMapping.minWPM + TTSRateMapping.maxWPM) / 2)
        #expect(abs(TTSRateMapping.rate(forWPM: midWPM) - 0.5) < 0.01)
    }

    // MARK: - handoff event kind decodes safely

    @Test func handoffEventDecodesWithoutError() {
        let e = TurnEvent.decode(line: Data(#"{"kind":"handoff","text":"One moment."}"#.utf8))
        #expect(e?.kind == .handoff)
        #expect(e?.text == "One moment.")
    }

    @Test func handoffDistinctFromUnknown() {
        #expect(TurnEvent.decode(line: Data(#"{"kind":"handoff"}"#.utf8))?.kind == .handoff)
        #expect(TurnEvent.decode(line: Data(#"{"kind":"some_future_kind"}"#.utf8))?.kind == .unknown("some_future_kind"))
    }
}
