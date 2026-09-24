import AVFoundation
import Foundation
import Speech

/// The microphone as a stream of in-memory PCM buffers (an AVAudioEngine
/// input tap). Shared by push-to-talk and meeting capture; buffers are handed
/// straight to a recognizer and never stored.
///
/// Each `start()` builds a fresh engine, so a capture never reuses one whose
/// input format went stale when the input device changed between captures.
/// A device change *during* a capture stops the engine (AVAudioEngine does
/// that itself); MicTap notices via AVAudioEngineConfigurationChange and
/// either restarts on the new device (`restartOnDeviceChange`) or reports
/// `onInterrupted` — a capture never goes silent without a word.
final class MicTap {
    private var engine: AVAudioEngine?
    private var observer: NSObjectProtocol?
    private var onBuffer: ((AVAudioPCMBuffer) -> Void)?

    /// Rebuild the tap on the new input device after a mid-capture device
    /// change, instead of stopping.
    var restartOnDeviceChange = false
    /// Main thread: capture stopped because the input device changed (and,
    /// with `restartOnDeviceChange`, restarting failed). The message is
    /// user-facing.
    var onInterrupted: ((String) -> Void)?
    /// Main thread: capture restarted on a new input device.
    var onRestarted: (() -> Void)?

    var isRunning: Bool { engine?.isRunning ?? false }

    /// `onBuffer` runs on the audio thread.
    func start(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        stop()
        let engine = AVAudioEngine()
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            throw CaptureError("No microphone input is available.")
        }
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in onBuffer(buffer) }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            throw CaptureError("Couldn't start the microphone: \(error.localizedDescription)")
        }
        self.engine = engine
        self.onBuffer = onBuffer
        observer = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self, weak engine] _ in
            guard let self, let engine, self.engine === engine else { return }
            self.deviceChanged()
        }
    }

    func stop() {
        if let observer { NotificationCenter.default.removeObserver(observer) }
        observer = nil
        if let engine {
            if engine.isRunning { engine.stop() }
            engine.inputNode.removeTap(onBus: 0)
        }
        engine = nil
        onBuffer = nil
    }

    private func deviceChanged() {
        let onBuffer = self.onBuffer
        let changed = "The audio input device changed"
        guard restartOnDeviceChange, let onBuffer else {
            stop()
            onInterrupted?("\(changed), so Water stopped listening.")
            return
        }
        do {
            try start(onBuffer)
            onRestarted?()
        } catch {
            stop()
            onInterrupted?("\(changed) and the microphone couldn't restart: \(error.localizedDescription)")
        }
    }
}

struct CaptureError: Error, LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

enum SpeechPermissions {
    /// Asks for Speech Recognition, then Microphone, each only if the user
    /// hasn't decided yet. Calls back on the main thread with nil when both
    /// are granted, else a message saying exactly where to fix it, ending
    /// with `retry` (e.g. "press the voice hotkey again").
    static func request(retry: String, _ done: @escaping (String?) -> Void) {
        let main: (String?) -> Void = { m in DispatchQueue.main.async { done(m) } }
        let speechDenied = "Speech Recognition access is off for Water. Turn it on in System Settings > Privacy & Security > Speech Recognition, then \(retry)."
        let micDenied = "Microphone access is off for Water. Turn it on in System Settings > Privacy & Security > Microphone, then \(retry)."

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

    /// An on-device English recognizer, or the reason there isn't one. Water
    /// never falls back to server recognition.
    static func onDeviceRecognizer() -> Result<SFSpeechRecognizer, CaptureError> {
        guard let r = SFSpeechRecognizer(locale: Locale(identifier: "en-US")), r.isAvailable else {
            return .failure(CaptureError("Speech recognition isn't available right now."))
        }
        guard r.supportsOnDeviceRecognition else {
            return .failure(CaptureError("On-device speech recognition isn't available on this Mac for English, and Water never sends audio off-device. Turn on Dictation in System Settings > Keyboard (let the English model download), then try again."))
        }
        return .success(r)
    }
}
