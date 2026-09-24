import AVFoundation
import Foundation
import Speech
import WaterClientCore

/// Push-to-talk plus spoken replies. The state machine itself is
/// WaterClientCore's VoiceSession (tested there); this wires it to the real
/// permission prompts, microphone, recognizer and main-queue timer, and owns
/// the speech synthesizer. Recognition is on-device only
/// (`requiresOnDeviceRecognition`): if this Mac can't recognize on-device,
/// voice refuses to run rather than send audio to Apple's servers.
final class VoiceController {
    typealias State = VoiceSession.State

    private let session: VoiceSession
    private let synth = AVSpeechSynthesizer()

    var state: State { session.state }

    var onListening: (() -> Void)? {
        get { session.onListening } set { session.onListening = newValue }
    }
    var onPartial: ((String) -> Void)? {
        get { session.onPartial } set { session.onPartial = newValue }
    }
    /// The final transcript (non-empty), ready to send as a voice turn.
    var onTranscript: ((String) -> Void)? {
        get { session.onTranscript } set { session.onTranscript = newValue }
    }
    /// A user-facing failure; the controller is back to idle.
    var onFailure: ((String) -> Void)? {
        get { session.onFailure } set { session.onFailure = newValue }
    }

    init(holdLabel: String) {
        session = VoiceSession(permissions: SystemVoicePermissions(),
                               capture: OnDeviceSpeechCapture(),
                               scheduler: MainQueueScheduler())
        session.releasedEarlyMessage = "Hold \(holdLabel) while you talk, and release it to send."
    }

    /// Menu click: start listening, or stop and send.
    func toggle() {
        if session.state == .idle { stopSpeaking() }
        session.toggle()
    }

    /// Hotkey down. A new capture interrupts any reply being spoken.
    func startHold() {
        if session.state == .idle { stopSpeaking() }
        session.startHold()
    }

    /// Hotkey up.
    func endHold() { session.endHold() }

    func cancel() { session.cancel() }

    /// Speaks one reply sentence. AVSpeechSynthesizer queues utterances, so
    /// sentences play back-to-back in arrival order while more stream in.
    func speak(_ sentence: String) {
        let s = sentence.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return }
        synth.speak(AVSpeechUtterance(string: s))
    }

    /// Stops the current utterance and drops every queued one.
    func stopSpeaking() {
        synth.stopSpeaking(at: .immediate)
    }
}

private final class SystemVoicePermissions: VoicePermissionGate {
    func request(_ done: @escaping (String?) -> Void) {
        SpeechPermissions.request(retry: "press the voice hotkey again", done)
    }
}

private final class MainQueueScheduler: VoiceScheduler {
    func after(_ seconds: TimeInterval, _ work: @escaping () -> Void) {
        DispatchQueue.main.asyncAfter(deadline: .now() + seconds, execute: work)
    }
}

/// The mic feeding one on-device SFSpeech request at a time.
private final class OnDeviceSpeechCapture: SpeechCapture {
    private let mic = MicTap()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var onResult: ((SpeechResult) -> Void)?

    init() {
        // A hold is short: if the input device changes mid-hold, end it with
        // a clear error rather than splice two audio formats into one request.
        mic.onInterrupted = { [weak self] message in self?.onResult?(.error(message)) }
    }

    func start(onResult: @escaping (SpeechResult) -> Void) throws {
        cancel()
        let recognizer: SFSpeechRecognizer
        switch SpeechPermissions.onDeviceRecognizer() {
        case .success(let r): recognizer = r
        case .failure(let e): throw CaptureStartError(e.message)
        }
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.requiresOnDeviceRecognition = true
        req.shouldReportPartialResults = true
        req.taskHint = .dictation
        do {
            try mic.start { buffer in req.append(buffer) }
        } catch {
            throw CaptureStartError(error.localizedDescription)
        }
        request = req
        self.onResult = onResult
        task = recognizer.recognitionTask(with: req) { result, error in
            let r: SpeechResult?
            if let result {
                let t = result.bestTranscription.formattedString
                r = result.isFinal ? .final(t) : .partial(t)
            } else if let error {
                r = .error(error.localizedDescription)
            } else {
                r = nil
            }
            // `onResult` is this capture's own closure (VoiceSession drops it
            // once stale), so a late callback can't reach a later capture.
            if let r { DispatchQueue.main.async { onResult(r) } }
        }
    }

    func endAudio() {
        mic.stop()
        request?.endAudio()
    }

    func cancel() {
        mic.stop()
        task?.cancel()
        task = nil
        request = nil
        onResult = nil
    }
}
