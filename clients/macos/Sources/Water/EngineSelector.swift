import AppKit
import Darwin
import FluidAudio
import Foundation
import WaterClientCore

/// The real orchestration behind `EngineSelection.choose` (WaterClientCore,
/// pure): detects actual hardware once, tracks actual FluidAudio
/// model-download state across launches, and — the first time this Mac
/// would ever use FluidAudio — shows a one-time consent dialog before
/// downloading anything, matching `AppDelegate.explainAccessibilityOnce`'s
/// exact shape (a `UserDefaults`-backed boolean guard, an `NSAlert` with two
/// buttons, `NSApp.activate` then `runModal()`).
///
/// This type itself has no unit test, same as `OnDeviceSpeechCapture`/
/// `KokoroSpeaker`/`ParakeetCapture` — it's real permission UI, hardware
/// detection and a real model download, none of which is worth (or able to
/// be) faked in a unit test. `EngineSelectionTests` (WaterClientCoreTests)
/// covers the actual decision this type feeds inputs into.
enum EngineSelector {
    private static let declinedKey = "fluidAudioDeclined"
    private static let modelsReadyKey = "fluidAudioModelsReady"

    /// The one `KokoroAneManager` instance for this process. `resolve`
    /// below calls `initialize()` on it once, during the consent/download
    /// flow, so AppDelegate can hand this same already-loaded instance to
    /// `KokoroSpeaker` afterwards instead of loading a second one — a fresh
    /// `KokoroAneManager` would find the model files already downloaded
    /// (they're cached on disk, not per-instance) but would still have to
    /// load them into memory again.
    static let kokoroManager = KokoroAneManager()

    /// Cheap, hardware-only check, read once and cached (the hardware can't
    /// change mid-run): true when this Mac's *hardware* is Apple Silicon,
    /// even if this process itself is running translated under Rosetta.
    /// `sysctlbyname("hw.optional.arm64", ...)` is Apple's own documented
    /// idiom for exactly that distinction — a compile-time `#if arch(arm64)`
    /// check would instead report the architecture of *this binary*, which
    /// is wrong for a Rosetta-translated launch on Apple Silicon hardware
    /// and (the case this project actually cares about) wrong the other way
    /// for an Intel Mac, where FluidAudio's Core ML/ANE path can't run at
    /// all regardless of which slice launched.
    ///
    /// Worth remembering here: `KokoroAneManager` has its own (non-public,
    /// so not callable or gate-able from this file) `isBnnsCrashProneOS`
    /// check for a real Apple BNNS crash bug on macOS 26.4–26.5 and iOS
    /// 26.4+/27 — `initialize()` already checks it internally and logs a
    /// warning on an affected build. This dev machine (15.6.1) isn't in
    /// that range, so nothing further is needed today, but a future OS
    /// upgrade could put it in range, and engine selection has no way to
    /// see that from outside FluidAudio unless a future FluidAudio release
    /// exposes the check publicly.
    static let isAppleSilicon: Bool = {
        var value: Int32 = 0
        var size = MemoryLayout<Int32>.size
        let ok = sysctlbyname("hw.optional.arm64", &value, &size, nil, 0) == 0
        return ok && value == 1
    }()

    /// Resolves which engine this run should use. Calls `then` on the main
    /// thread, exactly once — synchronously, for every path except the
    /// very first "not yet decided" one, where it returns only after the
    /// consent alert and (if accepted) the download finish.
    static func resolve(then: @escaping (SpeechEngine) -> Void) {
        let d = UserDefaults.standard
        let alreadyReady = d.bool(forKey: modelsReadyKey)
        // The pure decision (WaterClientCore): given what's true *right
        // now*, is there any reason to ask at all? `.fluidAudio` here means
        // Apple Silicon with models already downloaded from a previous
        // launch — done, nothing to decide. `.appleSpeech` covers both
        // "never will be" (Intel/Rosetta) and "not yet, but could be" (Apple
        // Silicon, first time) — the two are told apart just below, since
        // only the second is worth a consent dialog.
        if EngineSelection.choose(isAppleSilicon: isAppleSilicon, modelsReady: alreadyReady) == .fluidAudio {
            return then(.fluidAudio)
        }
        guard isAppleSilicon, !d.bool(forKey: declinedKey) else { return then(.appleSpeech) }

        let alert = NSAlert()
        alert.messageText = "Use on-device speech models?"
        alert.informativeText = """
        Water can talk and listen using FluidAudio's on-device speech models (Kokoro and Parakeet), which run entirely on this Mac's Neural Engine — no audio ever leaves it.

        This downloads about 1.5 GB the first time. If you'd rather not, Water keeps using Apple's built-in Speech and dictation instead — nothing to download, and you won't be asked again.
        """
        alert.addButton(withTitle: "Download and Use")
        alert.addButton(withTitle: "Use Apple Speech")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else {
            // Permanent for this install: no re-prompt, and (deliberately,
            // this slice) no settings UI to opt back in later — see
            // docs/known-gaps.md.
            d.set(true, forKey: declinedKey)
            return then(.appleSpeech)
        }

        showDownloadProgress { success in
            if success {
                d.set(true, forKey: modelsReadyKey)
            }
            // A failed download/initialize falls back to Apple Speech for
            // *this run only* — `declinedKey` is deliberately untouched, so
            // a transient failure (a network blip, say) doesn't turn into
            // the same permanent opt-out a real decline would; the next
            // launch is asked nothing and just tries again.
            then(success ? .fluidAudio : .appleSpeech)
        }
    }

    /// `resolve`, blocked on until it calls back. Every branch but one
    /// resolves synchronously already (no UI, or a modal alert that only
    /// returns once the user answers); the one that doesn't — accepting
    /// the download — keeps pumping the main run loop, the same technique
    /// `NSAlert.runModal()` and `NSApplication.runModal(for:)` use
    /// internally, so the app's event loop (and the progress window's own
    /// spinner and label) stays fully responsive while this function
    /// hasn't returned yet. Keeping this blocking (rather than threading an
    /// optional `VoiceController!` through the rest of AppDelegate) is a
    /// deliberate simplicity trade-off: this only ever runs once, ever, per
    /// Mac, and only for someone who both has Apple Silicon and hasn't
    /// already answered the consent dialog.
    static func resolveBlocking() -> SpeechEngine {
        var result: SpeechEngine?
        resolve { result = $0 }
        while result == nil {
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.05))
        }
        return result!
    }

    // MARK: - download progress

    /// Three discrete phase labels — "Downloading…", "Compiling…",
    /// "Ready." — over an indeterminate spinner, deliberately *not* a
    /// byte-level progress bar: `KokoroAneManager.initialize(preloadVoices:)`
    /// takes no progress-handler parameter (see docs/known-gaps.md, "Kokoro
    /// model-download progress is phase-only"). Only FluidAudio's
    /// lower-level `ModelHub`/`KokoroAneResourceDownloader` calls expose
    /// one, and reaching around `initialize()` into those internals — which
    /// changed shape five times in three days during this slice's planning
    /// pass — would trade a UI nicety for an ongoing maintenance liability.
    ///
    /// Because `initialize()` is one opaque call with no phase signal at
    /// all, the "Downloading…" → "Compiling…" transition below is a timed
    /// *guess* (download is normally the long pole; compiling the 7
    /// mlmodelcs onto the ANE is normally fast by comparison), not a real
    /// observation — an honest limitation of the phase-only approach.
    /// "Ready." is real: it's only shown once `initialize()` has actually
    /// returned.
    ///
    /// The window itself is plain, non-modal AppKit (`makeKeyAndOrderFront`,
    /// not `NSApp.runModal(for:)`) — nothing here blocks input to the rest
    /// of the app the way the consent `NSAlert` above deliberately does.
    private static func showDownloadProgress(completion: @escaping (Bool) -> Void) {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 340, height: 100),
                               styleMask: [.titled], backing: .buffered, defer: false)
        window.title = "Water"
        window.isReleasedWhenClosed = false
        window.center()

        let label = NSTextField(labelWithString: "Downloading speech models…")
        label.frame = NSRect(x: 20, y: 55, width: 300, height: 20)
        let spinner = NSProgressIndicator(frame: NSRect(x: 20, y: 30, width: 300, height: 16))
        spinner.style = .bar
        spinner.isIndeterminate = true
        spinner.startAnimation(nil)

        let content = NSView(frame: NSRect(x: 0, y: 0, width: 340, height: 100))
        content.addSubview(label)
        content.addSubview(spinner)
        window.contentView = content

        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)

        // Guessed timing for the "Compiling…" label — see the doc comment
        // above. Cancelled implicitly if the window is already gone by
        // then (the label update is harmless on a closed window).
        DispatchQueue.main.asyncAfter(deadline: .now() + 2.5) {
            label.stringValue = "Compiling speech models…"
        }

        Task {
            do {
                try await kokoroManager.initialize()
                await MainActor.run {
                    label.stringValue = "Ready."
                    spinner.stopAnimation(nil)
                    // Briefly visible rather than an instant close, so
                    // "Ready." is an actual observed phase, not just a
                    // value that flashes past in the same run-loop tick.
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) {
                        window.close()
                        completion(true)
                    }
                }
            } catch {
                // Never crash, never retry in a loop: one failed attempt
                // just closes the window and reports failure — `resolve`
                // above turns that into "use Apple Speech for this run."
                await MainActor.run {
                    window.close()
                    completion(false)
                }
            }
        }
    }
}
