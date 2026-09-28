import Foundation
import Testing
@testable import WaterClientCore

@Suite struct TokenTests {
    private func tempDir() throws -> URL {
        let d = FileManager.default.temporaryDirectory.appendingPathComponent("water-tok-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }

    @Test func clientsFileLookup() throws {
        let d = try tempDir()
        defer { try? FileManager.default.removeItem(at: d) }
        let f = d.appendingPathComponent("clients.json")
        try #"{"aaa": "cli", "bbb": "macos-client"}"#.write(to: f, atomically: true, encoding: .utf8)
        #expect(ClientsFile.token(named: "cli", path: f.path) == "aaa")
        #expect(ClientsFile.token(named: "macos-client", path: f.path) == "bbb")
        #expect(ClientsFile.token(named: "nope", path: f.path) == nil)
        #expect(ClientsFile.token(named: "cli", path: f.path + ".missing") == nil)
    }

    @Test func providerPrefersStoreThenClientsFileAndSaves() throws {
        let d = try tempDir()
        defer { try? FileManager.default.removeItem(at: d) }
        let f = d.appendingPathComponent("clients.json")
        try #"{"aaa": "cli", "bbb": "macos-client"}"#.write(to: f, atomically: true, encoding: .utf8)
        let store = MemoryTokenStore()
        let p = TokenProvider(store: store, clientsPath: f.path, waterBinaryCandidates: [])
        #expect(try p.token() == "bbb")
        #expect(try store.read() == "bbb", "saved to the store for next time")
        #expect(p.cliFallbackToken() == "aaa")

        let p2 = TokenProvider(store: MemoryTokenStore("kept"), clientsPath: f.path, waterBinaryCandidates: [])
        #expect(try p2.token() == "kept")
    }

    @Test func providerMintsWithWaterBinary() throws {
        let d = try tempDir()
        defer { try? FileManager.default.removeItem(at: d) }
        let fake = d.appendingPathComponent("water")
        let argsFile = d.appendingPathComponent("args")
        try "#!/bin/sh\necho \"$@\" > '\(argsFile.path)'\necho deadbeef01\n".write(to: fake, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: fake.path)
        let store = MemoryTokenStore()
        let p = TokenProvider(store: store, clientsPath: d.appendingPathComponent("none.json").path,
                              waterBinaryCandidates: [d.appendingPathComponent("missing").path, fake.path])
        #expect(try p.token() == "deadbeef01")
        #expect(try String(contentsOf: argsFile, encoding: .utf8) == "daemon token new macos-client\n")
        #expect(try store.read() == "deadbeef01")
    }

    @Test func providerWithNoSourceFails() {
        let p = TokenProvider(store: MemoryTokenStore(), clientsPath: "/nonexistent/clients.json", waterBinaryCandidates: [])
        #expect(throws: WaterClientError.self) { try p.token() }
    }

    /// Touches the real login keychain, so it runs only when asked — the same
    /// gate as internal/vault/keychain_darwin_test.go:
    /// WATER_KEYCHAIN_TEST=1 swift test
    @Test(.enabled(if: ProcessInfo.processInfo.environment["WATER_KEYCHAIN_TEST"] == "1"))
    func keychainRoundTrip() throws {
        let k = KeychainTokenStore(service: "water.client.selftest", account: "probe")
        try k.delete()
        #expect(try k.read() == nil)
        for v in ["first-token", "second-token"] {
            try k.write(v)
            #expect(try k.read() == v)
        }
        #expect(throws: WaterClientError.self) { try k.write("two\nlines") }
        try k.delete()
        #expect(try k.read() == nil)
        try k.delete() // deleting a missing item is not an error
    }
}
