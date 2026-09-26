import AppKit
import Darwin
import FluidAudio
import Foundation
import WaterClientCore

/// The real orchestration behind `EngineSelection.launchPlan`
/// (WaterClientCore, pure): detects actual hardware once, checks the model
/// files on disk in Water's own folder (`SpeechModels`), shows the Intel
/// notice once, and — whenever this Mac would use FluidAudio but the models
/// aren't there — shows a consent dialog before downloading anything, matching `AppDelegate.explainAccessibilityOnce`'s
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
    /// Set once a download finished. Still read after the move to Water's
    /// own models folder: it means "downloaded before, somewhere", so a
    /// Mac whose models sit in FluidAudio's old default folder is asked
    /// again with a re-download explanation, never downloaded silently.
    private static let modelsReadyKey = "fluidAudioModelsReady"
    private static let intelNoticeKey = "didExplainIntelSpeech"

    /// The one `KokoroAneManager` instance for this process, pointed at
    /// `SpeechModels.kokoroDirectory`. The consent/download flow calls
    /// `initialize()` on it, so AppDelegate hands this same already-loaded
    /// instance to `KokoroSpeaker` afterwards instead of loading a second.
    static let kokoroManager = SpeechModels.makeKokoroManager()

    /// The one Parakeet capture for this process: the download flow loads
    /// its models (inside the progress window), and AppDelegate hands this
    /// same instance to `VoiceController`, so they load once.
    static let parakeet = ParakeetCapture()

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
    /// one that downloads, which returns only after the download finishes.
    /// The decision itself is `EngineSelection.launchPlan` (pure, tested).
    static func resolve(then: @escaping (SpeechEngine) -> Void) {
        let d = UserDefaults.standard
        let plan = EngineSelection.launchPlan(
            isAppleSilicon: isAppleSilicon,
            modelsOnDisk: isAppleSilicon && SpeechModels.allOnDisk(),
            declined: d.bool(forKey: declinedKey),
            previouslyReady: d.bool(forKey: modelsReadyKey),
            intelNoticeShown: d.bool(forKey: intelNoticeKey))
        switch plan {
        case .useFluidAudio:
            return then(.fluidAudio)
        case .useAppleSpeech(let showIntelNotice):
            if showIntelNotice {
                d.set(true, forKey: intelNoticeKey)
                explainIntel()
            }
            return then(.appleSpeech)
        case .askConsent(let redownload):
            guard askConsent(redownload: redownload) else {
                // Permanent for this install: no re-prompt, and
                // (deliberately) no settings UI to opt back in later —
                // see docs/known-gaps.md.
                d.set(true, forKey: declinedKey)
                return then(.appleSpeech)
            }
            showDownloadProgress { success in
                if success { d.set(true, forKey: modelsReadyKey) }
                // A failed download/initialize falls back to Apple Speech
                // for *this run only* — `declinedKey` is deliberately
                // untouched, so a transient failure (a network blip, say)
                // doesn't turn into the same permanent opt-out a real
                // decline would; the next launch asks again.
                then(success ? .fluidAudio : .appleSpeech)
            }
        }
    }

    /// The consent dialog: nothing downloads before "Download and Use".
    private static func askConsent(redownload: Bool) -> Bool {
        let alert = NSAlert()
        alert.messageText = redownload ? "Download the speech models again?" : "Use on-device speech models?"
        let why = redownload
            ? "Water now keeps its speech models in its own folder (~/Library/Application Support/Water/Models), so it needs to download them there once. The older copy FluidAudio made isn't used any more; you can delete it.\n\n"
            : ""
        alert.informativeText = why + """
        Water can talk and listen using FluidAudio's on-device speech models (Kokoro and Parakeet), which run entirely on this Mac's Neural Engine — no audio ever leaves it.

        This downloads about 1.5 GB. If you'd rather not, Water keeps using Apple's built-in Speech and dictation instead — nothing to download, and you won't be asked again.
        """
        alert.addButton(withTitle: "Download and Use")
        alert.addButton(withTitle: "Use Apple Speech")
        NSApp.activate(ignoringOtherApps: true)
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Shown once, ever, on a Mac that can't run FluidAudio (Intel). The
    /// same "explain once" shape as AppDelegate's Accessibility notice.
    private static func explainIntel() {
        let alert = NSAlert()
        alert.messageText = "Water is using Apple's speech on this Mac"
        alert.informativeText = """
        Water's on-device Parakeet and Kokoro voices need Apple silicon. On this Mac, Water listens with macOS dictation and speaks with the system voice instead.

        Both still run on this Mac, and this is shown only once.
        """
        alert.addButton(withTitle: "OK")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    /// `resolve`, blocked on until it calls back. Every branch but one
    /// resolves synchronously already (no UI, or a modal alert that only
    /// returns once the user answers); the one that doesn't — accepting
    /// the download — keeps pumping the main run loop, the same technique
    /// `NSAlert.runModal()` and `NSApplication.runModal(for:)` use
    /// internally, so the app's event loop (and the progress window's own
    /// bar and label) stays fully responsive while this function hasn't
    /// returned yet. This only ever runs the download once per Mac.
    static func resolveBlocking() -> SpeechEngine {
        var result: SpeechEngine?
        resolve { result = $0 }
        while result == nil {
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.05))
        }
        return result!
    }

    // MARK: - download progress

    /// Both models, one window: Parakeet (speech recognition), then Kokoro
    /// (the voice), each with FluidAudio's real download progress (its
    /// public `progressHandler`s: file counts and a fraction, then a
    /// "compiling" phase), then loading both — Kokoro's `initialize()`
    /// (which also fetches the shared G2P assets) and Parakeet's model
    /// load — so the first hold and the first reply don't stall on it.
    /// Plain non-modal AppKit, like before.
    private static func showDownloadProgress(completion: @escaping (Bool) -> Void) {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 380, height: 110),
                               styleMask: [.titled], backing: .buffered, defer: false)
        window.title = "Water"
        window.isReleasedWhenClosed = false
        window.center()

        let label = NSTextField(labelWithString: "Getting ready…")
        label.frame = NSRect(x: 20, y: 62, width: 340, height: 20)
        let bar = NSProgressIndicator(frame: NSRect(x: 20, y: 34, width: 340, height: 16))
        bar.style = .bar
        bar.minValue = 0
        bar.maxValue = 1
        bar.isIndeterminate = true
        bar.startAnimation(nil)

        let content = NSView(frame: NSRect(x: 0, y: 0, width: 380, height: 110))
        content.addSubview(label)
        content.addSubview(bar)
        window.contentView = content

        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)

        @MainActor func show(_ text: String, fraction: Double?) {
            label.stringValue = text
            if let fraction {
                bar.isIndeterminate = false
                bar.doubleValue = min(1, max(0, fraction))
            } else {
                bar.isIndeterminate = true
                bar.startAnimation(nil)
            }
        }
        /// A FluidAudio progress callback (any queue) for one of the steps.
        func progress(_ step: String) -> ProgressHandler {
            return { p in
                let text: String
                let fraction: Double?
                switch p.phase {
                case .listing:
                    text = "\(step): checking files…"
                    fraction = nil
                case .downloading(let done, let total):
                    text = total > 0 ? "\(step): downloading (\(done) of \(total) files)…" : "\(step): downloading…"
                    fraction = p.fractionCompleted
                case .compiling:
                    text = "\(step): compiling…"
                    fraction = p.fractionCompleted
                }
                DispatchQueue.main.async { MainActor.assumeIsolated { show(text, fraction: fraction) } }
            }
        }

        Task {
            do {
                try FileManager.default.createDirectory(at: SpeechModels.root, withIntermediateDirectories: true)
                let listen = "Step 1 of 3, speech recognition (Parakeet)"
                try await AsrModels.download(to: SpeechModels.parakeetDirectory, progressHandler: progress(listen))
                let voice = "Step 2 of 3, voice (Kokoro)"
                try await KokoroAneResourceDownloader.ensureModels(directory: SpeechModels.kokoroDirectory,
                                                                    progressHandler: progress(voice))
                await MainActor.run { show("Step 3 of 3, loading the models…", fraction: nil) }
                try await kokoroManager.initialize()
                try await parakeet.load()
                guard SpeechModels.allOnDisk() else { throw CaptureError("speech models incomplete after download") }
                await MainActor.run {
                    show("Ready.", fraction: 1)
                    // Briefly visible rather than an instant close, so
                    // "Ready." is an actual observed phase.
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) {
                        window.close()
                        completion(true)
                    }
                }
            } catch {
                // Never crash, never retry in a loop: one failed attempt
                // just closes the window and reports failure — `resolve`
                // above turns that into "use Apple Speech for this run."
                print("water: speech model download failed: \(error)")
                await MainActor.run {
                    window.close()
                    completion(false)
                }
            }
        }
    }
}
