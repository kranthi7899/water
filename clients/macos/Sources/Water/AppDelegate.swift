import AppKit
import ApplicationServices
import WaterClientCore

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var accessibilityItem: NSMenuItem!
    private let panel = AskPanelController()
    private let runner = TurnRunner()
    /// Which engine (Apple Speech, or FluidAudio's Parakeet+Kokoro pair)
    /// this run actually uses — decided once in `applicationDidFinishLaunching`
    /// by `EngineSelector.resolveBlocking()` (`EngineSelection.choose`,
    /// WaterClientCore, is the pure decision it's built on), before any of
    /// the three properties below are constructed.
    private var voice: VoiceController!
    /// The real speech synthesizer, behind `SpeechOutput` (WaterClientCore)
    /// so `speechQueue` doesn't need to know which engine it is —
    /// `AppleSpeechOutput` or (FluidAudio selected) `KokoroSpeaker`.
    private var speechOutput: SpeechOutput!
    /// Feeds reply sentences to `speechOutput` in order, overlapping
    /// synthesis with playback and handling barge-in — see
    /// WaterClientCore/SpeechOutput.swift.
    private var speechQueue: SentenceSpeechQueue!
    /// Which turn may speak: bumped by every new turn and by every barge-in,
    /// so a sentence event from a replaced or silenced turn is never queued.
    private var speakingTurn = 0
    private var meeting: MeetingController!
    private var meetingItem: NSMenuItem!
    private var hotkeys: HotKeyMonitor!
    /// The workspace window (Slice V-ui): the daemon's web UI in a
    /// WKWebView, reached only through the `water://` scheme handler.
    private var workspace: WorkspaceWindowController!
    /// Live only while push-to-talk is listening (R-27): streams
    /// SFSpeechRecognizer's partial results to the daemon, and hands its
    /// turn id to the final `send(_:channel:turnID:)` call so the daemon can
    /// reuse whatever speculative work it already did.
    private var partialStreamer: PartialStreamer?
    /// The in-flight voice turn's latency trace (V-8, docs/slices/V.md §4):
    /// created at key-up, marked at each checkpoint as it happens, printed
    /// and cleared on that turn's `done`/`error`. Nil outside a voice turn,
    /// and for a toggle-started (rather than held) capture, which has no
    /// key-up moment to start it from.
    private var voiceTrace: VoiceTurnTrace?
    /// Which `speakingTurn` `voiceTrace` belongs to, so a stray event from a
    /// different turn (or from `channel != .voice`) never marks it.
    private var voiceTraceTurn: Int?

    func applicationDidFinishLaunching(_ note: Notification) {
        setUpStatusItem()
        setUpEngine()

        panel.onSubmit = { [weak self] text in self?.send(text, channel: .textBar) }
        panel.onClose = { [weak self] in
            guard let self else { return }
            // Closing the panel (Escape, or the text-bar hotkey) silences a
            // spoken reply. The turn itself keeps running into the hidden
            // panel, so reopening it shows the rest of the answer.
            self.interruptSpeech()
            if self.voice.state == .idle { self.clearStatus() }
        }
        setUpVoice()
        setUpMeeting()
        setUpWorkspace()
        fetchVoiceProfile()

        hotkeys = HotKeyMonitor { [weak self] key in
            guard let self else { return }
            switch key {
            case HotKeyConfig.textBar: self.toggleTextBar()
            case HotKeyConfig.meeting: self.meeting.toggle()
            case HotKeyConfig.workspace: self.workspace.toggle()
            case HotKeyConfig.voice: self.voice.startHold() // key went down: start recording
            default: break
            }
        }
        // Voice is push-to-talk by holding, so "stop" is the key going back
        // up, not a second press — a separate signal from the keyDown above.
        hotkeys.onVoiceKeyUp = { [weak self] in
            guard let self else { return }
            // Key-up is this trace's start (V-8) — a fresh trace per hold,
            // discarding any older one that never reached done/error (e.g.
            // a second hold started before the first turn's daemon reply
            // finished): diagnostic instrumentation, not correctness, so
            // losing that stale trace's log line is an acceptable trade for
            // never mixing two holds' checkpoints together.
            self.voiceTrace = VoiceTurnTrace()
            self.voiceTrace?.mark(.keyUp)
            self.voice.endHold()
        }
        hotkeys.onTrustChange = { [weak self] _ in self?.refreshAccessibilityItem() }
        hotkeys.start()
        refreshAccessibilityItem()

        // `open Water.app --args --ask "question"`: open the panel and submit,
        // exactly as if typed — a way to exercise the whole GUI path with no
        // hotkey and no click.
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: "--ask"), i + 1 < args.count {
            panel.show()
            panel.setInput(args[i + 1])
            send(args[i + 1], channel: .textBar)
        }

        if !HotKeyMonitor.isTrusted { explainAccessibilityOnce() }
    }

    func applicationWillTerminate(_ note: Notification) {
        meeting.shutdown()
    }

    // MARK: status item

    private func setUpStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let button = statusItem.button {
            if let img = NSImage(systemSymbolName: "drop.fill", accessibilityDescription: "Water") {
                img.isTemplate = true
                button.image = img
            } else {
                button.title = "W"
            }
        }
        let menu = NSMenu()
        let ask = NSMenuItem(title: "Ask Water  (\(HotKeyConfig.textBar.label))", action: #selector(askFromMenu), keyEquivalent: "")
        ask.target = self
        menu.addItem(ask)
        let talk = NSMenuItem(title: "Talk to Water  (\(HotKeyConfig.voice.label))", action: #selector(talkFromMenu), keyEquivalent: "")
        talk.target = self
        menu.addItem(talk)
        meetingItem = NSMenuItem(title: "", action: #selector(meetingFromMenu), keyEquivalent: "")
        meetingItem.target = self
        menu.addItem(meetingItem)
        let ws = NSMenuItem(title: "Open Workspace  (\(HotKeyConfig.workspace.label))", action: #selector(workspaceFromMenu), keyEquivalent: "")
        ws.target = self
        menu.addItem(ws)
        menu.addItem(.separator())
        accessibilityItem = NSMenuItem(title: "", action: #selector(openAccessibilitySettings), keyEquivalent: "")
        accessibilityItem.target = self
        menu.addItem(accessibilityItem)
        menu.addItem(.separator())
        menu.addItem(NSMenuItem(title: "Quit Water", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
        statusItem.menu = menu
    }

    private func refreshAccessibilityItem() {
        if HotKeyMonitor.isTrusted {
            accessibilityItem.title = "Global hotkeys: on"
            accessibilityItem.action = nil
        } else {
            accessibilityItem.title = "Global hotkeys: off — grant Accessibility…"
            accessibilityItem.action = #selector(openAccessibilitySettings)
        }
    }

    @objc private func askFromMenu() { panel.show() }

    @objc private func talkFromMenu() { voice.toggle() }

    @objc private func meetingFromMenu() { meeting.toggle() }

    @objc private func workspaceFromMenu() { workspace.show() }

    // MARK: text bar

    private func toggleTextBar() {
        if panel.isVisible, voice.state == .idle { panel.close() } else { panel.show() }
    }

    /// Barge-in: stop speaking now and drop anything still to be spoken for
    /// the current turn. Every path that replaces or dismisses a reply goes
    /// through here.
    private func interruptSpeech() {
        speakingTurn += 1
        speechQueue.stop()
    }

    private func send(_ text: String, channel: Channel, turnID: String? = nil) {
        interruptSpeech() // runner.run cancels the old stream; this silences it
        let turn = speakingTurn
        // V-8: this is the only voice turn `voiceTrace` (started at
        // key-up) ever attaches to — a text-bar/CLI turn, or a
        // toggle-started voice turn with no key-up trace, just leaves it
        // untouched (nil, or still whichever turn it belongs to).
        if channel == .voice, voiceTrace != nil {
            voiceTrace?.mark(.requestSent)
            voiceTraceTurn = turn
        }
        panel.beginReply()
        panel.setStatus(channel == .voice ? "Thinking… (voice)" : "Thinking…")
        // During meeting capture every question is about the meeting: the
        // daemon adds its recent transcript (untrusted, and it taints the turn).
        let meetingID = meeting.state == .active ? meeting.session?.id : nil
        runner.run(channel: channel, prompt: text, meetingID: meetingID, turnID: turnID, onEvent: { [weak self] e in
            guard let self else { return }
            switch e.kind {
            case .ack:
                if channel == .voice, turn == self.voiceTraceTurn { self.voiceTrace?.mark(.ack) }
            case .delta:
                self.clearStatus()
                self.panel.appendReply(e.text ?? "")
            case .sentence:
                if channel == .voice, turn == self.speakingTurn {
                    if turn == self.voiceTraceTurn { self.voiceTrace?.mark(.firstSentence) }
                    self.speechQueue.enqueue(e.text ?? "")
                }
            case .approvalRequired:
                self.panel.appendApproval(id: e.approvalID, action: e.approvalAction, risk: e.risk)
            case .handoff:
                // Safe-decode only for now (R-27): no code path emits this
                // kind yet (the daemon still sends a zero-text `ack` for the
                // same moment, see Events.swift's doc comment on
                // TurnEvent.Kind.handoff), and it isn't clear the plan wants
                // dedicated UI beyond that existing "Thinking…" status line
                // once it does. Flagged as a follow-up rather than guessed.
                break
            case .done:
                // Terminal: nothing follows done or error on a turn stream.
                self.clearStatus()
                self.finishVoiceTrace(turn: turn, mark: .done)
            case .error:
                self.clearStatus()
                self.panel.appendError(e.error ?? "the daemon reported an error")
                // No `.done` mark here: the log line just ends at whatever
                // checkpoint this turn actually reached before it errored.
                self.finishVoiceTrace(turn: turn, mark: nil)
            case .unknown:
                break
            }
        }, onNote: { [weak self] note in
            self?.panel.appendNote(note)
        }, onFinish: { [weak self] err in
            guard let self else { return }
            self.clearStatus()
            if let err {
                self.panel.appendError(err.localizedDescription)
            }
        })
    }

    /// Ends this turn's voice trace, if it has one (V-8): marks `mark` (when
    /// given), prints the one structured log line, and clears it so a later
    /// stray event can't reopen it. A no-op for a turn `voiceTrace` was
    /// never attached to (any non-voice channel, or a toggle-started voice
    /// turn — see `voiceTrace`'s doc comment).
    private func finishVoiceTrace(turn: Int, mark: VoiceTurnTrace.Checkpoint?) {
        guard turn == voiceTraceTurn, voiceTrace != nil else { return }
        if let mark { voiceTrace?.mark(mark) }
        if let trace = voiceTrace { print(trace.logLine(turnID: "\(turn)")) }
        voiceTrace = nil
        voiceTraceTurn = nil
    }

    // MARK: voice

    /// Builds `voice`/`speechOutput`/`speechQueue` from whichever engine
    /// `EngineSelector` resolves this run to — `.fluidAudio` (Apple Silicon,
    /// models already downloaded or just accepted-and-downloaded) wires in
    /// `ParakeetCapture`/`KokoroSpeaker`; `.appleSpeech` (Intel/Rosetta,
    /// models not ready, or the user declined) keeps today's
    /// `OnDeviceSpeechCapture`/`AppleSpeechOutput`. Runs before every other
    /// piece of setup that touches these three properties.
    private func setUpEngine() {
        switch EngineSelector.resolveBlocking() {
        case .fluidAudio:
            speechOutput = KokoroSpeaker(manager: EngineSelector.kokoroManager)
            voice = VoiceController(holdLabel: HotKeyConfig.voice.label, capture: ParakeetCapture())
        case .appleSpeech:
            speechOutput = AppleSpeechOutput()
            voice = VoiceController(holdLabel: HotKeyConfig.voice.label)
        }
        speechQueue = SentenceSpeechQueue(output: speechOutput)
        // V-8: "first audio" is hand-off to whichever engine setUpEngine
        // just chose — see VoiceTrace.swift's doc comment on
        // Checkpoint.firstAudio for why hand-off, not the engine's own
        // internal playback-start signal.
        speechQueue.onWillPlay = { [weak self] in self?.voiceTrace?.mark(.firstAudio) }
    }

    private func setUpVoice() {
        // A hold/toggle starting from idle silences any reply in progress
        // right away — before the (asynchronous) permission check and mic
        // start-up get anywhere. `onListening` below also calls
        // `interruptSpeech()` once the mic actually opens; this is the
        // earlier, synchronous cut.
        voice.onNeedsSilence = { [weak self] in self?.interruptSpeech() }
        voice.onListening = { [weak self] in
            guard let self else { return }
            self.runner.cancel()
            self.interruptSpeech()
            self.panel.setInput("")
            self.panel.show(placeholder: "Listening… release \(HotKeyConfig.voice.label) to send")
            self.panel.setStatus("● Listening")
            // A fresh turn id for this capture (R-27): SFSpeechRecognizer's
            // partial results stream to the daemon under it as they arrive,
            // fire-and-forget, so the daemon can start speculative work
            // before the final transcript is ready.
            self.partialStreamer = PartialStreamer(channel: .voice, tokenProvider: self.runner.tokens.token,
                                                    transport: self.runner.client)
        }
        voice.onPartial = { [weak self] text in
            self?.panel.setInput(text)
            self?.partialStreamer?.post(text)
        }
        voice.onTranscript = { [weak self] text in
            guard let self else { return }
            self.voiceTrace?.mark(.sttFinal)
            self.panel.setInput(text)
            let turnID = self.partialStreamer?.turnID
            self.partialStreamer = nil
            self.send(text, channel: .voice, turnID: turnID)
        }
        voice.onFailure = { [weak self] message in
            guard let self else { return }
            self.partialStreamer = nil
            self.clearStatus()
            self.panel.show()
            self.panel.beginReply()
            self.panel.appendError(message)
        }
    }

    /// Fetches the twin's TTS profile once at launch (`GET
    /// /v1/voice/profile`, R-27) and applies it to the voice controller.
    /// Best-effort: the daemon may not be reachable yet (or ever, until the
    /// user runs `water daemon`), and `VoiceController.speak` already falls
    /// back to `AVSpeechUtterance`'s own defaults when no profile is
    /// applied — so any failure here is silently ignored rather than
    /// blocking or erroring app startup.
    private func fetchVoiceProfile() {
        let client = runner.client
        let tokens = runner.tokens
        DispatchQueue.global(qos: .utility).async { [weak self] in
            guard let token = try? tokens.token(), let profile = try? client.fetchVoiceProfile(token: token) else { return }
            DispatchQueue.main.async {
                // `applyVoiceProfile` isn't part of `SpeechOutput` (the
                // protocol stays free of VoiceProfile, matching its
                // testable-with-a-fake design — see SpeechOutput.swift):
                // each real conformer applies the fetched profile its own
                // way, so this switches on whichever one `setUpEngine`
                // actually constructed.
                switch self?.speechOutput {
                case let apple as AppleSpeechOutput: apple.applyVoiceProfile(profile)
                case let kokoro as KokoroSpeaker: kokoro.applyVoiceProfile(profile)
                default: break
                }
            }
        }
    }

    // MARK: meeting

    private func setUpMeeting() {
        let client = UnixSocketClient(socketPath: runner.client.socketPath)
        client.readTimeout = 15
        meeting = MeetingController(daemon: MeetingDaemon(client: client, tokens: runner.tokens))
        meeting.onStateChange = { [weak self] _ in self?.refreshMeetingIndicator() }
        meeting.onNote = { [weak self] note in
            guard let self else { return }
            self.panel.show()
            self.panel.appendNote(note)
        }
        meeting.onFailure = { [weak self] message in
            guard let self else { return }
            self.panel.show()
            self.panel.beginReply()
            self.panel.appendError(message)
        }
        refreshMeetingIndicator()
    }

    // MARK: workspace

    private func setUpWorkspace() {
        // Its own client: page requests (a streaming thread reply among
        // them) must not share a CancelToken or timeout with the text bar.
        let client = UnixSocketClient(socketPath: runner.client.socketPath)
        workspace = WorkspaceWindowController(client: client, tokens: runner.tokens)
        // The page's mic button: the same push-to-talk toggle as the menu's
        // "Talk to Water". A click can't be held, so it toggles (click to
        // start listening, click again to send) rather than hold-to-talk;
        // the transcript goes out as an ordinary voice turn and its reply
        // shows in the text bar and is spoken, exactly as from the hotkey.
        workspace.onMic = { [weak self] in self?.voice.toggle() }
    }

    /// The listening indicator: while a session is live the menu-bar icon
    /// turns into a red waveform, the menu says so, and the panel's status
    /// line reads "● Meeting" whenever nothing else is using it.
    private func refreshMeetingIndicator() {
        let live = meeting.state != .idle
        if let button = statusItem.button {
            let name = live ? "waveform.circle.fill" : "drop.fill"
            if let img = NSImage(systemSymbolName: name, accessibilityDescription: live ? "Water — capturing a meeting" : "Water") {
                img.isTemplate = true
                button.image = img
                button.title = ""
            } else {
                button.title = live ? "●" : "W"
            }
            button.contentTintColor = live ? .systemRed : nil
            button.toolTip = live ? "Water is capturing this meeting (\(HotKeyConfig.meeting.label) to stop)" : nil
        }
        let label = HotKeyConfig.meeting.label
        switch meeting.state {
        case .idle: meetingItem.title = "Start Meeting Capture  (\(label))"
        case .starting: meetingItem.title = "Starting Meeting Capture…"
        case .active: meetingItem.title = "● Capturing Meeting — Stop  (\(label))"
        case .stopping: meetingItem.title = "Stopping Meeting Capture…"
        }
        meetingItem.isEnabled = meeting.state == .idle || meeting.state == .active
        clearStatus()
    }

    /// Clears the panel's status line, except for the meeting indicator.
    private func clearStatus() {
        panel.setStatus(meeting?.state == .active ? "● Meeting" : "")
    }

    // MARK: accessibility

    private static let axExplainedKey = "didExplainAccessibility"

    /// Shown once (not on every launch): what the Accessibility grant is for
    /// and exactly where to give it. The menu item stays as a reminder.
    private func explainAccessibilityOnce() {
        let d = UserDefaults.standard
        guard !d.bool(forKey: Self.axExplainedKey) else { return }
        d.set(true, forKey: Self.axExplainedKey)

        let alert = NSAlert()
        alert.messageText = "Turn on Water's global hotkeys"
        alert.informativeText = """
        To open Water from any app with \(HotKeyConfig.textBar.label) (text), \(HotKeyConfig.voice.label) (voice), \(HotKeyConfig.meeting.label) (meeting capture) and \(HotKeyConfig.workspace.label) (workspace), macOS needs you to allow it once:

        1. Open System Settings > Privacy & Security > Accessibility.
        2. Turn on the switch next to "Water". If Water isn't listed, click +, choose Water.app, and turn it on.
        3. That's it — no relaunch needed.

        Until then, click the drop icon in the menu bar and choose "Ask Water".
        """
        alert.addButton(withTitle: "Open Accessibility Settings")
        alert.addButton(withTitle: "Later")
        NSApp.activate(ignoringOtherApps: true)
        if alert.runModal() == .alertFirstButtonReturn { openAccessibilitySettings() }
    }

    @objc private func openAccessibilitySettings() {
        // Asking with the prompt option also adds Water to the Accessibility
        // list (switched off), so the user only has to flip it on.
        let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: false] as CFDictionary
        _ = AXIsProcessTrustedWithOptions(opts)
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility") {
            NSWorkspace.shared.open(url)
        }
    }
}
