import Foundation

// Which speech engine a voice session should use, kept free of
// AppKit/FluidAudio so the decision itself is testable — mirrors how
// HoldToTalk.swift keeps VoiceSession pure and lets Sources/Water supply
// the real recognizer/synthesizer behind SpeechCapture/SpeechOutput. The
// real inputs this decision needs (actual hardware, actual model-download
// state, the one-time consent dialog) are Sources/Water's job
// (EngineSelector.swift).

/// The two speech engines Water can run on macOS: FluidAudio's on-device
/// Parakeet (ASR) + Kokoro (TTS) pair, resident on the Neural Engine, or
/// Apple's own on-device Speech / AVSpeechSynthesizer pair, which ships in
/// the OS and needs no download. Exactly two cases — FluidAudio only ever
/// has the one fallback.
public enum SpeechEngine: Equatable {
    case fluidAudio
    case appleSpeech
}

/// A pure function of two facts, no I/O and no persisted state of its own:
/// FluidAudio's Core ML models only run on Apple Silicon, and only once
/// they've actually been downloaded and loaded at least once — anything
/// else (an Intel Mac, a Mac running under Rosetta, or Apple Silicon whose
/// models aren't ready yet) uses Apple Speech, which is always available.
public enum EngineSelection {
    /// - Parameters:
    ///   - isAppleSilicon: whether this Mac's hardware is Apple Silicon
    ///     (true even when the running process itself is translated under
    ///     Rosetta — see `EngineSelector.isAppleSilicon`'s doc comment for
    ///     why that distinction matters and how it's actually detected).
    ///   - modelsReady: whether FluidAudio's models have already been
    ///     downloaded and are ready to load, for this install.
    public static func choose(isAppleSilicon: Bool, modelsReady: Bool) -> SpeechEngine {
        isAppleSilicon && modelsReady ? .fluidAudio : .appleSpeech
    }

    /// What to do at launch (V-hud's §1 leftovers). Pure: every input is a
    /// fact `EngineSelector` read from the Mac or UserDefaults.
    /// - modelsOnDisk: every Parakeet and Kokoro file is present in Water's
    ///   own models folder (checked on disk, not remembered).
    /// - declined: the user chose Apple Speech in the consent dialog.
    /// - previouslyReady: models were downloaded once before, possibly to
    ///   FluidAudio's own default folder, which Water no longer reads.
    /// - intelNoticeShown: the one-time Intel notice was already shown.
    public static func launchPlan(isAppleSilicon: Bool, modelsOnDisk: Bool, declined: Bool,
                                  previouslyReady: Bool, intelNoticeShown: Bool) -> LaunchPlan {
        guard isAppleSilicon else { return .useAppleSpeech(showIntelNotice: !intelNoticeShown) }
        if modelsOnDisk { return .useFluidAudio }
        if declined { return .useAppleSpeech(showIntelNotice: false) }
        return .askConsent(redownload: previouslyReady)
    }
}

/// See `EngineSelection.launchPlan`.
public enum LaunchPlan: Equatable {
    /// Apple Silicon and every model file is in place.
    case useFluidAudio
    /// Ask before downloading. `redownload` means a previous download went
    /// to a folder Water no longer uses, so the dialog says why it's
    /// asking again.
    case askConsent(redownload: Bool)
    /// Apple's speech. `showIntelNotice` is true once, ever, on a Mac that
    /// can't run FluidAudio at all.
    case useAppleSpeech(showIntelNotice: Bool)
}
