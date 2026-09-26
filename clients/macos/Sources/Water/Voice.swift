import AVFoundation
import Foundation
import Speech
import WaterClientCore

/// Push-to-talk. The state machine itself is WaterClientCore's VoiceSession
/// (tested there); this wires it to the real permission prompts, microphone,
/// recognizer and main-queue timer. Recognition is on-device only
/// (`requiresOnDeviceRecognition`): if this Mac can't recognize on-device,
/// voice refuses to run rather than send audio to Apple's servers.
///
/// Spoken replies live outside this type now: AppDelegate drives a
/// `SentenceSpeechQueue` directly (WaterClientCore) over an
/// `AppleSpeechOutput` (this file's old AVSpeechSynthesizer logic, moved
/// behind `SpeechOutput` — see AppleSpeechOutput.swift). `onNeedsSilence`
/// is this controller's only remaining link to that: it fires wherever this
/// type used to call `stopSpeaking()` itself, so AppDelegate can silence a
/// reply the instant a new hold/toggle starts, before permission and mic
/// start-up (which are asynchronous) get anywhere.
final class VoiceController {
    typealias State = VoiceSession.State

    private let session: VoiceSession

    var state: State { session.state }

    /// Fires synchronously, on the caller's thread (always main here),
    /// whenever a fresh capture is about to start from idle — the same
    /// moments this type used to call its own `stopSpeaking()`.
    var onNeedsSilence: (() -> Void)?

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
    /// Main thread, ~30Hz while the mic is open: its level, 0...1. Both
    /// captures feed it from their `MicTap` (V-hud's blob).
    var onLevel: ((Float) -> Void)? {
        get { (capture as? LevelReportingCapture)?.onLevel }
        set { (capture as? LevelReportingCapture)?.onLevel = newValue }
    }

    private let capture: SpeechCapture

    /// `capture` defaults to Apple's on-device recognizer
    /// (`OnDeviceSpeechCapture`); `EngineSelector` (Sources/Water) passes
    /// `ParakeetCapture()` instead when FluidAudio is the chosen engine.
    /// `VoiceSession` itself is unaware which one it's driving.
    init(holdLabel: String, capture: SpeechCapture = OnDeviceSpeechCapture()) {
        self.capture = capture
        session = VoiceSession(permissions: SystemVoicePermissions(),
                               capture: capture,
                               scheduler: MainQueueScheduler())
        session.releasedEarlyMessage = "Hold \(holdLabel) while you talk, and release it to send."
    }

    /// Menu click: start listening, or stop and send.
    func toggle() {
        if session.state == .idle { onNeedsSilence?() }
        session.toggle()
    }

    /// Hotkey down. A new capture interrupts any reply being spoken.
    func startHold() {
        if session.state == .idle { onNeedsSilence?() }
        session.startHold()
    }

    /// Hotkey up.
    func endHold() { session.endHold() }

    func cancel() { session.cancel() }
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

/// A capture whose mic level can be observed (both of push-to-talk's).
protocol LevelReportingCapture: AnyObject {
    var onLevel: ((Float) -> Void)? { get set }
}

/// The mic feeding one on-device SFSpeech request at a time.
private final class OnDeviceSpeechCapture: SpeechCapture, LevelReportingCapture {
    private let mic = MicTap()

    var onLevel: ((Float) -> Void)? {
        get { mic.onLevel } set { mic.onLevel = newValue }
    }
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
