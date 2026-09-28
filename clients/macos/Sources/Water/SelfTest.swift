import Foundation
import WaterClientCore

/// `Water --selftest [--channel text-bar|voice|cli] [prompt]`: exercises the
/// same daemon client the app uses, headless, printing every event. It needs
/// no macOS permission and never touches the keychain (a keychain read from a
/// freshly rebuilt ad-hoc binary could raise a GUI prompt): the token comes
/// straight from clients.json — "macos-client" first, then "cli" on a 401.
enum SelfTest {
    static func run(arguments: [String]) -> Int32 {
        var args = Array(arguments.dropFirst().filter { $0 != "--selftest" })
        var channel = Channel.textBar
        if let i = args.firstIndex(of: "--channel"), i + 1 < args.count {
            guard let c = Channel(rawValue: args[i + 1]) else {
                print("unknown channel \(args[i + 1])"); return 2
            }
            channel = c
            args.removeSubrange(i...(i + 1))
        }
        let prompt = args.isEmpty ? "what's on my schedule today?" : args.joined(separator: " ")
        let client = UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath())
        print("socket: \(client.socketPath)")

        do {
            let h = try client.health()
            print("health: \(h)")
        } catch {
            print("health failed: \(error.localizedDescription)")
            return 1
        }

        let clients = ClientsFile.defaultPath()
        var candidates: [(String, String)] = []
        if let t = ClientsFile.token(named: "macos-client", path: clients) { candidates.append(("macos-client", t)) }
        if let t = ClientsFile.token(named: "cli", path: clients) { candidates.append(("cli", t)) }
        guard !candidates.isEmpty else { print("no token in \(clients)"); return 1 }

        for (name, tok) in candidates {
            print("turn: channel=\(channel.rawValue) token=\(name) prompt=\(prompt.debugDescription)")
            let start = Date()
            var sawDone = false
            do {
                try client.streamTurn(channel: channel, prompt: prompt, token: tok) { e in
                    let ms = Int(Date().timeIntervalSince(start) * 1000)
                    var line = "  +\(ms)ms \(e.kind)"
                    if let t = e.text { line += " text=\(t.debugDescription)" }
                    if let a = e.approvalID { line += " approval_id=\(a)" }
                    if let err = e.error { line += " error=\(err.debugDescription)" }
                    print(line)
                    if e.kind == .done { sawDone = true }
                }
                return sawDone ? 0 : 1
            } catch WaterClientError.http(status: 401, body: let body) {
                print("  401 with the \(name) token: \(body.trimmingCharacters(in: .whitespacesAndNewlines))")
                continue
            } catch {
                print("  failed: \(error.localizedDescription)")
                return 1
            }
        }
        return 1
    }
}
