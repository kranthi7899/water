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
}
