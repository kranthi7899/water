import Foundation
import WaterClientCore

/// Runs one turn at a time against the daemon, off the main thread, and
/// delivers every event back on the main thread. A new turn cancels the one
/// in flight (closing its socket, which cancels it in the daemon too).
final class TurnRunner {
    let client: UnixSocketClient
    let tokens: TokenProvider
    private var current: CancelToken?

    init(client: UnixSocketClient = UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath()),
         tokens: TokenProvider = TokenProvider(store: KeychainTokenStore())) {
        self.client = client
        self.tokens = tokens
    }

    func cancel() {
        current?.cancel()
        current = nil
    }

    /// - onNote: out-of-band status text (e.g. "using the CLI token").
    /// - onFinish: nil on a clean end of stream, else the error.
    func run(channel: Channel, prompt: String,
             onEvent: @escaping (TurnEvent) -> Void,
             onNote: @escaping (String) -> Void,
             onFinish: @escaping (Error?) -> Void) {
        cancel()
        let token = CancelToken()
        current = token
        let client = self.client, tokens = self.tokens
        DispatchQueue.global(qos: .userInitiated).async {
            let deliver: (TurnEvent) -> Void = { e in DispatchQueue.main.async { if !token.isCancelled { onEvent(e) } } }
            var failure: Error?
            do {
                let tok = try tokens.token()
                do {
                    try client.streamTurn(channel: channel, prompt: prompt, token: tok, cancel: token, onEvent: deliver)
                } catch WaterClientError.http(status: 401, body: _) {
                    // Current daemons re-read clients.json on an unknown
                    // token, so this only fires against an older daemon that
                    // loaded it once at startup. Fall back to the CLI's own
                    // token (what `water ask` uses) rather than failing.
                    guard let cli = tokens.cliFallbackToken(), cli != tok else { throw WaterClientError.http(status: 401, body: "invalid token") }
                    DispatchQueue.main.async {
                        onNote("The daemon rejected this app's token (an older daemon loads tokens only at startup) — using the CLI token. Restart or update `water daemon` to fix.")
                    }
                    try client.streamTurn(channel: channel, prompt: prompt, token: cli, cancel: token, onEvent: deliver)
                }
            } catch {
                failure = error
            }
            DispatchQueue.main.async {
                if case WaterClientError.cancelled? = failure as? WaterClientError { return }
                if !token.isCancelled { onFinish(failure) }
            }
        }
    }
}
