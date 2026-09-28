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
    /// The globe (owner brief 2026-09-26): a small orb, by default centred
    /// about three inches above the bottom of the screen (OverlayLayout),
    /// during an interaction, all through voice mode, or while a voice
    /// approval waits. Only its circle takes the mouse (hover, drag, dock
    /// into the menu bar, double-click menu); it never takes key focus.
    /// Replaces the Activity HUD's blob, steps and cards.
    private var hud: GlobeHUD!
    /// The glass tab (owner brief 2026-09-26): a floating translucent panel
    /// for what a voice request produced — an email draft, or an approval
    /// with Approve / Edit / Reject.
    private var glass: GlassTabController!
    /// Native notifications (V-notify): polls the daemon's needs-you
    /// notifications and opens a tapped one's thread in the workspace. Nil
    /// when not running as a bundled app.
    private var notifier: Notifier?
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
    /// The workspace thread the next voice transcript goes to (V-ui2):
    /// set only by the page's mic-down, cleared by any other capture.
    private var workspaceVoice = WorkspaceVoiceTarget()
    /// Voice mode (owner brief 2026-09-26): ⌃⌥V on/off, Space held to talk,
    /// Esc or a minute of true idleness to leave. The rules are
    /// WaterClientCore's `VoiceMode`; this carries out its effects.
    private var voiceMode = VoiceMode()
    /// Space and Esc as Carbon hot keys, registered only while voice mode
    /// is on. Every press and release goes through `VoiceMode.key`.
    private lazy var captureKeys = CaptureKeys { [weak self] key, pressed in
        guard let self else { return }
        self.applyVoiceMode(self.voiceMode.key(key, pressed: pressed, now: Date()))
    }
    /// Voice mode's clock (idle timeout, approval pinning); runs only while on.
    private var voiceModeTimer: Timer?
    /// True while Space is down on a capture it started in voice mode, so
    /// Space's release never ends a hold the workspace mic owns.
    private var spaceOwnsHold = false
    /// True from a Space-started capture's start until it delivers, fails
    /// or is cancelled — including while it finishes after release — so Esc
    /// cancels exactly the captures voice mode started.
    private var spaceCaptureActive = false
    /// The globe's id for the voice turn in flight (nil when none), so Esc
    /// cancels a voice turn and never a typed one.
    private var activeVoiceTurn: Int?
    private var voiceModeItem: NSMenuItem!

    func applicationDidFinishLaunching(_ note: Notification) {
        setUpStatusItem()
        setUpEngine()
        setUpGlobeAndGlass()

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
        setUpNotifier()
        fetchVoiceProfile()

        hotkeys = HotKeyMonitor { [weak self] key in
            guard let self else { return }
            switch key {
            case HotKeyConfig.textBar: self.toggleTextBar()
            case HotKeyConfig.meeting: self.meeting.toggle()
            case HotKeyConfig.workspace: self.workspace.toggle()
            case HotKeyConfig.voice:
                // Voice mode on/off. The hold itself is ⌃V (CaptureKeys),
                // so ⌃⌥V's release means nothing now (no onVoiceKeyUp).
                self.toggleVoiceMode()
            default: break
            }
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
        applyVoiceMode(voiceMode.shutdown())
        captureKeys.unregister() // belt and braces: never leave Space captured
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
        voiceModeItem = NSMenuItem(title: "Voice Mode  (\(HotKeyConfig.voice.label))", action: #selector(talkFromMenu), keyEquivalent: "")
        voiceModeItem.target = self
        menu.addItem(voiceModeItem)
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

    /// The menu's "Voice Mode" does what ⌃⌥V does. Space and Esc are Carbon
    /// hot keys, so this works even before the Accessibility grant.
    @objc private func talkFromMenu() { toggleVoiceMode() }

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

    private func send(_ text: String, channel: Channel, turnID: String? = nil, threadID: String? = nil) {
        interruptSpeech() // runner.run cancels the old stream; this silences it
        let turn = speakingTurn
        let hudTurn = hud.turnSent()
        voiceMode.turnStarted(hudTurn, now: Date())
        activeVoiceTurn = channel == .voice ? hudTurn : nil
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
        // A workspace thread turn (the page's held mic) is about the thread,
        // not the meeting: the daemon refuses the pair, so the thread wins.
        let meetingID = meeting.state == .active && threadID == nil ? meeting.session?.id : nil
        runner.run(channel: channel, prompt: text, meetingID: meetingID, turnID: turnID, threadID: threadID, onEvent: { [weak self] e in
            guard let self else { return }
            // An approval pins the globe only when the glass tab can show
            // it (a voice turn); a typed turn's approval is a line in the
            // text bar, as before, and never leaves the orb stuck on screen.
            if e.kind != .approvalRequired || channel == .voice { self.hud.event(e, turn: hudTurn) }
            self.glass.event(e, channel: channel)
            // Only a voice turn's real `sentence` is spoken: an `ack` (the
            // daemon's silent handoff, Slice W D3) never queues a filler, so
            // the globe keeps THINKING until the model's first sentence.
            if let spoken = e.spokenText(channel: channel), turn == self.speakingTurn {
                if turn == self.voiceTraceTurn { self.voiceTrace?.mark(.firstSentence) }
                self.speechQueue.enqueue(spoken)
            }
            switch e.kind {
            case .ack:
                if channel == .voice, turn == self.voiceTraceTurn { self.voiceTrace?.mark(.ack) }
            case .delta:
                self.clearStatus()
                self.panel.appendReply(e.text ?? "")
            case .sentence:
                break // spoken above (`spokenText`)
            case .approvalRequired:
                self.panel.appendApproval(id: e.approvalID, action: e.approvalAction, risk: e.risk)
            case .handoff:
                // Safe-decode only (R-27): no code path emits this kind (the
                // daemon sends a silent `ack` for the same moment, Slice W
                // D3). Like `ack`, it is never spoken and keeps "Thinking…".
                break
            case .done:
                // Terminal: nothing follows done or error on a turn stream.
                self.clearStatus()
                self.finishVoiceTrace(turn: turn, mark: .done)
                // The daemon stored both messages in the thread before done:
                // show them in the open page.
                if threadID != nil { self.workspace.refresh() }
            case .error:
                self.clearStatus()
                self.panel.appendError(e.error ?? "the daemon reported an error")
                // In voice mode the text bar stays closed: say it on the tab.
                if channel == .voice, self.voiceMode.isOn {
                    self.glass.notice("Water couldn't answer that: \(e.error ?? "the daemon reported an error")")
                }
                // No `.done` mark here: the log line just ends at whatever
                // checkpoint this turn actually reached before it errored.
                self.finishVoiceTrace(turn: turn, mark: nil)
            case .queued, .toolStart, .toolEnd, .artifact:
                // Steps are not drawn anywhere (owner brief 2026-09-26); a
                // voice draft's artifact shows in the glass tab (above).
                // The panel's transcript stays text only.
                break
            case .unknown:
                break
            }
        }, onNote: { [weak self] note in
            self?.panel.appendNote(note)
        }, onFinish: { [weak self] err in
            guard let self else { return }
            self.hud.turnEnded(turn: hudTurn)
            self.voiceMode.turnFinished(hudTurn, now: Date())
            if self.activeVoiceTurn == hudTurn { self.activeVoiceTurn = nil }
            self.clearStatus()
            if let err {
                self.panel.appendError(err.localizedDescription)
                if channel == .voice, self.voiceMode.isOn {
                    self.glass.notice("Water couldn't answer that: \(err.localizedDescription)")
                }
            }
            // Also on a failed or refused thread turn, so the page drops its
            // "sent by voice" line and shows what was (or wasn't) stored.
            if threadID != nil { self.workspace.refresh() }
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
            // The same instance the download flow loaded (if it ran this
            // launch); otherwise load it now, so the first hold is fast.
            EngineSelector.parakeet.prewarm()
            voice = VoiceController(holdLabel: "Space", capture: EngineSelector.parakeet)
        case .appleSpeech:
            speechOutput = AppleSpeechOutput()
            voice = VoiceController(holdLabel: "Space")
        }
        speechQueue = SentenceSpeechQueue(output: speechOutput)
        // V-8: "first audio" is hand-off to whichever engine setUpEngine
        // just chose — see VoiceTrace.swift's doc comment on
        // Checkpoint.firstAudio for why hand-off, not the engine's own
        // internal playback-start signal.
        speechQueue.onWillPlay = { [weak self] in
            self?.voiceTrace?.mark(.firstAudio)
            self?.hud?.speechStarted()
            self?.voiceMode.speechStarted(now: Date())
        }
        speechQueue.onIdle = { [weak self] in
            self?.hud?.speechIdle()
            self?.voiceMode.speechIdle(now: Date())
        }
    }

    /// The globe and the glass tab, fed from here: hotkey and capture
    /// (`holdStarted`/`holdEnded`/`cancelled`, mic level), every turn's
    /// events (`send`), and the speech queue (started, idle, output level).
    /// Each has its own socket client, so a click or a re-read never waits
    /// behind a turn stream.
    private func setUpGlobeAndGlass() {
        let hudClient = UnixSocketClient(socketPath: runner.client.socketPath)
        hudClient.readTimeout = 30
        hud = GlobeHUD(client: hudClient, tokens: runner.tokens)

        let glassClient = UnixSocketClient(socketPath: runner.client.socketPath)
        // An approved action runs inside the decision request (a send can
        // take a while); a timeout here is reported as "didn't go through"
        // and the re-read that follows shows what actually happened.
        glassClient.readTimeout = 120
        glass = GlassTabController(client: glassClient, tokens: runner.tokens)
        // The globe where the owner left it (or low and centred, clear of an
        // open text bar); the tab above it, following it when it moves or
        // docks (OverlayLayout).
        hud.avoidFrame = { [weak self] in self?.panel.frameIfVisible }
        glass.placement = { [weak self] size in self?.hud.glassOrigin(for: size) ?? GlobeHUD.glassOrigin(for: size) }
        hud.onMoved = { [weak self] in self?.glass.reposition() }
        // The globe's menu: "Quit voice mode" is exactly Esc (the same
        // router call CaptureKeys makes for an Esc press).
        hud.isVoiceModeOn = { [weak self] in self?.voiceMode.isOn ?? false }
        hud.onQuitVoiceMode = { [weak self] in
            guard let self else { return }
            self.applyVoiceMode(self.voiceMode.key(.escape, pressed: true, now: Date()))
        }
        glass.onEdit = { [weak self] id in self?.workspace.open(view: "approvals", id: id) }
        glass.onNote = { [weak self] note in
            guard let self else { return }
            // Voice mode never pops the text bar (it takes focus and Esc).
            if self.voiceMode.isOn { return self.glass.notice(note) }
            self.panel.show()
            self.panel.appendNote(note)
        }
        // The ✕ on an approval only hides the tab: the globe stops pinning
        // on it, and it stays pending in the workspace.
        glass.onApprovalHidden = { [weak self] id in self?.hud.approvalHidden(id) }
        // A click in the tab decided it: re-read what pins the globe.
        glass.onDecided = { [weak self] _ in self?.hud.refreshOpenApprovals() }
        // A re-read found an approval decided elsewhere: the tab re-reads too.
        hud.onApprovalsResolved = { [weak self] in self?.glass.refresh() }

        (speechOutput as? SpeechLevelSource)?.onLevel = { [weak self] v in self?.hud.ttsLevel(v) }
        voice.onLevel = { [weak self] v in self?.hud.micLevel(v) }
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
            // A cancelled stream never reports its finish.
            self.voiceMode.turnsCancelled(now: Date())
            self.activeVoiceTurn = nil
            self.interruptSpeech()
            self.hud.holdStarted()
            self.panel.setInput("")
            // In voice mode the globe is the listening indicator: the text
            // bar would take focus (and Esc) from the app the CEO is in.
            if !self.voiceMode.isOn {
                self.panel.show(placeholder: "Listening… release \(self.voice.holdLabel) to send")
            }
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
        voice.onWarmingUp = { [weak self] in
            guard let self else { return }
            // The first hold after launch waits for the speech models to
            // load (can be many seconds): its audio is queued, not lost.
            self.hud.transcriptWarmingUp()
            let hint = "Speech recognition is warming up — what you said is queued, one moment…"
            if self.voiceMode.isOn { self.glass.notice(hint) } else { self.panel.setStatus("Warming up…") }
        }
        voice.onTranscript = { [weak self] text in
            guard let self else { return }
            self.spaceCaptureActive = false
            self.voiceTrace?.mark(.sttFinal)
            self.panel.setInput(text)
            let turnID = self.partialStreamer?.turnID
            self.partialStreamer = nil
            self.send(text, channel: .voice, turnID: turnID, threadID: self.workspaceVoice.take())
        }
        voice.onFailure = { [weak self] message in
            guard let self else { return }
            let fromSpace = self.spaceCaptureActive
            self.spaceCaptureActive = false
            self.hud.cancelled()
            if self.workspaceVoice.take() != nil { self.workspace.refresh() }
            self.partialStreamer = nil
            self.clearStatus()
            if self.voiceMode.isOn || fromSpace {
                // Briefly on the tab; voice mode stays on.
                self.glass.notice(message)
                return
            }
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

    // MARK: voice mode

    private func toggleVoiceMode() {
        let entering = !voiceMode.isOn
        applyVoiceMode(voiceMode.toggle(now: Date()))
        if entering, voiceMode.isOn { explainVoiceModeOnce() }
    }

    /// Carries out `VoiceMode`'s effects, in order.
    private func applyVoiceMode(_ effects: [VoiceMode.Effect]) {
        for effect in effects {
            switch effect {
            case .registerCaptureKeys:
                guard captureKeys.register() else {
                    // Nothing is registered; leave voice mode at once.
                    applyVoiceMode(voiceMode.captureUnavailable())
                    return
                }
                startVoiceModeTimer()
            case .unregisterCaptureKeys:
                captureKeys.unregister()
                stopVoiceModeTimer()
            case .startHold:
                // Same path the old ⌃⌥V hold took. Only a hold started from
                // idle is Space's: one already running (the workspace mic)
                // is left alone, and so is its thread target.
                guard voice.state == .idle else {
                    if spaceCaptureActive, voice.state == .finishing {
                        glass.notice("Still working on what you just said…")
                    }
                    break
                }
                spaceOwnsHold = true
                spaceCaptureActive = true
                workspaceVoice.clear() // a Space question is never a thread message
                voice.holdLabel = "Space"
                voice.startHold()
            case .endHold:
                guard spaceOwnsHold else { break }
                spaceOwnsHold = false
                // Key-up is this trace's start (V-8) — a fresh trace per
                // hold, discarding any older one that never reached
                // done/error: diagnostic instrumentation, so losing a stale
                // trace's log line beats mixing two holds' checkpoints.
                voiceTrace = VoiceTurnTrace()
                voiceTrace?.mark(.keyUp)
                if voice.state == .listening { hud.holdEnded() }
                voice.endHold()
            case .cancelHold:
                // A Space capture, held or still finishing after release
                // (Esc then must stop its transcript from going out).
                let ours = spaceOwnsHold || spaceCaptureActive
                spaceOwnsHold = false
                spaceCaptureActive = false
                guard ours else { break }
                if voice.state != .idle {
                    voice.cancel()
                    hud.cancelled()
                    partialStreamer = nil
                    voiceTrace = nil
                    panel.setInput("")
                    clearStatus()
                }
            case .cancelTurn:
                // Only a voice turn: a typed one keeps running in its panel.
                guard activeVoiceTurn != nil else { break }
                activeVoiceTurn = nil
                runner.cancel() // closes the stream; its finish never arrives
                voiceMode.turnsCancelled(now: Date())
                if let t = voiceTraceTurn { finishVoiceTrace(turn: t, mark: nil) }
                clearStatus()
            case .stopSpeech:
                interruptSpeech()
            case .showGlobe:
                hud.keepVisible = true
            case .hideGlobe:
                hud.keepVisible = false
            case .exited(let reason):
                stopVoiceModeTimer()
                // Esc is immediate: the globe goes now (an approval still
                // open keeps it and the glass tab; Esc never closes one).
                if reason == .escape { hud.dismissNow() }
                if reason == .captureUnavailable {
                    panel.show()
                    panel.beginReply()
                    panel.appendError("Voice mode needs the Space key, but another app has it reserved as a shortcut.")
                }
            }
        }
        voiceModeItem.state = voiceMode.isOn ? .on : .off
    }

    private func startVoiceModeTimer() {
        guard voiceModeTimer == nil else { return }
        let t = Timer(timeInterval: 1, repeats: true) { [weak self] _ in
            guard let self else { return }
            let now = Date()
            // A voice approval pins the globe; voice mode waits it out.
            self.voiceMode.setApprovalPinned(self.hud.model.isPinned, now: now)
            // A Space capture still finishing (a warm-up can take longer
            // than the idle timeout) keeps voice mode on until it delivers.
            self.voiceMode.setCapturePending(self.spaceCaptureActive && self.voice.state != .idle, now: now)
            self.applyVoiceMode(self.voiceMode.tick(now: now))
        }
        RunLoop.main.add(t, forMode: .common)
        voiceModeTimer = t
    }

    private func stopVoiceModeTimer() {
        voiceModeTimer?.invalidate()
        voiceModeTimer = nil
    }

    private static let voiceModeExplainedKey = "didExplainVoiceMode"

    /// First entry only: one line on how voice mode works.
    private func explainVoiceModeOnce() {
        let d = UserDefaults.standard
        guard !d.bool(forKey: Self.voiceModeExplainedKey) else { return }
        d.set(true, forKey: Self.voiceModeExplainedKey)
        // On the glass tab, briefly: the text bar would take focus and Esc.
        glass.notice("Hold ⌃V to talk. Esc to leave voice mode.")
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
        // The bar's mic is hold-to-talk (V-ui2), like the voice hotkey:
        // pressed starts listening, released sends. The transcript goes out
        // as a voice turn with the page's thread_id (native only; the page
        // never reaches /v1/turns), so the question and reply land in that
        // thread. It is spoken and drives the globe like any voice turn, and
        // the page is refreshed on done.
        workspace.onMicDown = { [weak self] thread in
            guard let self else { return }
            guard self.voice.state == .idle else { return } // already listening from Space in voice mode
            self.spaceOwnsHold = false
            self.spaceCaptureActive = false
            self.workspaceVoice.micDown(thread: thread)
            self.voice.holdLabel = "the mic button"
            self.voice.startHold()
        }
        workspace.onMicUp = { [weak self] in
            guard let self, self.workspaceVoice.thread != nil else { return }
            if self.voice.state == .listening { self.hud.holdEnded() }
            self.voice.endHold()
        }
        // U16: "View related data"'s open affordance. The page already
        // handed over the real, server-resolved URL (see
        // WorkspaceMessage.openExternal's own doc comment); this closure's
        // only job is the host allowlist check, then NSWorkspace. A denied
        // or malformed URL opens nothing at all.
        workspace.onOpenExternal = { source, id, urlString in
            guard let url = WorkspaceAllowlist.resolvedExternalURL(source: source, id: id, urlString: urlString) else { return }
            NSWorkspace.shared.open(url)
        }
    }

    /// V-notify. Its own client, so a poll never waits behind a turn or a
    /// page request. It asks for notification permission only when there is
    /// a first banner to show.
    private func setUpNotifier() {
        let client = UnixSocketClient(socketPath: runner.client.socketPath)
        // Anchoring a decision rebuilds its card on the daemon, which can take a
        // moment; the poll itself answers at once.
        client.readTimeout = 120
        notifier = Notifier.makeIfBundled(client: client, tokens: runner.tokens)
        notifier?.onOpen = { [weak self] view, id in self?.workspace.open(view: view, id: id) }
        notifier?.start()
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
        To open Water from any app with \(HotKeyConfig.textBar.label) (text), \(HotKeyConfig.voice.label) (voice mode, then hold ⌃V to talk), \(HotKeyConfig.meeting.label) (meeting capture) and \(HotKeyConfig.workspace.label) (workspace), macOS needs you to allow it once:

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
