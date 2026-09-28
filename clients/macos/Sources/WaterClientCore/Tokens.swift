import Foundation
import Security

/// Where the client's daemon bearer token is kept.
public protocol TokenStoring {
    func read() throws -> String?
    func write(_ token: String) throws
    func delete() throws
}

/// The token in the macOS login keychain as a generic password
/// (service `water.client`, account `macos` by default), via the Security
/// framework's SecItem APIs. The item is created by this app, so the
/// keychain's default ACL trusts this app to read it back without a prompt —
/// until the binary is rebuilt: an ad-hoc signature changes with every build,
/// so the first read after a rebuild shows one "allow access" prompt.
public struct KeychainTokenStore: TokenStoring {
    public var service: String
    public var account: String

    public init(service: String = "water.client", account: String = "macos") {
        self.service = service
        self.account = account
    }

    private var baseQuery: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }

    public func read() throws -> String? {
        var q = baseQuery
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let st = SecItemCopyMatching(q as CFDictionary, &out)
        if st == errSecItemNotFound { return nil }
        guard st == errSecSuccess, let data = out as? Data else { throw KeychainError(status: st, op: "read") }
        let s = String(decoding: data, as: UTF8.self)
        return s.isEmpty ? nil : s
    }

    public func write(_ token: String) throws {
        guard !token.isEmpty, !token.contains(where: { $0.isNewline }) else {
            throw WaterClientError.noToken("refusing to store an empty or multi-line token")
        }
        let data = Data(token.utf8)
        let upd = SecItemUpdate(baseQuery as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if upd == errSecSuccess { return }
        guard upd == errSecItemNotFound else { throw KeychainError(status: upd, op: "update") }
        var add = baseQuery
        add[kSecValueData as String] = data
        add[kSecAttrLabel as String] = "\(service) (\(account))"
        let st = SecItemAdd(add as CFDictionary, nil)
        guard st == errSecSuccess else { throw KeychainError(status: st, op: "add") }
    }

    public func delete() throws {
        let st = SecItemDelete(baseQuery as CFDictionary)
        guard st == errSecSuccess || st == errSecItemNotFound else { throw KeychainError(status: st, op: "delete") }
    }
}

public struct KeychainError: Error, LocalizedError {
    public let status: OSStatus
    public let op: String
    public var errorDescription: String? {
        let msg = (SecCopyErrorMessageString(status, nil) as String?) ?? "OSStatus \(status)"
        return "keychain \(op): \(msg)"
    }
}

/// An in-memory store, for tests and the headless self-test.
public final class MemoryTokenStore: TokenStoring {
    private var value: String?
    public init(_ value: String? = nil) { self.value = value }
    public func read() throws -> String? { value }
    public func write(_ token: String) throws { value = token }
    public func delete() throws { value = nil }
}

/// The daemon's client table, `~/.water/run/clients.json` (0600): a JSON
/// object mapping token -> client name (internal/gateway/tokens.go).
public enum ClientsFile {
    public static func defaultPath(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
        UnixSocketClient.waterHome(environment: environment) + "/run/clients.json"
    }

    /// The token registered under `name`, if any.
    public static func token(named name: String, path: String) -> String? {
        guard let data = FileManager.default.contents(atPath: path),
              let table = (try? JSONSerialization.jsonObject(with: data)) as? [String: String] else { return nil }
        // Deterministic if a name was ever minted twice.
        return table.filter { $0.value == name }.keys.sorted().first
    }
}

/// Resolves this client's bearer token:
/// 1. the keychain;
/// 2. else a token already registered under `clientName` in clients.json
///    (e.g. one minted earlier with `water daemon token new macos-client`);
/// 3. else mints one by running `water daemon token new <clientName>`.
/// Whatever it finds in 2 or 3 is saved to the keychain for next time.
///
/// A running daemon re-reads clients.json when it sees an unknown token, so
/// a freshly minted token works at once. Daemons built before that change
/// loaded the file only at startup and reject (401) a new token until they
/// restart; `cliFallbackToken` covers only that case, with the CLI's own
/// token (internal/cli/daemonclient.go reads the "cli" entry the same way).
public final class TokenProvider {
    public let store: TokenStoring
    public let clientsPath: String
    public let clientName: String
    public let waterBinaryCandidates: [String]

    public init(store: TokenStoring,
                clientsPath: String = ClientsFile.defaultPath(),
                clientName: String = "macos-client",
                waterBinaryCandidates: [String] = TokenProvider.defaultWaterBinaries()) {
        self.store = store
        self.clientsPath = clientsPath
        self.clientName = clientName
        self.waterBinaryCandidates = waterBinaryCandidates
    }

    public static func defaultWaterBinaries(environment: [String: String] = ProcessInfo.processInfo.environment) -> [String] {
        var c: [String] = []
        if let b = environment["WATER_BIN"], !b.isEmpty { c.append(b) }
        c += [NSHomeDirectory() + "/.local/bin/water", "/opt/homebrew/bin/water", "/usr/local/bin/water"]
        return c
    }

    public func token() throws -> String {
        if let t = try store.read() { return t }
        let t: String
        if let existing = ClientsFile.token(named: clientName, path: clientsPath) {
            t = existing
        } else {
            t = try mint()
        }
        try store.write(t)
        return t
    }

    public func cliFallbackToken() -> String? {
        ClientsFile.token(named: "cli", path: clientsPath)
    }

    func mint() throws -> String {
        guard let bin = waterBinaryCandidates.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
            throw WaterClientError.noToken("no `water` binary found (tried \(waterBinaryCandidates.joined(separator: ", "))); run `water daemon token new \(clientName)` once")
        }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        p.arguments = ["daemon", "token", "new", clientName]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        let tok = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        guard p.terminationStatus == 0, !tok.isEmpty, !tok.contains(where: { $0.isWhitespace }) else {
            throw WaterClientError.noToken("`\(bin) daemon token new \(clientName)` failed (exit \(p.terminationStatus))")
        }
        return tok
    }
}
