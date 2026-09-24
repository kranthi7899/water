import AVFoundation
import Foundation
import Speech

/// The microphone as a stream of in-memory PCM buffers (an AVAudioEngine
/// input tap). Shared by push-to-talk and meeting capture; buffers are handed
/// straight to a recognizer and never stored.
final class MicTap {
    private let engine = AVAudioEngine()

    var isRunning: Bool { engine.isRunning }

    /// `onBuffer` runs on the audio thread.
    func start(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            throw CaptureError("No microphone input is available.")
        }
        input.removeTap(onBus: 0)
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in onBuffer(buffer) }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            throw CaptureError("Couldn't start the microphone: \(error.localizedDescription)")
        }
    }

    func stop() {
        if engine.isRunning { engine.stop() }
        engine.inputNode.removeTap(onBus: 0)
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
