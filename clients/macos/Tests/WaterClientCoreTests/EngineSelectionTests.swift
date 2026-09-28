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
    // MARK: launchPlan (V-hud's section 1 leftovers)

    /// Intel: Apple Speech, with the notice exactly once, whatever else is
    /// true.
    @Test func intelShowsTheNoticeOnceAndNeverAsks() {
        for ready in [false, true] {
            for declined in [false, true] {
                #expect(EngineSelection.launchPlan(isAppleSilicon: false, modelsOnDisk: ready, declined: declined,
                                                   previouslyReady: ready, intelNoticeShown: false)
                        == .useAppleSpeech(showIntelNotice: true))
                #expect(EngineSelection.launchPlan(isAppleSilicon: false, modelsOnDisk: ready, declined: declined,
                                                   previouslyReady: ready, intelNoticeShown: true)
                        == .useAppleSpeech(showIntelNotice: false))
            }
        }
    }

    @Test func appleSiliconWithModelsOnDiskUsesThem() {
        #expect(EngineSelection.launchPlan(isAppleSilicon: true, modelsOnDisk: true, declined: false,
                                           previouslyReady: true, intelNoticeShown: false) == .useFluidAudio)
        // Files on disk win even over an old decline (they got there somehow).
        #expect(EngineSelection.launchPlan(isAppleSilicon: true, modelsOnDisk: true, declined: true,
                                           previouslyReady: false, intelNoticeShown: false) == .useFluidAudio)
    }

    @Test func firstLaunchAsks() {
        #expect(EngineSelection.launchPlan(isAppleSilicon: true, modelsOnDisk: false, declined: false,
                                           previouslyReady: false, intelNoticeShown: false)
                == .askConsent(redownload: false))
    }

    /// Models downloaded before to FluidAudio's default folder: Water now
    /// reads only its own folder, so it asks again (never downloads
    /// silently) and says why.
    @Test func modelsInTheOldFolderAskAgainAsARedownload() {
        #expect(EngineSelection.launchPlan(isAppleSilicon: true, modelsOnDisk: false, declined: false,
                                           previouslyReady: true, intelNoticeShown: false)
                == .askConsent(redownload: true))
    }

    @Test func aDeclineIsRespectedAndNoIntelNoticeOnAppleSilicon() {
        #expect(EngineSelection.launchPlan(isAppleSilicon: true, modelsOnDisk: false, declined: true,
                                           previouslyReady: true, intelNoticeShown: false)
                == .useAppleSpeech(showIntelNotice: false))
    }
}
