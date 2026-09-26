import Foundation
import WaterClientCore

/// `Water --voice-bench [--n <count>] [--tts apple|kokoro]`: runs `count`
/// (default 30) synthetic voice turns headlessly — no NSApplication, no
/// mic, no human — matching `--selftest`'s early-exit convention in
/// main.swift, and reports aggregate per-checkpoint-interval latency stats
/// (`VoiceLatencyStats`/`VoiceTurnAggregator`, WaterClientCore).
///
/// What's real vs. synthetic, honestly, since there's no microphone or
/// human in this mode:
/// - **Synthetic**: "key-up" and "STT final" — there is nothing to
///   recognize, so both are stamped back-to-back at the top of each
///   iteration, standing in for "a hold-to-talk capture already produced
///   this canned text." Their measured interval is ~0ms by construction —
///   not a real capture-latency number.
/// - **Real**: everything from "request sent" on. Each iteration makes a
///   real `POST /v1/turns` over the real Unix socket (`UnixSocketClient`,
///   the same client the app uses) and hands reply sentences to a real
///   engine through a real `SentenceSpeechQueue` for the "first audio"
///   checkpoint (hand-off to the engine's `play`; see VoiceTrace.swift).
///   - `--tts apple` (default): `AppleSpeechOutput`. Nothing to download,
///     runs anywhere.
///   - `--tts kokoro` (V-hud): `KokoroSpeaker`, so first audio includes
///     Kokoro's synthesis of the first sentence — the number that matters
///     for the Kokoro engine. It needs the models already in
///     `SpeechModels.root` (accept the app's download dialog once); the
///     bench never downloads. The model load and one warm-up synthesis run
///     before the first turn and are reported separately, not counted in
///     any turn.
///
/// The turn streams on a background thread while the main run loop is
/// pumped, because Kokoro's synthesis completes on the main actor; events
/// are handled on main, as in the app.
enum VoiceBench {
    static let defaultN = VoiceBenchOptions.defaultN
    /// How long to wait for first audio after `done`, and for a turn.
    static let firstAudioTimeout: TimeInterval = 30
    static let turnTimeout: TimeInterval = 300

    /// Cycled through so a run longer than the prompt list doesn't repeat
    /// the exact same text back-to-back; content doesn't matter for
    /// latency, only that each is a real turn the daemon has to answer.
    static let cannedPrompts = [
        "what's on my schedule today?",
        "summarize my unread email",
        "any approvals waiting on me?",
        "what's the status of the current project?",
        "read me today's top decision",
        "who's working on what this week?",
    ]

    static func run(arguments: [String]) -> Int32 {
        let opts: VoiceBenchOptions
        switch VoiceBenchOptions.parse(arguments) {
        case .success(let o): opts = o
        case .failure(let e):
            print("voice-bench: \(e.message)")
            return 2
        }

        let client = UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath())
        print("voice-bench: n=\(opts.n) tts=\(opts.tts.rawValue) socket=\(client.socketPath)")
        do {
            _ = try client.health()
        } catch {
            print("voice-bench: health failed: \(error.localizedDescription)")
            return 1
        }

        let clientsPath = ClientsFile.defaultPath()
        guard let token = ClientsFile.token(named: "macos-client", path: clientsPath)
                ?? ClientsFile.token(named: "cli", path: clientsPath) else {
            print("voice-bench: no token in \(clientsPath)")
            return 1
        }

        let speech: SpeechOutput
        switch opts.tts {
        case .apple:
            speech = AppleSpeechOutput()
        case .kokoro:
            guard EngineSelector.isAppleSilicon else {
                print("voice-bench: Kokoro needs Apple silicon")
                return 1
            }
            guard SpeechModels.allOnDisk() else {
                print("voice-bench: the speech models aren't in \(SpeechModels.root.path). Launch Water.app and accept the download first; the bench never downloads.")
                return 1
            }
            let kokoro = KokoroSpeaker(manager: SpeechModels.makeKokoroManager())
            let t0 = Date()
            var warmed: Bool?
            kokoro.prepare("Ready.") { warmed = $0 != nil }
            guard pump(until: { warmed != nil }, timeout: 600), warmed == true else {
                print("voice-bench: Kokoro failed to load or synthesize")
                return 1
            }
            print(String(format: "voice-bench: kokoro load + warm-up synthesis %.0fms (not counted)",
                         Date().timeIntervalSince(t0) * 1000))
            speech = kokoro
        }
        let queue = SentenceSpeechQueue(output: speech)
        var traces: [VoiceTurnTrace] = []

        for i in 0..<opts.n {
            let prompt = cannedPrompts[i % cannedPrompts.count]
            var trace = VoiceTurnTrace()
            trace.mark(.keyUp)
            trace.mark(.sttFinal) // synthetic — see the type doc comment
            var firstAudio = false
            queue.onWillPlay = {
                trace.mark(.firstAudio)
                firstAudio = true
            }

            var sawDone = false
            var sawSentence = false
            var finished = false
            var failure: Error?
            trace.mark(.requestSent)
            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    try client.streamTurn(channel: .voice, prompt: prompt, token: token) { e in
                        DispatchQueue.main.async {
                            switch e.kind {
                            case .ack: trace.mark(.ack)
                            case .sentence:
                                trace.mark(.firstSentence)
                                sawSentence = true
                                queue.enqueue(e.text ?? "")
                            case .done:
                                trace.mark(.done)
                                sawDone = true
                            default: break
                            }
                        }
                    }
                    DispatchQueue.main.async { finished = true }
                } catch {
                    DispatchQueue.main.async {
                        failure = error
                        finished = true
                    }
                }
            }
            _ = pump(until: { finished }, timeout: turnTimeout)
            if sawSentence, failure == nil {
                _ = pump(until: { firstAudio }, timeout: firstAudioTimeout)
            }
            // Resets the queue for the next iteration: without this, a
            // queue still "playing" from this turn would just accumulate
            // every later turn's sentences as pending.
            queue.stop()
            if let failure {
                print("voice-bench: turn \(i) failed: \(failure.localizedDescription)")
                continue
            }
            print(trace.logLine(turnID: "bench-\(i)"))
            if sawDone { traces.append(trace) }
        }

        print("voice-bench: \(traces.count)/\(opts.n) turns completed")
        guard !traces.isEmpty else { return 1 }
        for (label, stats) in VoiceTurnAggregator.aggregate(traces) {
            print(String(format: "  %@: n=%d min=%.0fms median=%.0fms p90=%.0fms max=%.0fms",
                         label, stats.count, stats.minMs, stats.medianMs, stats.p90Ms, stats.maxMs))
        }
        return 0
    }

    /// Runs the main run loop (which also drains the main dispatch queue
    /// and main-actor work) until `done()` or the timeout. True if done.
    private static func pump(until done: () -> Bool, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while !done() {
            if Date() >= deadline { return false }
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.01))
        }
        return true
    }
}
