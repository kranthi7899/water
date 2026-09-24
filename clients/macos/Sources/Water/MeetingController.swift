import AVFoundation
import Foundation
import Speech
import WaterClientCore

/// Live-meeting capture, started and stopped by hand only (the meeting
/// hotkey or the menu). While a session is active, two independent on-device
/// recognizers run: the microphone (tagged `mic`, the CEO) and the Mac's
/// audio output via a Core Audio process tap (tagged `system`, everyone
/// else). Each final recognition result is posted to the daemon as a
/// timestamped text segment. Audio stays in memory on this Mac: it never
/// reaches the daemon and is never written to disk.
///
/// The daemon treats every segment, from either channel, as untrusted
/// transcript — never as an instruction. Asking Water something during a
/// meeting is still a deliberate turn (the text bar or the voice hotkey).
final class MeetingController {
    enum State { case idle, starting, active, stopping }
    private(set) var state: State = .idle { didSet { if state != oldValue { onStateChange?(state) } } }
    private(set) var session: MeetingSession?

    private let daemon: MeetingDaemon
    private let mic = MicTap()
    private var systemTap: AnyObject?
    private var streams: [MeetingChannel: RecognitionStream] = [:]

    var onStateChange: ((State) -> Void)?
    /// Something worth telling the user that doesn't end the session.
    var onNote: ((String) -> Void)?
    /// The session couldn't start or had to end; the controller is idle.
    var onFailure: ((String) -> Void)?

    init(daemon: MeetingDaemon) {
        self.daemon = daemon
    }

    func toggle() {
        switch state {
        case .idle: begin()
        case .active: end()
        case .starting, .stopping: break
        }
    }

    // MARK: start

    private func begin() {
        state = .starting
        SpeechPermissions.request(retry: "press the meeting hotkey again") { [weak self] problem in
            guard let self else { return }
            if let problem { return self.fail(problem) }
            // Two independent recognizers, one per channel.
            var recognizers: [SFSpeechRecognizer] = []
            for _ in 0..<2 {
                switch SpeechPermissions.onDeviceRecognizer() {
                case .success(let r): recognizers.append(r)
                case .failure(let e): return self.fail(e.message)
                }
            }
            self.daemon.start { result, note in
                if let note { self.onNote?(note) }
                switch result {
                case .success(let s):
                    self.session = s
                    self.startCapture(mic: recognizers[0], system: recognizers[1])
                case .failure(let e):
                    self.fail("Couldn't start a meeting session: \(e.localizedDescription)")
                }
            }
        }
    }

    private func startCapture(mic micRecognizer: SFSpeechRecognizer, system systemRecognizer: SFSpeechRecognizer) {
        guard let session else { return }
        var problems: [String] = []

        let micStream = makeStream(.mic, micRecognizer, session: session)
        do {
            micStream.start()
            try mic.start { micStream.append($0) }
            streams[.mic] = micStream
        } catch {
            micStream.cancel()
            problems.append("Microphone: \(error.localizedDescription)")
        }

        if #available(macOS 14.2, *) {
            let systemStream = makeStream(.system, systemRecognizer, session: session)
            let tap = SystemAudioTap()
            do {
                systemStream.start()
                try tap.start { systemStream.append($0) }
                systemTap = tap
                streams[.system] = systemStream
            } catch {
                systemStream.cancel()
                problems.append("System audio: \(error.localizedDescription)")
            }
        } else {
            problems.append("System audio capture needs macOS 14.2 or later.")
        }

        if streams.isEmpty {
            daemon.stop(sessionID: session.id) { _ in }
            self.session = nil
            return fail("Couldn't capture any audio for the meeting. " + problems.joined(separator: " "))
        }
        state = .active
        if !problems.isEmpty {
            onNote?("Meeting capture is running with only part of the audio. " + problems.joined(separator: " "))
        }
    }

    private func makeStream(_ channel: MeetingChannel, _ recognizer: SFSpeechRecognizer, session: MeetingSession) -> RecognitionStream {
        let s = RecognitionStream(recognizer: recognizer)
        s.onFinal = { [weak self] text, at in
            guard let self, self.session?.id == session.id else { return }
            for piece in MeetingSegments.pieces(text) {
                self.daemon.post(sessionID: session.id, channel: channel, text: piece, at: at) { [weak self] err in
                    self?.segmentFailed(err, sessionID: session.id)
                }
            }
        }
        s.onError = { [weak self] message in
            self?.onNote?("\(channel == .mic ? "Microphone" : "System audio") transcription stopped: \(message)")
        }
        return s
    }

    private var reportedPostFailure = false

    private func segmentFailed(_ err: Error?, sessionID: String) {
        guard let err, session?.id == sessionID else { return }
        if case WaterClientError.http(let status, _)? = err as? WaterClientError, status == 404 || status == 409 {
            // The daemon no longer has this session open; stop listening.
            teardownCapture()
            session = nil
            return fail("The daemon ended this meeting session, so Water stopped listening.")
        }
        if !reportedPostFailure {
            reportedPostFailure = true
            onNote?("Couldn't send meeting transcript to the daemon: \(err.localizedDescription)")
        }
    }

    // MARK: stop

    private func end() {
        guard let session else { return teardownAndIdle() }
        state = .stopping
        stopAudio()
        // Let each recognizer deliver its last final result (posted ahead of
        // the stop on the daemon queue), then close the session.
        let waiting = streams.values
        var remaining = waiting.count
        let finish: () -> Void = { [weak self] in
            guard let self else { return }
            self.daemon.stop(sessionID: session.id) { [weak self] err in
                guard let self else { return }
                self.teardownAndIdle()
                if let err { self.onNote?("Couldn't close the meeting session cleanly: \(err.localizedDescription)") }
            }
        }
        if remaining == 0 { return finish() }
        for s in waiting {
            s.onDrained = {
                remaining -= 1
                if remaining == 0 { finish() }
            }
            s.stop()
        }
    }

    /// At quit: stop listening and close the session without waiting for
    /// last results.
    func shutdown() {
        guard let session else { return teardownCapture() }
        teardownCapture()
        self.session = nil
        daemon.stopNow(sessionID: session.id)
    }

    private func stopAudio() {
        mic.stop()
        if #available(macOS 14.2, *) { (systemTap as? SystemAudioTap)?.stop() }
        systemTap = nil
    }

    private func teardownCapture() {
        stopAudio()
        for s in streams.values { s.cancel() }
        streams = [:]
    }

    private func teardownAndIdle() {
        teardownCapture()
        session = nil
        reportedPostFailure = false
        state = .idle
    }

    private func fail(_ message: String) {
        teardownAndIdle()
        onFailure?(message)
    }
}

/// One continuous on-device recognition stream, fed buffers from any audio
/// source. An open-ended stream seldom produces a final result on its own,
/// so after a pause in speech (or a long stretch) the current request is
/// ended and a fresh one takes over; the ended request then delivers its
/// final result, which is what `onFinal` reports. Partials are only used to
/// spot the pause, never reported.
final class RecognitionStream {
    private final class Utterance {
        let request = SFSpeechAudioBufferRecognitionRequest()
        var task: SFSpeechRecognitionTask?
        let openedAt = Date()
        var firstWordsAt: Date?
        var changedAt = Date()
        var text = ""
        var done = false
    }

    static let pause: TimeInterval = 1.5
    static let longest: TimeInterval = 45
    static let finalTimeout: TimeInterval = 3

    private let recognizer: SFSpeechRecognizer
    private let lock = NSLock()
    private var current: Utterance? // read on the audio thread, under lock
    private var ending: [Utterance] = []
    private var timer: Timer?
    private var stopping = false
    private var failures = 0

    /// Main thread: a final transcript and roughly when its speech began.
    var onFinal: ((String, Date) -> Void)?
    var onError: ((String) -> Void)?
    /// Main thread, after `stop()`: every final result is in (or timed out).
    var onDrained: (() -> Void)?

    init(recognizer: SFSpeechRecognizer) {
        self.recognizer = recognizer
    }

    func start() {
        open()
        let t = Timer(timeInterval: 0.5, repeats: true) { [weak self] _ in self?.tick() }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    /// Any thread (the audio callbacks call it).
    func append(_ buffer: AVAudioPCMBuffer) {
        lock.lock()
        let u = current
        lock.unlock()
        u?.request.append(buffer)
    }

    func stop() {
        stopping = true
        timer?.invalidate()
        timer = nil
        endCurrent()
        checkDrained()
    }

    func cancel() {
        stopping = true
        timer?.invalidate()
        timer = nil
        let all = swapCurrent(nil).map { [$0] } ?? []
        for u in all + ending {
            u.done = true
            u.task?.cancel()
        }
        ending = []
        onDrained = nil
    }

    private func open() {
        let u = Utterance()
        u.request.requiresOnDeviceRecognition = true
        u.request.shouldReportPartialResults = true
        u.request.taskHint = .dictation
        u.request.addsPunctuation = true
        u.task = recognizer.recognitionTask(with: u.request) { [weak self, weak u] result, error in
            DispatchQueue.main.async {
                guard let self, let u else { return }
                self.handle(u, result, error)
            }
        }
        _ = swapCurrent(u)
    }

    @discardableResult
    private func swapCurrent(_ u: Utterance?) -> Utterance? {
        lock.lock()
        defer { lock.unlock() }
        let old = current
        current = u
        return old
    }

    private func endCurrent() {
        guard let old = swapCurrent(nil) else { return }
        ending.append(old)
        old.request.endAudio()
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.finalTimeout) { [weak self, weak old] in
            guard let self, let old, !old.done else { return }
            old.task?.cancel()
            self.finish(old, text: nil)
        }
    }

    private func tick() {
        lock.lock()
        let u = current
        lock.unlock()
        guard let u, !stopping else { return }
        let now = Date()
        let paused = !u.text.isEmpty && now.timeIntervalSince(u.changedAt) >= Self.pause
        if paused || now.timeIntervalSince(u.openedAt) >= Self.longest {
            endCurrent()
            open()
        }
    }

    private func handle(_ u: Utterance, _ result: SFSpeechRecognitionResult?, _ error: Error?) {
        guard !u.done else { return }
        if let result {
            failures = 0
            let text = result.bestTranscription.formattedString
            if result.isFinal { return finish(u, text: text) }
            if text != u.text {
                if u.firstWordsAt == nil, !text.isEmpty { u.firstWordsAt = Date() }
                u.text = text
                u.changedAt = Date()
            }
        } else if let error {
            lock.lock()
            let wasCurrent = current === u
            lock.unlock()
            finish(u, text: nil)
            // A live request died on its own (e.g. a long silence): carry on
            // with a fresh one unless it keeps failing.
            guard wasCurrent, !stopping else { return }
            failures += 1
            if failures >= 3 {
                timer?.invalidate()
                timer = nil
                onError?(error.localizedDescription)
            } else {
                open()
            }
        }
    }

    private func finish(_ u: Utterance, text: String?) {
        guard !u.done else { return }
        u.done = true
        u.task = nil
        ending.removeAll { $0 === u }
        lock.lock()
        let wasCurrent = current === u
        if wasCurrent { current = nil }
        lock.unlock()
        if let text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            onFinal?(text, u.firstWordsAt ?? u.openedAt)
        }
        // The recognizer closed the live request by itself: keep listening.
        if wasCurrent, text != nil, !stopping { open() }
        checkDrained()
    }

    private func checkDrained() {
        guard stopping, ending.isEmpty else { return }
        lock.lock()
        let open = current != nil
        lock.unlock()
        guard !open, let done = onDrained else { return }
        onDrained = nil
        DispatchQueue.main.async { done() }
    }
}

/// The meeting endpoints, called in order on one serial queue — so every
/// segment posted before a stop reaches the daemon before the stop does.
/// Callbacks come back on the main thread.
final class MeetingDaemon {
    let client: UnixSocketClient
    let tokens: TokenProvider
    private let queue = DispatchQueue(label: "water.meetings", qos: .userInitiated)
    private var token: String? // touched only on `queue`

    init(client: UnixSocketClient, tokens: TokenProvider) {
        self.client = client
        self.tokens = tokens
    }

    /// Resolves the token like TurnRunner does (falling back to the CLI's
    /// token on a 401) and keeps it for the session's segments and stop.
    func start(_ done: @escaping (Result<MeetingSession, Error>, String?) -> Void) {
        queue.async { [self] in
            var note: String?
            let result = Result<MeetingSession, Error> {
                let tok = try tokens.token()
                do {
                    let s = try client.startMeeting(token: tok)
                    token = tok
                    return s
                } catch WaterClientError.http(status: 401, body: _) {
                    guard let cli = tokens.cliFallbackToken(), cli != tok else { throw WaterClientError.http(status: 401, body: "invalid token") }
                    note = "The daemon doesn't know this app's token yet (it loads tokens at startup) — using the CLI token. Restart `water daemon` to fix."
                    let s = try client.startMeeting(token: cli)
                    token = cli
                    return s
                }
            }
            DispatchQueue.main.async { done(result, note) }
        }
    }

    func post(sessionID: String, channel: MeetingChannel, text: String, at: Date, done: @escaping (Error?) -> Void) {
        queue.async { [self] in
            var err: Error?
            do {
                guard let token else { throw WaterClientError.noToken("no meeting session token") }
                try client.postMeetingSegment(sessionID: sessionID, channel: channel, text: text, at: at, token: token)
            } catch {
                err = error
            }
            DispatchQueue.main.async { done(err) }
        }
    }

    func stop(sessionID: String, done: @escaping (Error?) -> Void) {
        queue.async { [self] in
            var err: Error?
            do {
                guard let token else { throw WaterClientError.noToken("no meeting session token") }
                try client.stopMeeting(sessionID: sessionID, token: token)
            } catch {
                err = error
            }
            token = nil
            DispatchQueue.main.async { done(err) }
        }
    }

    /// Blocking, for app quit.
    func stopNow(sessionID: String) {
        queue.sync { [self] in
            if let token { _ = try? client.stopMeeting(sessionID: sessionID, token: token) }
            token = nil
        }
    }
}
