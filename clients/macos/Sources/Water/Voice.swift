import AVFoundation
import Foundation
import Speech

/// Push-to-talk: hold the hotkey to record, release to send — `startHold`/
/// `endHold` are the hold-gesture entry points the hotkey uses; `toggle`
/// stays available for a plain click (e.g. the menu item), where there's no
/// "hold" to track. Recognition is on-device only
/// (`requiresOnDeviceRecognition`): if this Mac can't recognize on-device,
/// voice refuses to run rather than send audio to Apple's servers.
final class VoiceController {
    enum State { case idle, listening, finishing }
    private(set) var state: State = .idle

    private let mic = MicTap()
    private var recognizer: SFSpeechRecognizer?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var transcript = ""
    private var delivered = false
    private let synth = AVSpeechSynthesizer()

    var onListening: (() -> Void)?
    var onPartial: ((String) -> Void)?
    /// The final transcript (non-empty), ready to send as a voice turn.
    var onTranscript: ((String) -> Void)?
    /// A user-facing failure; the controller is back to idle.
    var onFailure: ((String) -> Void)?

    func toggle() {
        switch state {
        case .idle: begin()
        case .listening: finish()
        case .finishing: break
        }
    }

    /// Hold gesture: key went down. No-op if a capture is already in
    /// progress (e.g. a stray repeat) — only .idle actually starts one.
    func startHold() {
        guard state == .idle else { return }
        begin()
    }

    /// Hold gesture: key went up. No-op unless we're actually listening —
    /// guards a release with no matching press (e.g. focus changed
    /// mid-hold) from tearing down a capture that never started.
    func endHold() {
        guard state == .listening else { return }
        finish()
    }

    /// Speaks one reply sentence. AVSpeechSynthesizer queues utterances, so
    /// sentences play back-to-back in arrival order while more stream in.
    func speak(_ sentence: String) {
        let s = sentence.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return }
        synth.speak(AVSpeechUtterance(string: s))
    }

    func stopSpeaking() {
        synth.stopSpeaking(at: .immediate)
    }

    // MARK: permissions

    private func begin() {
        stopSpeaking()
        state = .finishing // guard against a double press while prompts are up
        SpeechPermissions.request(retry: "press the voice hotkey again") { [weak self] problem in
            guard let self else { return }
            if let problem {
                self.state = .idle
                self.onFailure?(problem)
                return
            }
            self.startCapture()
        }
    }

    // MARK: capture

    private func startCapture() {
        let recognizer: SFSpeechRecognizer
        switch SpeechPermissions.onDeviceRecognizer() {
        case .success(let r): recognizer = r
        case .failure(let e): return fail(e.message)
        }
        self.recognizer = recognizer
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.requiresOnDeviceRecognition = true
        req.shouldReportPartialResults = true
        req.taskHint = .dictation

        do {
            try mic.start { buffer in req.append(buffer) }
        } catch {
            return fail(error.localizedDescription)
        }

        request = req
        transcript = ""
        delivered = false
        state = .listening
        task = recognizer.recognitionTask(with: req) { [weak self] result, error in
            DispatchQueue.main.async {
                guard let self else { return }
                if let result {
                    self.transcript = result.bestTranscription.formattedString
                    if !self.delivered { self.onPartial?(self.transcript) }
                    if result.isFinal { self.deliver() }
                } else if error != nil, self.state == .finishing {
                    self.deliver()
                } else if let error, self.state == .listening {
                    self.teardown()
                    self.fail("Speech recognition stopped: \(error.localizedDescription)")
                }
            }
        }
        onListening?()
    }

    private func finish() {
        state = .finishing
        teardownAudio()
        request?.endAudio()
        // endAudio() only has already-buffered audio left to decode, so
        // isFinal normally arrives in well under a second — this is a worst
        // case backstop, not the typical path. Most of the old toggle-mode
        // latency was never recognition speed; it was the user having to
        // decide to press the hotkey a second time. Holding removes that.
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { [weak self] in self?.deliver() }
    }

    private func deliver() {
        guard !delivered, state == .finishing else { return }
        delivered = true
        let text = transcript.trimmingCharacters(in: .whitespacesAndNewlines)
        teardown()
        if text.isEmpty {
            onFailure?("Didn't catch anything — try again.")
        } else {
            onTranscript?(text)
        }
    }

    private func teardownAudio() {
        mic.stop()
    }

    private func teardown() {
        teardownAudio()
        task?.cancel()
        task = nil
        request = nil
        state = .idle
    }

    private func fail(_ message: String) {
        state = .idle
        onFailure?(message)
    }
}
