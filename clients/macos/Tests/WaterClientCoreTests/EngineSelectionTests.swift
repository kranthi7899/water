import Foundation
import Testing
@testable import WaterClientCore

/// `EngineSelection.choose` is a pure function of two booleans — all four
/// combinations, each asserted directly, matching this project's existing
/// style of testing pure decision logic with fake inputs (VoiceSessionTests,
/// SentenceSpeechQueueTests) rather than mocking real hardware or a real
/// FluidAudio model download.
@Suite struct EngineSelectionTests {
    @Test func appleSiliconWithModelsReadyChoosesFluidAudio() {
        #expect(EngineSelection.choose(isAppleSilicon: true, modelsReady: true) == .fluidAudio)
    }

    @Test func appleSiliconWithoutModelsReadyChoosesAppleSpeech() {
        #expect(EngineSelection.choose(isAppleSilicon: true, modelsReady: false) == .appleSpeech)
    }

    @Test func intelOrRosettaWithModelsReadyStillChoosesAppleSpeech() {
        // Models being "ready" (already downloaded from a previous run on a
        // different, Apple Silicon disk) never matters on hardware that
        // can't run FluidAudio's Core ML graphs at all.
        #expect(EngineSelection.choose(isAppleSilicon: false, modelsReady: true) == .appleSpeech)
    }

    @Test func intelOrRosettaWithoutModelsReadyChoosesAppleSpeech() {
        #expect(EngineSelection.choose(isAppleSilicon: false, modelsReady: false) == .appleSpeech)
    }
}
