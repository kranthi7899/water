import Foundation

// Spoken replies, kept free of AVFoundation/FluidAudio so the ordering,
// overlap and barge-in logic is testable (SentenceSpeechQueueTests uses a
// fake SpeechOutput). Mirrors how HoldToTalk.swift keeps VoiceSession pure
// and lets Voice.swift supply the real recognizer; here Sources/Water
// supplies the real synthesizer(s) behind SpeechOutput.

/// A token returned by `SpeechOutput.prepare`, opaque to everything except
/// the conformer that produced it. `Any` rather than an associated type so
/// `SentenceSpeechQueue` and its tests can hold a `SpeechOutput` value
/// without becoming generic over the engine.
public typealias SpeechToken = Any

/// One text-to-speech engine, with synthesis and playback deliberately
/// split into two calls so `SentenceSpeechQueue` can start synthesizing a
/// sentence while an earlier one is still playing — no audio gap between
/// sentences — without ever asking an engine to play two sentences at
/// once. `AppleSpeechOutput` (Sources/Water) is the first conformer, and
/// wraps `AVSpeechSynthesizer`, which only exposes "synthesize and play" as
/// one call — its `prepare` does nothing but hand the text back as the
/// token, deferring all the work to `play`. A future engine that can
/// synthesize audio ahead of time (e.g. Kokoro) does that real work in
/// `prepare` instead.
///
/// Every call here happens on the main thread, and `SentenceSpeechQueue`
/// only ever has one `play` in flight at a time — a conformer never needs
/// to mix two sentences' audio.
public protocol SpeechOutput: AnyObject {
    /// Synthesizes `text`, calling `ready` on the main thread exactly once:
    /// with a token to hand to `play` once it's this sentence's turn, or
    /// with `nil` if synthesis failed — the queue then just drops that
    /// sentence and moves on, rather than getting stuck on it.
    func prepare(_ text: String, ready: @escaping (SpeechToken?) -> Void)
    /// Plays a token from a previous `prepare` call. Calls `done` on the
    /// main thread exactly once, when this sentence has finished playing,
    /// so the queue can hand off to whatever it already has prepared next.
    func play(_ token: SpeechToken, done: @escaping () -> Void)
    /// Stops whatever is playing right now, immediately. Safe to call even
    /// when nothing is playing.
    func stopCurrent()
}

/// Feeds reply sentences to a `SpeechOutput` in arrival order, keeping
/// exactly one sentence of lookahead synthesizing ahead of whatever is
/// currently playing so there's no gap at the handoff, and supports
/// barge-in: `stop()` cuts the sentence playing right now and drops every
/// sentence queued or mid-synthesis, immediately and for good.
///
/// Generation-counter guarded, the same idiom `VoiceSession` uses for stale
/// callbacks (HoldToTalk.swift): `stop()` bumps the generation, and every
/// `prepare`/`play` completion carries the generation it was issued under,
/// so a synthesis or playback callback that lands after a barge-in is
/// simply dropped rather than starting or ending anything.
public final class SentenceSpeechQueue {
    private let output: SpeechOutput
    private var generation = 0

    /// Sentences appended but not yet even asked to prepare.
    private var pending: [String] = []
    /// True while `prepare` is in flight for the sentence that will become
    /// current the moment it's ready (nothing has played yet this run).
    private var preparingCurrent = false
    /// True from the moment `play` is called for the current sentence
    /// until its `done` fires.
    private var isPlaying = false
    /// True while `prepare` is in flight for the lookahead sentence — the
    /// one right after whatever is currently playing.
    private var preparingNext = false
    /// Set once the lookahead sentence's `prepare` has returned a token
    /// while the current sentence is still playing.
    private var nextToken: SpeechToken?
    /// True once the current sentence has finished but the lookahead
    /// sentence's `prepare` hasn't returned yet, so `play` runs on it the
    /// moment it does, instead of sitting in `nextToken` first (a real
    /// gap, when synthesis is slower than playback — unavoidable, not a
    /// bug).
    private var playNextAsSoonAsReady = false

    /// Fired on the main thread the instant a token is handed to
    /// `output.play` — i.e. hand-off to whatever engine is behind
    /// `SpeechOutput`, not that engine's own internal "audio actually
    /// started" signal (see VoiceTrace.swift's doc comment on
    /// `Checkpoint.firstAudio` for why hand-off is the measured point).
    /// Fires once per sentence played, not just the first; latency
    /// instrumentation (V-8) marks its own "first" checkpoint idempotently.
    /// Nil by default — no behavior or cost when unset.
    public var onWillPlay: (() -> Void)?

    public init(output: SpeechOutput) {
        self.output = output
    }

    /// Appends one reply sentence. Blank (whitespace-only) sentences are
    /// dropped, matching the old `VoiceController.speak`'s guard.
    public func enqueue(_ sentence: String) {
        let s = sentence.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return }
        pending.append(s)
        pump()
    }

    /// Barge-in: stops whatever is playing right now and drops everything
    /// queued or mid-synthesis. Any `prepare`/`play` completion from before
    /// this call is ignored when it lands (stale generation), so it's safe
    /// even if one is already in flight.
    public func stop() {
        generation += 1
        pending.removeAll()
        preparingCurrent = false
        isPlaying = false
        preparingNext = false
        nextToken = nil
        playNextAsSoonAsReady = false
        output.stopCurrent()
    }

    // MARK: -

    /// Keeps the pipeline full: starts the very first sentence if nothing
    /// is playing or about to be, and keeps exactly one sentence prepared
    /// (or preparing) ahead of whatever is currently playing. Safe to call
    /// any time — a no-op unless there's new work to start.
    private func pump() {
        if !preparingCurrent, !isPlaying, !playNextAsSoonAsReady, nextToken == nil, !preparingNext,
           !pending.isEmpty {
            startCurrent(pending.removeFirst())
        }
        if isPlaying, nextToken == nil, !preparingNext, !pending.isEmpty {
            startPreparingNext(pending.removeFirst())
        }
    }

    private func startCurrent(_ sentence: String) {
        preparingCurrent = true
        let gen = generation
        output.prepare(sentence) { [weak self] token in
            guard let self, self.generation == gen else { return }
            self.preparingCurrent = false
            if let token {
                self.isPlaying = true
                self.playToken(token, gen: gen)
                self.pump() // start the lookahead now that something's playing
            } else if !self.pending.isEmpty {
                self.startCurrent(self.pending.removeFirst())
            }
            // else: nothing to play and nothing pending — stays idle.
        }
    }

    private func startPreparingNext(_ sentence: String) {
        preparingNext = true
        let gen = generation
        output.prepare(sentence) { [weak self] token in
            guard let self, self.generation == gen else { return }
            self.preparingNext = false
            if let token {
                if self.playNextAsSoonAsReady {
                    self.playNextAsSoonAsReady = false
                    self.isPlaying = true
                    self.playToken(token, gen: gen)
                    self.pump()
                } else {
                    self.nextToken = token
                }
            } else if !self.pending.isEmpty {
                // This lookahead sentence failed to synthesize: try the one
                // right after it for the same slot, without disturbing
                // whatever is (or isn't) currently playing.
                self.startPreparingNext(self.pending.removeFirst())
            } else {
                self.playNextAsSoonAsReady = false
            }
        }
    }

    private func currentDidFinish() {
        isPlaying = false
        if let token = nextToken {
            nextToken = nil
            isPlaying = true
            playToken(token, gen: generation)
        } else if preparingNext {
            playNextAsSoonAsReady = true
        }
        pump()
    }

    private func playToken(_ token: SpeechToken, gen: Int) {
        onWillPlay?()
        output.play(token) { [weak self] in
            guard let self, self.generation == gen else { return }
            self.currentDidFinish()
        }
    }
}
