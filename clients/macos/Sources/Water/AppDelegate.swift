import AppKit
import ApplicationServices
import WaterClientCore

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var accessibilityItem: NSMenuItem!
    private let panel = AskPanelController()
    private let runner = TurnRunner()
    private let voice = VoiceController()
    private var meeting: MeetingController!
    private var meetingItem: NSMenuItem!
    private var hotkeys: HotKeyMonitor!

    func applicationDidFinishLaunching(_ note: Notification) {
        setUpStatusItem()

        panel.onSubmit = { [weak self] text in self?.send(text, channel: .textBar) }
        panel.onClose = { [weak self] in
            guard let self else { return }
            if self.voice.state == .idle { self.clearStatus() }
        }
        setUpVoice()
        setUpMeeting()

        hotkeys = HotKeyMonitor { [weak self] key in
            guard let self else { return }
            switch key {
            case HotKeyConfig.textBar: self.toggleTextBar()
            case HotKeyConfig.meeting: self.meeting.toggle()
            default: self.voice.startHold() // key went down: start recording
            }
        }
        // Voice is push-to-talk by holding, so "stop" is the key going back
        // up, not a second press — a separate signal from the keyDown above.
        hotkeys.onVoiceKeyUp = { [weak self] in self?.voice.endHold() }
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

    // MARK: text bar

    private func toggleTextBar() {
        if panel.isVisible, voice.state == .idle { panel.close() } else { panel.show() }
    }

    private func send(_ text: String, channel: Channel) {
        panel.beginReply()
        panel.setStatus(channel == .voice ? "Thinking… (voice)" : "Thinking…")
        runner.run(channel: channel, prompt: text, onEvent: { [weak self] e in
            guard let self else { return }
            switch e.kind {
            case .ack:
                break
            case .delta:
                self.clearStatus()
                self.panel.appendReply(e.text ?? "")
            case .sentence:
                if channel == .voice { self.voice.speak(e.text ?? "") }
            case .approvalRequired:
                self.panel.appendApproval(id: e.approvalID)
            case .done:
                self.clearStatus()
            case .error:
                self.clearStatus()
                self.panel.appendError(e.error ?? "the daemon reported an error")
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

    // MARK: voice

    private func setUpVoice() {
        voice.onListening = { [weak self] in
            guard let self else { return }
            self.runner.cancel()
            self.panel.setInput("")
            self.panel.show(placeholder: "Listening… release \(HotKeyConfig.voice.label) to send")
            self.panel.setStatus("● Listening")
        }
        voice.onPartial = { [weak self] text in self?.panel.setInput(text) }
        voice.onTranscript = { [weak self] text in
            guard let self else { return }
            self.panel.setInput(text)
            self.send(text, channel: .voice)
        }
        voice.onFailure = { [weak self] message in
            guard let self else { return }
            self.clearStatus()
            self.panel.show()
            self.panel.beginReply()
            self.panel.appendError(message)
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
        To open Water from any app with \(HotKeyConfig.textBar.label) (text), \(HotKeyConfig.voice.label) (voice) and \(HotKeyConfig.meeting.label) (meeting capture), macOS needs you to allow it once:

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
