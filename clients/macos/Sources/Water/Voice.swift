import AVFoundation
import Foundation
import Speech

/// Push-to-talk: first press starts listening, second press stops and hands
/// the transcript over. Recognition is on-device only
/// (`requiresOnDeviceRecognition`): if this Mac can't recognize on-device,
/// voice refuses to run rather than send audio to Apple's servers.
final class VoiceController {
    enum State { case idle, listening, finishing }
    private(set) var state: State = .idle

    private let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US"))
    private let engine = AVAudioEngine()
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
        requestPermissions { [weak self] problem in
            guard let self else { return }
            if let problem {
                self.state = .idle
                self.onFailure?(problem)
                return
            }
            self.startCapture()
        }
    }

    /// Asks for Speech Recognition, then Microphone, each only if the user
    /// hasn't decided yet. Calls back on the main thread with nil when both
    /// are granted, else a message saying exactly where to fix it.
    private func requestPermissions(_ done: @escaping (String?) -> Void) {
        let main: (String?) -> Void = { m in DispatchQueue.main.async { done(m) } }
        let speechDenied = "Speech Recognition access is off for Water. Turn it on in System Settings > Privacy & Security > Speech Recognition, then press the voice hotkey again."
        let micDenied = "Microphone access is off for Water. Turn it on in System Settings > Privacy & Security > Microphone, then press the voice hotkey again."

        let afterSpeech: () -> Void = {
            switch AVCaptureDevice.authorizationStatus(for: .audio) {
            case .authorized: main(nil)
            case .notDetermined:
                AVCaptureDevice.requestAccess(for: .audio) { ok in main(ok ? nil : micDenied) }
            default: main(micDenied)
            }
        }
        switch SFSpeechRecognizer.authorizationStatus() {
        case .authorized: afterSpeech()
        case .notDetermined:
            SFSpeechRecognizer.requestAuthorization { st in
                if st == .authorized { afterSpeech() } else { main(speechDenied) }
            }
        default: main(speechDenied)
        }
    }

    // MARK: capture

    private func startCapture() {
        guard let recognizer, recognizer.isAvailable else {
            return fail("Speech recognition isn't available right now.")
        }
        guard recognizer.supportsOnDeviceRecognition else {
            return fail("On-device speech recognition isn't available on this Mac for English, and Water never sends audio off-device. Turn on Dictation in System Settings > Keyboard (let the English model download), then try again.")
        }
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.requiresOnDeviceRecognition = true
        req.shouldReportPartialResults = true
        req.taskHint = .dictation

        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            return fail("No microphone input is available.")
        }
        input.removeTap(onBus: 0)
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            req.append(buffer)
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            return fail("Couldn't start the microphone: \(error.localizedDescription)")
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
        // The final result usually lands within a moment; don't hang on it.
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in self?.deliver() }
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
        if engine.isRunning { engine.stop() }
        engine.inputNode.removeTap(onBus: 0)
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
