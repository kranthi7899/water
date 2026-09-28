import Foundation
import Testing
@testable import WaterClientCore

/// A tripwire, not a dependency-graph auditor. This project's rule is "ask
/// before adding a dependency" (CLAUDE.md's Go-focused wording, extended to
/// the Swift client in docs/slices/V.md #1). Before slice V, Package.swift's
/// own comment said "No third-party packages — Apple frameworks only".
/// FluidAudio (task V-1) was the first exception, explicitly approved and
/// pinned to an exact version, not a range. This test fails loudly the
/// moment either fact stops being true — a new pin appears, or FluidAudio's
/// own pin drifts off "0.17.4" — so a future session adding a dependency
/// without going through the same approval step notices immediately instead
/// of it slipping in quietly.
@Suite struct DependencyPolicyTests {
    /// Package.swift and Package.resolved live at the package root, fixed
    /// relative to this test file (Tests/WaterClientCoreTests/ ->
    /// clients/macos/) regardless of the process's current directory, which
    /// differs between `./test.sh` and other invocations.
    static var packageRoot: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // DependencyPolicyTests.swift -> WaterClientCoreTests/
            .deletingLastPathComponent() // WaterClientCoreTests/ -> Tests/
            .deletingLastPathComponent() // Tests/ -> clients/macos/
    }

    @Test func onlyFluidAudioIsPinnedInTheLockfile() throws {
        let data = try Data(contentsOf: Self.packageRoot.appendingPathComponent("Package.resolved"))
        let resolved = try JSONDecoder().decode(ResolvedFile.self, from: data)

        // Deliberately not asserting on FluidAudio's own transitive
        // dependencies: Package.resolved's flat pin list doesn't distinguish
        // "our direct dependency" from "something FluidAudio itself depends
        // on", and whatever FluidAudio chooses to pull in is FluidAudio's
        // call, not ours to police. If that ever changes, this count
        // assertion fails and a future session can decide then whether the
        // new pins need their own scrutiny.
        let identities = resolved.pins.map { $0.identity }
        #expect(
            resolved.pins.count == 1,
            "expected only FluidAudio pinned beyond the pre-slice-V Apple-frameworks-only baseline; got \(identities)"
        )

        let fluidAudio = try #require(
            resolved.pins.first { $0.identity == "fluidaudio" },
            "FluidAudio is missing from Package.resolved"
        )
        #expect(
            fluidAudio.state.version == "0.17.4",
            "FluidAudio's resolved version drifted from the approved 0.17.4 (docs/slices/V.md #1)"
        )
    }

    @Test func fluidAudioIsPinnedExactNotARange() throws {
        // Package.resolved alone can't tell "exact" from "range that
        // happened to resolve to 0.17.4" — that constraint only lives in
        // Package.swift's dependency declaration, so this test reads it
        // there instead of duplicating a Swift-manifest parser.
        let manifest = try String(contentsOf: Self.packageRoot.appendingPathComponent("Package.swift"), encoding: .utf8)
        let fluidAudioLine = try #require(
            manifest.split(separator: "\n").first { $0.contains("FluidAudio") && $0.contains(".package(") },
            "no FluidAudio entry found in Package.swift's dependencies"
        )
        #expect(
            fluidAudioLine.contains(#"exact: "0.17.4""#),
            "FluidAudio must stay pinned with `exact:`, not a range (from:/upToNextMajor/etc.) — got: \(fluidAudioLine)"
        )
    }
}

private struct ResolvedFile: Decodable {
    let pins: [Pin]

    struct Pin: Decodable {
        let identity: String
        let state: State

        struct State: Decodable {
            let version: String?
        }
    }
}
