# Slice B (interim): the macOS client

Status: **text bar and voice built and compiling; verified against the live
daemon over the socket. The global hotkeys, microphone, speech recognition and
spoken replies need the owner's permission grants and haven't been tested end to end.**

`clients/macos/` is a Swift package. It is a pure daemon client with no agent
logic (CONTEXT.md principle 5). It uses only Apple frameworks (AppKit,
Foundation, Speech, AVFoundation, Security, ApplicationServices), has no
third-party packages and makes no metered calls. It builds without Xcode.

## Layout
- `Sources/WaterClientCore/`: the testable core.
  - `HTTP.swift`: the request serializer and an incremental response parser.
    The parser handles chunked encoding, which Go uses for `/v1/turns`.
  - `Events.swift`: `TurnEvent` mirrors `runtime.Event`, which has a `kind`
    field (not `type`). Also the NDJSON line splitter.
  - `UnixSocketClient.swift`: POSIX `socket`/`connect`/`write`/`read`. It
    streams events as each line completes, and cancels by shutting the socket down.
  - `Tokens.swift`: the keychain store (`SecItem*`, service `water.client`,
    account `macos`), a `clients.json` reader, and `TokenProvider`.
- `Sources/Water/`: the app.
  - `main.swift`: plain `NSApplication`, `.accessory` activation policy.
  - `AppDelegate.swift`: the status item and wiring.
  - `AskPanel.swift`: the borderless floating panel.
  - `HotKeys.swift`: **hotkey constants at the top of the file.**
  - `Voice.swift`, `TurnRunner.swift`, `SelfTest.swift`.
- `build.sh`: runs `swift build -c release`, assembles `build/Water.app`
  (Info.plist with LSUIElement and the mic/speech usage strings), and
  ad-hoc signs it. `--install` also copies it to `~/Applications`.
- `test.sh`: runs `swift test` with the flags that swift-testing needs
  when only the Command Line Tools are installed. XCTest doesn't exist
  without full Xcode, so the tests use swift-testing, not XCTest.
  - `WATER_KEYCHAIN_TEST=1` adds a real login-keychain round trip.
  - `WATER_LIVE_DAEMON_TEST=1` probes the real `/v1/health`.

## Hotkeys
- **⌃⌥Space** opens the text bar. Type, press Enter, and the reply streams
  in. Escape closes it. The turn is sent with `channel: "text-bar"`.
- **⌃⌥V** is push-to-talk. Press once to start listening and again to send.
  - The transcript goes out as a `voice` turn.
  - Each `sentence` event is spoken with `AVSpeechSynthesizer` as it arrives.
  - Recognition is on-device only (`requiresOnDeviceRecognition`). If the
    Mac can't recognize speech on-device, voice refuses to run rather than
    sending audio to a server.
- The drop icon in the menu bar has three items: "Ask Water" (works with no
  permissions), "Talk to Water", and Quit.

## What the owner needs to do
1. **Launch:** `open ~/Applications/Water.app`. A copy was installed and
   left running overnight.
   - To rebuild: `cd clients/macos && ./build.sh --install`.
2. **Accessibility (global hotkeys):** open System Settings > Privacy &
   Security > Accessibility and turn on **Water**. If Water isn't listed,
   click **+**, pick `~/Applications/Water.app`, and turn it on. The app
   picks up the grant within about 3 seconds, with no relaunch.
   - A one-time alert explains this at first launch.
   - Until you grant it, use the menu-bar icon's "Ask Water".
3. **Microphone and Speech Recognition:** press ⌃⌥V the first time and
   click **Allow** on both system prompts. If you denied them, re-enable
   them under System Settings > Privacy & Security > Microphone and
   > Speech Recognition.
   - If on-device recognition isn't available, turn on Dictation in System
     Settings > Keyboard and let English download.
4. **Restart the daemon once** so it loads the `macos-client` token.
   - Until then the app falls back to the CLI's token and shows a note
     saying so.
5. **After any rebuild**, the ad-hoc signature changes, so:
   - Toggle Water's Accessibility switch off and on.
   - Expect one keychain "allow access" prompt.

## Findings from reading the daemon
- **The daemon read `clients.json` only at startup** (`gateway.LoadClients`
  in `runDaemon`). A token minted by `water daemon token new` got a 401
  until the daemon restarted, which is why the app falls back to the `cli`
  token. *Fixed in the 2026-09-24 review pass:* the daemon now re-reads the
  file on an unknown token. The fallback remains only for older daemons.
- **`approval_required` was never emitted** (fixed in the whole-system
  review). A model tool call that the daemon queues for approval is now
  reported on the open `POST /v1/turns` stream as `approval_required`
  (`approval_id`, and the action in `text`), before `done`. *Since the
  2026-09-24 review pass:* the daemon runs one model turn at a time, and the
  event goes only to the stream of the turn whose model is running. It also
  carries `action`, `risk` and `payload_hash`. A call that lands after its
  turn's stream closed is still queued, just not announced inline. See A2.md
  for the full contract. Voice approvals are still Slice B work.
- `sentence` events come only on the `voice` channel. Fast-path answers get
  them too (`deliverText`).

## Verified here
- The release build compiles with no warnings.
- `./test.sh` passes 24 tests, covering:
  - request bytes;
  - the chunked and content-length parser at every split size;
  - NDJSON reassembly, including multibyte characters split across chunks;
  - streaming against an in-test Unix-socket server;
  - 401 handling and cancellation;
  - token resolution, including minting through a fake `water` binary.
- The opt-in tests also pass: the real keychain round trip, and a real
  `/v1/health` probe.
- `Water --selftest` from the bundle, against the live daemon:
  - health is ok;
  - the new `macos-client` token got a 401, as predicted;
  - with the `cli` token, `text-bar` and `voice` turns streamed
    `ack`/`delta`/`sentence`/`done`.
- Both the `open`-launched bundle and the `--ask` GUI path stayed running
  with no crash reports. The app itself wrote the keychain item.

## Not verifiable without the owner
- A global hotkey actually firing, which needs Accessibility.
- Microphone capture and on-device transcription, which need two grants and a voice.
- TTS being audible.
- How the panel looks. This session has no Screen Recording permission,
  so screenshots show only the wallpaper.
