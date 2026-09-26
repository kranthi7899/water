import Foundation
import WaterClientCore

/// `Water --voice-bench [--n <count>]`: runs `count` (default 30) synthetic
/// voice turns headlessly — no NSApplication, no mic, no human — matching
/// `--selftest`'s early-exit convention in main.swift, and reports
/// aggregate per-checkpoint-interval latency stats
/// (`VoiceLatencyStats`/`VoiceTurnAggregator`, WaterClientCore).
///
/// What's real vs. synthetic, honestly, since there's no microphone or
/// human in this mode:
/// - **Synthetic**: "key-up" and "STT final" — there is nothing to
///   recognize, so both are stamped back-to-back at the top of each
///   iteration, standing in for "a hold-to-talk capture already produced
///   this canned text." Their measured interval is ~0ms by construction —
///   not a real capture-latency number, and the aggregate report should be
///   read accordingly.
/// - **Real**: everything from "request sent" on. Each iteration makes a
///   real `POST /v1/turns` over the real Unix socket (`UnixSocketClient`,
///   the same client the app uses) and hands the first reply sentence to a
///   real `AppleSpeechOutput` through a real `SentenceSpeechQueue` for the
///   "first audio" checkpoint — deliberately `AppleSpeechOutput`, not
///   Kokoro/FluidAudio, so `--voice-bench` needs no multi-hundred-MB model
///   download and stays runnable anywhere (a fresh machine, a sandboxed CI
///   run) with nothing to consent to first. Audio may not actually be
///   audible in a headless run with no spinning main run loop (nothing
///   here calls `RunLoop.main.run()`/`dispatchMain()`) — irrelevant to what
///   this measures: "first audio" is hand-off to the engine's `play` call
///   (see VoiceTrace.swift's doc comment on `Checkpoint.firstAudio`), which
///   happens synchronously within that call, before anything needs a run
///   loop to continue.
enum VoiceBench {
    static let defaultN = 30

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
        var args = Array(arguments.dropFirst().filter { $0 != "--voice-bench" })
        var n = defaultN
        if let i = args.firstIndex(of: "--n"), i + 1 < args.count {
            guard let v = Int(args[i + 1]), v > 0 else {
                print("voice-bench: --n needs a positive integer, got \(args[i + 1].debugDescription)")
                return 2
            }
            n = v
            args.removeSubrange(i...(i + 1))
        }

        let client = UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath())
        print("voice-bench: n=\(n) socket=\(client.socketPath)")
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

        let speech = AppleSpeechOutput()
        let queue = SentenceSpeechQueue(output: speech)
        var traces: [VoiceTurnTrace] = []

        for i in 0..<n {
            let prompt = cannedPrompts[i % cannedPrompts.count]
            var trace = VoiceTurnTrace()
            trace.mark(.keyUp)
            trace.mark(.sttFinal) // synthetic — see the type doc comment
            queue.onWillPlay = { trace.mark(.firstAudio) }

            trace.mark(.requestSent)
            var sawDone = false
            do {
                try client.streamTurn(channel: .voice, prompt: prompt, token: token) { e in
                    switch e.kind {
                    case .ack: trace.mark(.ack)
                    case .sentence:
                        trace.mark(.firstSentence)
                        queue.enqueue(e.text ?? "")
                    case .done:
                        trace.mark(.done)
                        sawDone = true
                    default: break
                    }
                }
            } catch {
                print("voice-bench: turn \(i) failed: \(error.localizedDescription)")
                queue.stop()
                continue
            }
            // Resets the queue for the next iteration regardless of whether
            // this run loop ever pumps long enough for the engine's `done`
            // callback to arrive (see the type doc comment on why that
            // callback may never fire headlessly) — without this, a queue
            // still "playing" from this turn would just accumulate every
            // later turn's sentences as pending and never play any of them.
            queue.stop()
            print(trace.logLine(turnID: "bench-\(i)"))
            if sawDone { traces.append(trace) }
        }

        print("voice-bench: \(traces.count)/\(n) turns completed")
        guard !traces.isEmpty else { return 1 }
        for (label, stats) in VoiceTurnAggregator.aggregate(traces) {
            print(String(format: "  %@: n=%d min=%.0fms median=%.0fms p90=%.0fms max=%.0fms",
                         label, stats.count, stats.minMs, stats.medianMs, stats.p90Ms, stats.maxMs))
        }
        return 0
    }
}
